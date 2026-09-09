package controller

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/RTLDeutschland/talos-operator/internal/logz"
	tclient "github.com/siderolabs/talos/pkg/machinery/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubectl/pkg/drain"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// these are globals, yes, one would hope if someone is using this operator to manage tens of
// thousands of clusters that they provision the appropriate amount of RAM for that.
var kubernetesClientsetCacheMutex sync.Mutex
var kubernetesClientsetCache = make(map[string]*kubernetes.Clientset)
var kubernetesCRClientCacheMutex sync.Mutex
var kubernetesCRClientCache = make(map[string]client.Client)

// getKubeconfig retrieves the kubeconfig for the given cluster by using the Talos API.
func getKubeconfig(
	ctx context.Context,
	operatorClient client.Client,
	cluster *talosv1alpha1.Cluster,
) (*rest.Config, error) {
	// find controlplane node for kubeconfig
	nodeList := &talosv1alpha1.NodeList{}
	err := operatorClient.List(
		ctx,
		nodeList,
		client.InNamespace(cluster.Namespace),
		client.MatchingFields{
			"spec.clusterRef": cluster.Name,
			"spec.role":       Controlplane,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list controlplane nodes: %w", err)
	}
	if len(nodeList.Items) == 0 {
		return nil, fmt.Errorf("no controlplane nodes found for cluster %s", cluster.Name)
	}
	// find the first Ready = True node
	// (our Ready meaning reachable, not KubernetesReady)
	var controlPlaneNode *talosv1alpha1.Node
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		if meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionReady) {
			controlPlaneNode = node
			break
		}
	}
	if controlPlaneNode == nil {
		return nil, fmt.Errorf("no Ready controlplane nodes found for cluster %s", cluster.Name)
	}

	// connect to it and get a kubeconfig
	talosCPCtx, talosCPClient, err := getTalosClient(ctx, operatorClient, controlPlaneNode, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get talos controlplane client: %w", err)
	}
	defer talosCPClient.Close()
	kubeconfigBytes, err := talosCPClient.Kubeconfig(talosCPCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// and parse it
	kubeconfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to create rest config from kubeconfig: %w", err)
	}
	if kubeconfig == nil {
		return nil, fmt.Errorf("kubeconfig is nil?")
	}
	return kubeconfig, nil
}

// getKubernetesClient returns a cached Kubernetes clientset for the given cluster, or creates a new one if not cached.
func getKubernetesClient(
	ctx context.Context,
	operatorClient client.Client,
	cluster *talosv1alpha1.Cluster,
) (*kubernetes.Clientset, error) {
	kubernetesClientsetCacheMutex.Lock()
	defer kubernetesClientsetCacheMutex.Unlock()

	cacheKey := fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name)
	if clientSet, exists := kubernetesClientsetCache[cacheKey]; exists {
		// verify that the client is still valid, otherwise we fall through to making a new one
		_, err := clientSet.Discovery().ServerVersion()
		if err == nil {
			return clientSet, nil
		}
	}

	kubeconfig, err := getKubeconfig(ctx, operatorClient, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	remoteClient, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create remote kubernetes client: %w", err)
	}

	kubernetesClientsetCache[cacheKey] = remoteClient
	return remoteClient, nil
}

// getKubernetesCRClient returns a cached Kubernetes controller-runtime client for the given cluster, or creates a new one if not cached.
func getKubernetesCRClient(
	ctx context.Context,
	operatorClient client.Client,
	cluster *talosv1alpha1.Cluster,
) (client.Client, error) {
	kubernetesCRClientCacheMutex.Lock()
	defer kubernetesCRClientCacheMutex.Unlock()

	cacheKey := fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name)
	if crClient, exists := kubernetesCRClientCache[cacheKey]; exists {
		// verify that the client is still valid, otherwise we fall through to making a new one
		nodes := &corev1.NodeList{}
		err := crClient.List(ctx, nodes)
		if err == nil {
			return crClient, nil
		}
	}

	kubeconfig, err := getKubeconfig(ctx, operatorClient, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	client, err := client.New(kubeconfig, client.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes controller-runtime client: %w", err)
	}

	kubernetesCRClientCache[cacheKey] = client
	return client, nil
}

func newDrainer(ctx context.Context, clientSet *kubernetes.Clientset) *drain.Helper {
	return &drain.Helper{
		Client:              clientSet,
		Ctx:                 ctx,
		IgnoreAllDaemonSets: true, // ignore DaemonSet pods, because DaemonSets ignore unschedulable taints
		DeleteEmptyDirData:  true, // allow deleting emptyDir pods' data (yes)
		Force:               true, // evict pods with no controller
		GracePeriodSeconds:  -1,   // use pod's terminationGracePeriodSeconds
		Out:                 io.Discard,
		ErrOut:              io.Discard, // FIXME?
	}
}

// doNodeDrain cordons and drains the node, asynchronously.
func (r *NodeReconciler) doNodeDrain(
	ctx context.Context,
	remoteClient *kubernetes.Clientset,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
	nextPhase string, // the next phase to transition to after the drain is complete
) (*ctrl.Result, error) {
	log := logz.New(ctx, "node.drain")

	if node.Status.Operation != nil && node.Status.Operation.Options.Unsafe {
		// unsafe mode, skip drain
		err := r.updateOpStatus(
			ctx,
			nextPhase,
			NodeOperationReasonSkippedDrain,
			"Node drain skipped due to unsafe mode",
		)
		if err != nil {
			return nil, fmt.Errorf("failed to update node operation status: %w", err)
		}
		return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
	}

	// check if timeout expired
	drainTimeout, err := cluster.Spec.Options.GetDrainTimeout()
	if err != nil {
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionApplyDrain,
			"Invalid drain timeout duration, using default: %v",
			err,
		)
	}
	if node.Status.Operation != nil &&
		time.Since(node.Status.Operation.LastTransitionTime.Time) > drainTimeout {
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeWarning,
			EventReasonFailed,
			EventActionDrain,
			"Node drain timed out after %s, failing operation",
			drainTimeout.String(),
		)
		err := r.updateOpStatus(
			ctx,
			NodeOperationPhaseFailed,
			NodeOperationReasonFailedDrain,
			"Node drain timed out",
		)
		if err != nil {
			return nil, fmt.Errorf("failed to update node operation status: %w", err)
		}
		return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
	}

	// create the drain helper with a very conservative timeout, since we'll just requeue if it takes too long
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	drainHelper := newDrainer(timeoutCtx, remoteClient)

	// cordon the node
	k8sNode, err := remoteClient.CoreV1().Nodes().Get(ctx, node.GetShortName(), metav1.GetOptions{})
	if err != nil {
		log.Error().Err(err).Msg("failed to get node from kubernetes for cordon")
		return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
	}
	err = drain.RunCordonOrUncordon(drainHelper, k8sNode, true)
	if err != nil {
		log.Error().Err(err).Msg("failed to cordon node")
		return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
	}
	log.Debug().Msg("node cordoned successfully")

	// check for eviction support
	evictionSupport := true
	evictionGVK, err := drain.CheckEvictionSupport(remoteClient)
	if err != nil {
		evictionSupport = false
		log.Info().Err(err).Msg("eviction API not supported, falling back to delete")
	}

	// get all evictable pods for node
	podList, errors := drainHelper.GetPodsForDeletion(node.GetShortName())
	if len(errors) > 0 {
		for _, err := range errors {
			log.Error().Err(err).Msg("failed to get pods for deletion")
		}
		return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
	}
	pods := podList.Pods()
	log.Debug().Int("pods.count", len(pods)).Msg("found pods for deletion")

	// try to evict all pods for node, once, roughly the way drain does it internally
	returnCh := make(chan error, len(pods))
	wg := sync.WaitGroup{}
	for _, pod := range pods {
		wg.Add(1)
		go func(pod corev1.Pod) {
			defer wg.Done()
			var err error
			if evictionSupport {
				log.Trace().Str("pod", pod.Name).Msg("evicting pod")
				err = drainHelper.EvictPod(pod, evictionGVK)
			} else {
				log.Trace().Str("pod", pod.Name).Msg("deleting pod")
				err = drainHelper.DeletePod(pod)
			}
			if err != nil {
				log.Info().
					Err(err).
					Str("pod", pod.Name).
					Msg("expected failure evicting pod, will requeue")
			}
			returnCh <- err
		}(pod)
	}

	// close the channel when all goroutines are done
	go func() {
		wg.Wait()
		close(returnCh)
	}()

	// receive all errors from the goroutines
	ok := true
	for err := range returnCh {
		if err != nil {
			ok = false
		}
	}
	if !ok {
		log.Info().Msg("some pods failed to evict, requeuing")
		return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
	}

	// if we reach here, all pods have been evicted successfully, so we proceed with the next phase
	log.Debug().Str("nextPhase", nextPhase).Msg("node drain complete, proceeding to next phase")
	err = r.updateOpStatus(
		ctx,
		nextPhase,
		NodeOperationReasonSuccessfulDrain,
		"Node drain successful, proceeding to next phase",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update node operation status: %w", err)
	}
	return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
}

// hasNodeRebootedYet checks whether the given node has rebooted and become Ready in Kubernetes, given it was already Ready before.
func (r *NodeReconciler) hasNodeRebootedYet(
	tCtx context.Context,
	talosClient *tclient.Client,
	remoteClient *kubernetes.Clientset,
	node *talosv1alpha1.Node,
) bool {
	// compare boot ID
	currentBootID, err := getNodeBootID(tCtx, talosClient)
	if currentBootID == node.Status.BootID {
		return false
	}

	// check if k8s is ready after reboot
	if !meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionKubernetesReady) {
		// node was not ready before apply, skip ready check
		return true
	}

	k8sNode, err := remoteClient.CoreV1().
		Nodes().
		Get(tCtx, node.GetShortName(), metav1.GetOptions{})
	if err != nil {
		return false
	}
	kubeletReady := false
	for _, cond := range k8sNode.Status.Conditions {
		if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
			kubeletReady = true
		}
	}
	// check additionally for node.cilium.io/agent-not-ready taint
	for _, taint := range k8sNode.Spec.Taints {
		if taint.Key == "node.cilium.io/agent-not-ready" {
			return false
		}
	}
	if kubeletReady {
		return true
	}

	return false
}

func (r *NodeReconciler) doNodeUncordon(
	ctx context.Context,
	remoteClient *kubernetes.Clientset,
	node *talosv1alpha1.Node,
) error {
	drainer := newDrainer(ctx, remoteClient)

	k8sNode, err := remoteClient.CoreV1().Nodes().Get(ctx, node.GetShortName(), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node from kubernetes: %w", err)
	}
	err = drain.RunCordonOrUncordon(drainer, k8sNode, false)
	if err != nil {
		return fmt.Errorf("failed to uncordon node: %w", err)
	}

	return nil
}
