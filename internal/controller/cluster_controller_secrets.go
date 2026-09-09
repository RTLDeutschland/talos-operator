package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// secretMutator is a struct we use to genericize secret generation and updating logic, see cluster reconciler.
type secretMutator struct {
	Name      string
	Namespace string
	Helper    func(context.Context, *talosv1alpha1.Cluster, *corev1.Secret) error
}

// errNothingToDoHere is a sentinel error used to indicate that the mutate function can't do anything here yet, so the caller should just return and wait for the next reconciliation loop.
var errNothingToDoHere = errors.New("can't do anything here yet")

// getFirstProvisionedControllerNode returns the first provisioned controller node for the cluster.
// Returns nothingToDoHereError if no provisioned controller nodes are found.
func (r *ClusterReconciler) getFirstProvisionedControllerNode(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
) (*talosv1alpha1.Node, error) {
	nodes, err := r.listEligibleNodes(ctx, cluster, Controlplane)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no provisioned controller nodes: %w", errNothingToDoHere)
	}
	return &nodes[0], nil
}

// updateSecret is a helper function that checks if a secret exists and is fresh, and if not, it calls the provided function to generate a new secret and updates it in the cluster.
func (r *ClusterReconciler) updateSecret(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
	key client.ObjectKey,
	mutateFn func(context.Context, *talosv1alpha1.Cluster, *corev1.Secret) error,
) error {
	// fetch the secret:
	secret := &corev1.Secret{}
	getErr := r.Get(ctx, key, secret)
	if getErr != nil {
		if !apierrors.IsNotFound(getErr) {
			return fmt.Errorf(
				"failed to get secret %s/%s: %w",
				key.Namespace,
				key.Name,
				getErr,
			)
		} else if apierrors.IsNotFound(getErr) {
			secret.ObjectMeta = ctrl.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
			}
		}
	} else {
		// if secret exists and it's fresh:
		if secret.Annotations == nil {
			secret.Annotations = make(map[string]string)
		}
		if ts, ok := secret.Annotations["last-refresh-time"]; ok {
			lastRefreshTime, err := time.Parse(time.RFC3339, ts)
			if err == nil && time.Since(lastRefreshTime) < 24*time.Hour {
				// secret is fresh, no need to regenerate
				return nil
			}
		}
	}

	// secret needs to be updated
	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	secret.Annotations["last-refresh-time"] = time.Now().Format(time.RFC3339)
	// only set an owner reference if the secret is in the same namespace.
	// otherwise the Kubernetes garbage collector complains and deletes it immediately.
	if secret.Namespace == cluster.Namespace && len(secret.OwnerReferences) == 0 {
		secret.OwnerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(cluster, talosv1alpha1.GroupVersion.WithKind("Cluster")),
		}
	}
	// so for cross-namespace, we use a label to indicate ownership.
	if secret.Labels == nil {
		secret.Labels = make(map[string]string)
	}
	_, ok := secret.Labels["ownerName"]
	if secret.Namespace != cluster.Namespace && !ok {
		gvk := cluster.GroupVersionKind()
		secret.Labels["ownerGroup"] = gvk.Group
		secret.Labels["ownerVersion"] = gvk.Version
		secret.Labels["ownerKind"] = gvk.Kind
		secret.Labels["ownerName"] = cluster.Name
		secret.Labels["ownerNamespace"] = cluster.Namespace
	}

	// run the mutate helper:
	err := mutateFn(ctx, cluster, secret)
	if errors.Is(err, errNothingToDoHere) {
		// this is a special error that indicates we can't do anything here yet
		// (e.g. no controller nodes to generate a kubeconfig from),
		// so we just return and wait for the next reconciliation loop.
		return nil
	} else if err != nil {
		return fmt.Errorf("failed secret mutation %s/%s: %w", key.Namespace, key.Name, err)
	}

	// and update it in Kubernetes.
	if apierrors.IsNotFound(getErr) {
		err = r.Create(ctx, secret)
	} else {
		err = r.Update(ctx, secret)
	}
	if err != nil {
		return fmt.Errorf(
			"failed to create/update secret %s/%s: %w",
			key.Namespace,
			key.Name,
			err,
		)
	}
	r.Recorder.Eventf(
		cluster,
		secret,
		corev1.EventTypeNormal,
		EventReasonUpdated,
		EventActionSecretReconcile,
		"Secret refreshed: %s/%s",
		secret.Namespace,
		secret.Name,
	)
	return nil
}

// mutateKubeconfigSecret is a helper function that generates a kubeconfig from the first controller node and updates the provided secret with the new kubeconfig data and annotations.
func (r *ClusterReconciler) mutateKubeconfigSecret(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
	secret *corev1.Secret,
) error {
	node, err := r.getFirstProvisionedControllerNode(ctx, cluster)
	if err != nil {
		return err
	}

	// generate kubeconfig from the first controller node
	tCtx, talosClient, err := getTalosClient(ctx, r.Client, node, cluster)
	if err != nil {
		return err
	}
	defer talosClient.Close()
	kubeconfigBytes, err := talosClient.Kubeconfig(tCtx)
	if err != nil {
		return err
	}
	secret.Data = map[string][]byte{
		"kubeconfig": kubeconfigBytes,
	}
	return nil
}

// mutateTalosconfigSecret is a helper function that generates a talosconfig from the first controller node and updates the provided secret with the new talosconfig data and annotations.
func (r *ClusterReconciler) mutateTalosconfigSecret(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
	secret *corev1.Secret,
) error {
	node, err := r.getFirstProvisionedControllerNode(ctx, cluster)
	if err != nil {
		return err
	}

	// generate talosconfig from the first controller node
	talosClientConfig, err := getTalosClientConfig(ctx, r.Client, node, cluster)
	if err != nil {
		return err
	}
	talosClientConfigBytes, err := talosClientConfig.Bytes()
	if err != nil {
		return err
	}
	secret.Data = map[string][]byte{
		"talosconfig": talosClientConfigBytes,
	}
	return nil
}

// mutateArgoCDSecret is a helper function that generates an Argo CD secret from the first controller node and updates the provided secret with the new data and annotations.
func (r *ClusterReconciler) mutateArgoCDSecret(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
	secret *corev1.Secret,
) error {
	node, err := r.getFirstProvisionedControllerNode(ctx, cluster)
	if err != nil {
		return err
	}

	// generate Argo CD secret data from the first controller node
	tCtx, talosClient, err := getTalosClient(ctx, r.Client, node, cluster)
	if err != nil {
		return err
	}
	defer talosClient.Close()
	kubeconfigBytes, err := talosClient.Kubeconfig(tCtx)
	if err != nil {
		return fmt.Errorf("failed to generate kubeconfig: %w", err)
	}

	// parse kubeconfig:
	kubeconfig, err := clientcmd.Load(kubeconfigBytes)
	if err != nil {
		return fmt.Errorf("failed to parse kubeconfig: %w", err)
	}
	if kubeconfig.CurrentContext == "" {
		return fmt.Errorf("kubeconfig has no current context")
	}

	// assemble argo CD config data
	contextData, ok := kubeconfig.Contexts[kubeconfig.CurrentContext]
	if !ok {
		return fmt.Errorf("kubeconfig is missing expected context: %s", kubeconfig.CurrentContext)
	}
	clusterData, ok1 := kubeconfig.Clusters[contextData.Cluster]
	authInfoData, ok2 := kubeconfig.AuthInfos[contextData.AuthInfo]
	if !ok1 || !ok2 {
		return fmt.Errorf(
			"kubeconfig is missing expected data: cluster ok: %t, auth info ok: %t",
			ok1,
			ok2,
		)
	}

	// https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/#clusters
	argoCDConfig := KV{
		"tlsClientConfig": KV{
			"caData":   clusterData.CertificateAuthorityData,
			"certData": authInfoData.ClientCertificateData,
			"keyData":  authInfoData.ClientKeyData,
			"insecure": false,
		},
	}
	serializedArgoCDConfig, err := json.Marshal(argoCDConfig)
	if err != nil {
		return fmt.Errorf("failed to serialize Argo CD config: %w", err)
	}

	featureOptions := cluster.Spec.Options.ArgoCDSecret

	if secret.Labels == nil {
		secret.Labels = make(map[string]string)
	}
	if len(featureOptions.ExtraLabels) > 0 {
		maps.Copy(secret.Labels, featureOptions.ExtraLabels)
	}
	secret.Labels["argocd.argoproj.io/secret-type"] = "cluster"
	secret.Data = map[string][]byte{
		"name": []byte(
			lo.CoalesceOrEmpty(featureOptions.ClusterNameOverride, cluster.Name),
		),
		"server": []byte(clusterData.Server),
		"config": serializedArgoCDConfig,
	}
	return nil
}
