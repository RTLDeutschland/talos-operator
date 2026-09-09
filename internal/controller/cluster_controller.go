// Package controller implements the Talos operator reconciliation logic for Cluster and Node resources.
package controller

import (
	"context"
	"fmt"

	"github.com/samber/lo"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/config"
	talossecrets "github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"go.yaml.in/yaml/v4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	iapi "github.com/RTLDeutschland/talos-operator/internal/api"
	. "github.com/RTLDeutschland/talos-operator/internal/constants"
	"github.com/RTLDeutschland/talos-operator/internal/keyedmutex"
	"github.com/RTLDeutschland/talos-operator/internal/logz"
)

const DefaultKubernetesVersion = "v" + constants.DefaultKubernetesVersion

// ClusterReconciler reconciles a Cluster object by managing its state and coordinating
// cluster-wide operations such as rolling reboots, rolling applies, and Kubernetes upgrades.
// It also manages maintenance windows and automatically triggers operations when appropriate.
type ClusterReconciler struct {
	client.Client
	APIReader    client.Reader
	Scheme       *runtime.Scheme
	Recorder     events.EventRecorderLogger
	Mutexes      *keyedmutex.MutexMap
	FeatureFlags iapi.FeatureFlags
}

// +kubebuilder:rbac:groups=talos.rtl.de,resources=clusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=talos.rtl.de,resources=clusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=talos.rtl.de,resources=clusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=talos.rtl.de,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;update;get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="events.k8s.io",resources=events,verbs=create;patch

// Reconcile compares the state of the cluster resource with the real physical
// Talos cluster, identifies changes that need to be made, and makes those
// changes, usually* with user approval.
//
// *:
//   - Sane defaults: the Cluster reconciler will automatically fill
//     spec.secretsRef and spec.kubernetesVersion if left empty.
//     If spec.secretsRef references a non-existent secret, it will generate a
//     new secrets bundle for the cluster.
//   - Auto-Bootstrap: We identify clusters that have all Ready = True nodes with
//     last ClusterOperationTypeRollingApply type = Provision, and we automatically Bootstrap the first
//     control plane.
//   - MaintenanceWindow: If the cluster is inside a maintenance window (requires
//     prior feature enablement), then we autonomously start RollingApply or
//     KubernetesUpgrades where applicable, if the last cluster operation isn't
//     in a failed state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = context.WithValue(ctx, CtxKeyRequest, &req)
	log := logz.New(ctx, "cluster.root")
	log.Info().Msg("Reconciling Cluster")

	cluster := &talosv1alpha1.Cluster{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// attach cluster to context
	ctx = context.WithValue(ctx, CtxKeyCluster, cluster)

	// set a default Kubernetes version if not set
	if cluster.Spec.KubernetesVersion == "" {
		err := r.updateCluster(ctx, func(c *talosv1alpha1.Cluster) {
			c.Spec.KubernetesVersion = DefaultKubernetesVersion
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		log.Info().
			Str("kubernetes_version", DefaultKubernetesVersion).
			Msg("Set default Kubernetes version")
	}

	// set secretsRef if empty
	if cluster.Spec.SecretsRef == "" {
		err := r.updateCluster(ctx, func(c *talosv1alpha1.Cluster) {
			c.Spec.SecretsRef = c.Name + "-secrets"
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}

	// generate a secret if secretsRef points to a secret that doesn't exist
	clusterSecret := &corev1.Secret{}
	clusterSecret.Name = cluster.Spec.SecretsRef
	clusterSecret.Namespace = cluster.Namespace
	err := r.Get(ctx, client.ObjectKeyFromObject(clusterSecret), clusterSecret)
	if apierrors.IsNotFound(err) {
		// TODO: cluster option to disable secrets auto-gen?
		bundle, err := talossecrets.NewBundle(
			talossecrets.NewClock(),
			talosconfig.TalosVersionCurrent,
		)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error generating new secrets bundle: %w", err)
		}
		secretsYaml, err := yaml.Marshal(bundle)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error marshalling generated secrets bundle: %w", err)
		}
		clusterSecret.Immutable = lo.ToPtr(true)
		clusterSecret.Data = map[string][]byte{"secrets.yaml": secretsYaml}
		err = r.Create(ctx, clusterSecret)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error saving generated talos secrets bundle: %w", err)
		}
		r.Recorder.Eventf(
			cluster,
			clusterSecret,
			corev1.EventTypeNormal,
			EventReasonCreated,
			EventActionSecretReconcile,
			"Talos secrets bundle generated successfully for cluster %s/%s",
			cluster.Namespace,
			cluster.Name,
		)
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("error fetching cluster secrets bundle: %w", err)
	}

	// update cluster status
	err = r.updateStatus(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error updating cluster status: %w", err)
	}

	// update secrets
	secretHelpers := []secretMutator{
		{Name: "kubeconfig", Namespace: cluster.Namespace, Helper: r.mutateKubeconfigSecret},
		{Name: "talosconfig", Namespace: cluster.Namespace, Helper: r.mutateTalosconfigSecret},
	}
	argoCDSecret := cluster.Spec.Options.ArgoCDSecret
	if argoCDSecret.Enabled != nil && *argoCDSecret.Enabled {
		secretHelpers = append(secretHelpers, secretMutator{
			Name: fmt.Sprintf(
				"%s-argocd-cluster",
				cluster.Namespace,
			), // cluster name is still prepended to this...
			Namespace: argoCDSecret.Namespace,
			Helper:    r.mutateArgoCDSecret,
		})
	}
	for _, sh := range secretHelpers {
		secretKey := client.ObjectKey{
			Name:      fmt.Sprintf("%s-%s", cluster.Name, sh.Name), // <- here
			Namespace: sh.Namespace,
		}
		if err := r.updateSecret(ctx, cluster, secretKey, sh.Helper); err != nil {
			return ctrl.Result{}, fmt.Errorf("error updating %s secret: %w", sh.Name, err)
		}
	}

	// automatically trigger bootstrap if cluster is not bootstrapped but ready for it
	autoBootstrapEnabled := cluster.Spec.Options.AutoBootstrap == nil ||
		*cluster.Spec.Options.AutoBootstrap
	if cluster.Operation == nil &&
		(cluster.Status.Operation == nil || cluster.Status.Operation.Phase == ClusterOperationPhaseDone) &&
		meta.IsStatusConditionFalse(cluster.Status.Conditions, ClusterConditionBootstrapped) &&
		autoBootstrapEnabled {
		// check if we have at least one provisioned controlplane node,
		// and verify all provisioned controlplanes have completed their Provision operation
		controlplaneNodes, err := r.listEligibleNodes(ctx, cluster, Controlplane)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"error listing controlplane nodes for cluster %s: %w",
				cluster.Name,
				err,
			)
		}

		readyForBootstrap := len(controlplaneNodes) > 0
		for _, node := range controlplaneNodes {
			// check that the node is Ready
			if !meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionReady) {
				readyForBootstrap = false
				break
			}
			// check that the provision operation is done
			if node.Status.Operation != nil &&
				node.Status.Operation.Phase != NodeOperationPhaseDone {
				readyForBootstrap = false
				break
			}
			// check that the last completed operation was Provision
			if node.Status.Operation != nil &&
				node.Status.Operation.Type != NodeOperationTypeProvision {
				readyForBootstrap = false
				break
			}
		}
		if readyForBootstrap {
			// trigger automatic bootstrap
			err = r.updateClusterStatus(ctx, func(c *talosv1alpha1.Cluster) {
				c.Status.Operation = &talosv1alpha1.ClusterOperationStatus{
					ClusterOperation: talosv1alpha1.ClusterOperation{
						Type: ClusterOperationTypeBootstrap,
					},
					Phase:              ClusterOperationPhasePending,
					Reason:             OperationReasonAutomaticallyTriggered,
					Message:            "Triggered automatic cluster bootstrap",
					LastTransitionTime: metav1.Now(),
				}
			})
			if err != nil {
				return ctrl.Result{}, fmt.Errorf(
					"failed to set automatic bootstrap operation status: %w",
					err,
				)
			}
			r.Recorder.Eventf(
				cluster,
				nil,
				corev1.EventTypeNormal,
				EventReasonStarted,
				EventActionBootstrap,
				"Triggered automatic cluster bootstrap",
			)
		}
	}

	// decide based on status and maintenance window, whether to automatically trigger operations
	//
	// NextMaintenanceWindow is set in updateMaintenanceStatus which handles enablement of the
	// maintenance window feature, so we can rely on IsZero here.
	inMaintenanceWindow := !cluster.Status.NextMaintenanceWindow.IsZero() &&
		cluster.Status.NextMaintenanceWindow.Before(lo.ToPtr(metav1.Now()))
	didLastClusterOperationCompleteSuccessfully := cluster.Operation == nil &&
		(cluster.Status.Operation == nil || cluster.Status.Operation.Phase == ClusterOperationPhaseDone)
	if inMaintenanceWindow && didLastClusterOperationCompleteSuccessfully {
		var op *talosv1alpha1.ClusterOperation
		// if not all cluster nodes are in sync:
		if meta.IsStatusConditionFalse(cluster.Status.Conditions, ClusterConditionNodesInSync) {
			// trigger rolling apply
			op = &talosv1alpha1.ClusterOperation{
				Type: ClusterOperationTypeRollingApply,
			}
			r.Recorder.Eventf(
				cluster,
				nil,
				corev1.EventTypeNormal,
				EventReasonStarted,
				EventActionRollingApply,
				"Triggered automatic rolling apply due to some nodes needing config apply and in maintenance window",
			)

			// or if some cluster nodes need kubernetes updates:
		} else if meta.IsStatusConditionFalse(cluster.Status.Conditions, ClusterConditionNodesKubernetesUpToDate) {
			// trigger kubernetes upgrade
			op = &talosv1alpha1.ClusterOperation{
				Type: ClusterOperationTypeKubernetesUpgrade,
			}
			r.Recorder.Eventf(
				cluster,
				nil,
				corev1.EventTypeNormal,
				EventReasonStarted,
				EventActionKubernetesUpgrade,
				"Triggered automatic kubernetes upgrade due to some nodes needing kubernetes updates and in maintenance window",
			)
		}

		if op != nil {
			// set operation on cluster
			err = r.updateClusterStatus(ctx, func(c *talosv1alpha1.Cluster) {
				c.Status.Operation = &talosv1alpha1.ClusterOperationStatus{
					ClusterOperation:   *op,
					Phase:              ClusterOperationPhasePending,
					Reason:             OperationReasonAutomaticallyTriggered,
					Message:            "Triggered automatic operation due to maintenance window",
					LastTransitionTime: metav1.Now(),
				}
			})
			if err != nil {
				return ctrl.Result{}, fmt.Errorf(
					"failed to set automatic Cluster Operation status: %w",
					err,
				)
			}
			return ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
		}
	}

	// init operation if needed
	if cluster.Operation != nil {
		// copy operation to status
		err := r.updateClusterStatus(ctx, func(c *talosv1alpha1.Cluster) {
			c.Status.Operation = &talosv1alpha1.ClusterOperationStatus{
				ClusterOperation:   *cluster.Operation,
				Phase:              ClusterOperationPhasePending,
				Reason:             OperationReasonInitialized,
				Message:            "Cluster operation has been initialized",
				LastTransitionTime: metav1.Now(),
			}
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"failed to initialize Cluster Operation status: %w",
				err,
			)
		}

		// zero out operation
		err = r.updateCluster(ctx, func(c *talosv1alpha1.Cluster) {
			c.Operation = nil
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to clear Cluster Operation: %w", err)
		}

		return ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}

	if r.isOperationComplete(cluster) {
		// operation is done, exit early
		return ctrl.Result{RequeueAfter: defaultRefreshInterval()}, nil
	}

	// run cluster operations
	var res *ctrl.Result
	err = nil
	switch cluster.Status.Operation.Type {
	case ClusterOperationTypeRollingReboot:
		res, err = r.handleRollingRebootOperation(ctx, cluster)
	case ClusterOperationTypeRollingApply:
		res, err = r.handleRollingApplyOperation(ctx, cluster)
	case ClusterOperationTypeKubernetesUpgrade:
		res, err = r.handleKubernetesUpgrade(ctx, cluster)
	case ClusterOperationTypeBootstrap:
		res, err = r.handleBootstrapOperation(ctx, cluster)
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf(
			"error performing %s operation: %w",
			cluster.Status.Operation.Type,
			err,
		)
	}
	if res != nil {
		log.Debug().
			Dur("requeue_after", res.RequeueAfter).
			Msgf("%s operation requeued", cluster.Status.Operation.Type)
		return *res, nil
	}

	return ctrl.Result{RequeueAfter: defaultRefreshInterval()}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&talosv1alpha1.Cluster{}).
		Named("cluster").
		Complete(r)
}
