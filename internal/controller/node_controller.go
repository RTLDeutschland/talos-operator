package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	iapi "github.com/RTLDeutschland/talos-operator/internal/api"
	. "github.com/RTLDeutschland/talos-operator/internal/constants"
	"github.com/RTLDeutschland/talos-operator/internal/keyedmutex"
	"github.com/RTLDeutschland/talos-operator/internal/logz"
)

const (
	// Requeue durations for async operations
	RequeueImmediate     = 1 * time.Second  // Immediate requeue for next step in a multi-step operation
	RequeueShortDelay    = 5 * time.Second  // Fast polling for quick operations
	RequeueStandardDelay = 10 * time.Second // Standard polling for most operations
)

// NodeReconciler reconciles a Node object by managing its lifecycle and operations.
// It handles provisioning, bootstrapping, configuration application, and various
// node operations such as reboot, shutdown, reset, and Kubernetes component upgrades.
type NodeReconciler struct {
	client.Client
	APIReader    client.Reader
	Scheme       *runtime.Scheme
	Recorder     events.EventRecorderLogger
	Mutexes      *keyedmutex.MutexMap
	FeatureFlags iapi.FeatureFlags
}

// +kubebuilder:rbac:groups=talos.rtl.de,resources=nodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=talos.rtl.de,resources=nodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=talos.rtl.de,resources=nodes/finalizers,verbs=update
// +kubebuilder:rbac:groups=talos.rtl.de,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps;secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="events.k8s.io",resources=events,verbs=create;patch

// Reconcile compares the Node state defined in the cluster to the real state
// of the Talos node and makes the necessary adjustments, if authorized by
// the user using `operation`.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = context.WithValue(ctx, CtxKeyRequest, &req) // attach request to context for logging
	log := logz.New(ctx, "node.root")
	log.Info().Msg("Reconciling Node")

	// fetch Node
	node := &talosv1alpha1.Node{}
	err := r.APIReader.Get(ctx, req.NamespacedName, node)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if node.Name == "" {
		return ctrl.Result{}, nil
	}

	// attach node to context
	ctx = context.WithValue(ctx, CtxKeyNode, node)

	// fetch Cluster and update Node status if cluster ref doesn't exist.
	// we also have a validating webhook to check this, but don't assume it exists.
	cluster := &talosv1alpha1.Cluster{}
	err = r.APIReader.Get(ctx, client.ObjectKey{
		Name:      node.Spec.ClusterRef,
		Namespace: req.Namespace,
	}, cluster)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf(
			"failed to get referenced Cluster %s: %w",
			node.Spec.ClusterRef,
			err,
		)
	}
	if apierrors.IsNotFound(err) || cluster.Name == "" {
		// set Ready false on Node because Cluster not found
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  NodeConditionReasonClusterNotFound,
				Message: "Referenced Cluster not found",
			})
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// attach cluster to context
	ctx = context.WithValue(ctx, CtxKeyCluster, cluster)

	// re-init logger with new info
	log = logz.New(ctx, "node.root")

	// migration logic: remove deprecated conditions
	if meta.FindStatusCondition(node.Status.Conditions, "KubernetesNeedsUpdates") != nil {
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.RemoveStatusCondition(&n.Status.Conditions, "KubernetesNeedsUpdates")
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error removing deprecated status conditions: %w", err)
		}
	}

	// ensure KubernetesVersions is initialized
	var kubernetesVersionsChanged bool
	kubernetesVersions := node.Status.KubernetesVersions
	if node.Status.KubernetesVersions == nil {
		kubernetesVersions = &talosv1alpha1.KubernetesVersions{}
	}
	defaultVersions := makeDefaultKubernetesVersions(node, cluster)
	if kubernetesVersions.Kubelet == "" {
		log.Trace().Msg("Setting default kubelet version from Cluster")
		kubernetesVersions.Kubelet = defaultVersions.Kubelet
		kubernetesVersionsChanged = true
	}
	if node.Spec.Role == Controlplane {
		if kubernetesVersions.APIServer == "" {
			log.Trace().Msg("Setting default API server version from Cluster")
			kubernetesVersions.APIServer = defaultVersions.APIServer
			kubernetesVersionsChanged = true
		}
		if kubernetesVersions.ControllerManager == "" {
			log.Trace().Msg("Setting default controller manager version from Cluster")
			kubernetesVersions.ControllerManager = defaultVersions.ControllerManager
			kubernetesVersionsChanged = true
		}
		if kubernetesVersions.Scheduler == "" {
			log.Trace().Msg("Setting default scheduler version from Cluster")
			kubernetesVersions.Scheduler = defaultVersions.Scheduler
			kubernetesVersionsChanged = true
		}
	} else {
		// zero out control plane versions for worker nodes
		if kubernetesVersions.APIServer != "" {
			log.Trace().Msg("Clearing API server version for worker node")
			kubernetesVersions.APIServer = ""
			kubernetesVersionsChanged = true
		}
		if kubernetesVersions.ControllerManager != "" {
			log.Trace().Msg("Clearing controller manager version for worker node")
			kubernetesVersions.ControllerManager = ""
			kubernetesVersionsChanged = true
		}
		if kubernetesVersions.Scheduler != "" {
			log.Trace().Msg("Clearing scheduler version for worker node")
			kubernetesVersions.Scheduler = ""
			kubernetesVersionsChanged = true
		}
	}
	if kubernetesVersionsChanged {
		log.Debug().
			Interface("kubernetes_versions", kubernetesVersions).
			Msg("Updating kubernetesVersions status")
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.KubernetesVersions = kubernetesVersions
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"failed to update Node KubernetesVersions status: %w",
				err,
			)
		}
	}

	// update Node EffectiveConfig status
	opts := &GenerateMachineConfigOptions{
		WithoutRTLLabel:                !r.FeatureFlags.EnableRTLNodeLabel,
		WithoutCrossNamespacePatchRefs: !r.FeatureFlags.EnableCrossNamespacePatchRefs,
		WithoutSecrets:                 true,
	}
	cfg, patchHierarchy, err := GenerateMachineConfig(
		ctx,
		r.Client,
		node,
		opts,
	)
	if err != nil {
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionStatusReconcile,
			"Failed to generate machine config: %v",
			err,
		)
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionReady,
				Status:  metav1.ConditionFalse,
				Reason:  NodeReasonConfigGenerationFailed,
				Message: "Failed to generate machine config: " + err.Error(),
			})
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"failed to update Node status after config generation failure: %w",
				err,
			)
		}
		return ctrl.Result{}, nil
	}
	encodedCfg, err := EncodeMachineConfig(cfg, true)
	if err != nil {
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionStatusReconcile,
			"Failed to encode machine config: %v",
			err,
		)
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionReady,
				Status:  metav1.ConditionFalse,
				Reason:  NodeReasonConfigSerializationFailed,
				Message: "Failed to encode machine config: " + err.Error(),
			})
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"failed to update Node status after config encoding failure: %w",
				err,
			)
		}
		return ctrl.Result{}, nil
	}
	encodedCfgStr := string(encodedCfg)
	if node.Status.EffectiveConfig != encodedCfgStr {
		log.Debug().Msg("Updating effectiveConfig status")
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.EffectiveConfig = encodedCfgStr
			n.Status.PatchHierarchy = patchHierarchy
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"failed to update Node EffectiveConfig status: %w",
				err,
			)
		}
	}

	// NEW logic for operations:
	// copy node.Operation to node.Status.Operation immediately, and then immediately clear node.Operation.
	// this allows us to interrupt running operations of the same type by setting node.Operation again,
	// instead of having to worry about node.Status.Operation equalling node.Operation and ignoring it accidentally.
	if node.Operation != nil {
		log.Debug().Str("operation.type", node.Operation.Type).Msg("Processing new Operation...")
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.Operation = &talosv1alpha1.NodeOperationStatus{
				NodeOperation:      *node.Operation,
				Phase:              NodeOperationPhasePending,
				Reason:             OperationReasonInitialized,
				Message:            "Node operation initialized",
				LastTransitionTime: metav1.Now(),
			}
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update Node Operation status: %w", err)
		}
		log.Trace().Msg("Copied to node.Status.Operation")

		node.Operation = nil
		err = r.Patch(
			ctx,
			node,
			client.RawPatch("application/merge-patch+json", []byte(`{"operation":null}`)),
		)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to clear Node Operation: %w", err)
		}
		log.Trace().Msg("Cleared node.Operation, requeueing immediately")

		// requeue immediately to update Node object
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	// WORK BEGINS HERE

	// provision the node from maintenance mode if needed
	res, err := r.handleNodeProvisioning(ctx, node, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if res != nil {
		log.Trace().
			Dur("requeue_after", res.RequeueAfter).
			Msg("Node provisioning operation requeued")
		return *res, nil
	}

	// if node isn't provisioned, set Ready false and exit early
	// *unless* there's a reset in progress
	resetInProgress := !r.isOperationComplete(node) &&
		node.Status.Operation.Type == NodeOperationTypeReset
	if !meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionProvisioned) &&
		!resetInProgress {
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionProvisioned,
				Status:  metav1.ConditionFalse,
				Reason:  NodeConditionReasonNodeNotProvisioned,
				Message: "Node is not yet provisioned",
			})
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionReady,
				Status:  metav1.ConditionFalse,
				Reason:  NodeConditionReasonNodeNotProvisioned,
				Message: "Node is not yet provisioned",
			})
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		log.Info().Msg("Node is not provisioned, not requeueing")
		return ctrl.Result{}, nil
	}

	// check if the Node was deleted but we need to perform cleanup
	if r.isOperationComplete(node) && node.DeletionTimestamp != nil &&
		slices.Contains(node.Finalizers, NodeFinalizer) {
		log.Info().Msg("Node deletion detected, starting reset operation and cleanup")
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.Operation = &talosv1alpha1.NodeOperationStatus{
				NodeOperation: talosv1alpha1.NodeOperation{
					Type: NodeOperationTypeReset,
					// let the reset handler auto-detect graceful mode
				},
				Phase:              NodeOperationPhasePending,
				Reason:             NodeOperationReasonNodeDeletionCleanup,
				Message:            "Node deletion detected, performing cleanup operation",
				LastTransitionTime: metav1.Now(),
			}
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"failed to update Node Operation status for deletion cleanup: %w",
				err,
			)
		}
	}

	// run bootstrap if requested
	res, err = r.handleNodeBootstrap(ctx, node, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if res != nil {
		log.Trace().Dur("requeue_after", res.RequeueAfter).Msg("Node bootstrap operation requeued")
		return *res, nil
	}

	// figure out if node config is in sync with expected config
	res, err = r.reconcileNodeStatus(ctx, node, cluster)
	if err != nil {
		log.Err(err).Msg("Could not reconcile node status")
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionReady,
				Status:  metav1.ConditionFalse,
				Reason:  NodeConditionReasonNodeStatusFailed,
				Message: "Failed to check node status: " + err.Error(),
			})
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
	}
	if res != nil {
		log.Trace().Dur("requeue_after", res.RequeueAfter).Msg("Node status check requeued")
		return *res, nil
	}

	// check if node needs initial updates and start an Apply for them
	if false {
		log.Info().Msg("Node needs initial OS update, starting Apply operation")
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.Operation = &talosv1alpha1.NodeOperationStatus{
				NodeOperation: talosv1alpha1.NodeOperation{
					Type: NodeOperationTypeApply,
				},
				Phase:              NodeOperationPhasePending,
				Reason:             NodeOperationReasonUpgradeRequired,
				Message:            "Node needs initial OS update, starting Apply operation",
				LastTransitionTime: metav1.Now(),
			}
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"failed to update Node Operation status for initial OS update: %w",
				err,
			)
		}
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	if r.isOperationComplete(node) {
		// operation is done, exit early
		log.Trace().Msg("No ongoing operation, requeueing at default polling interval")
		return ctrl.Result{RequeueAfter: defaultRefreshInterval()}, nil
	}
	switch node.Status.Operation.Type {
	case NodeOperationTypeApply:
		res, err = r.handleNodeApply(ctx, node, cluster)
	case NodeOperationTypeUpgrade:
		res, err = r.handleNodeUpgrade(ctx, node, cluster)
	case NodeOperationTypeKubernetesComponentUpgrade:
		res, err = r.handleKubernetesComponentUpgrade(ctx, node, cluster)
	case NodeOperationTypeReboot, NodeOperationTypeShutdown:
		res, err = r.handleNodePower(ctx, node, cluster)
	case NodeOperationTypeReset:
		res, err = r.handleNodeReset(ctx, node, cluster)
	}
	log = log.With().
		Str("operation.type", node.Status.Operation.Type).
		Str("operation.phase", node.Status.Operation.Phase).
		Logger()
	if err != nil {
		log.Err(err).Msg("Error during node operation, requeueing")
		return ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}
	if res != nil {
		log.Trace().
			Dur("requeue_after", res.RequeueAfter).
			Msgf("%s operation requeued", node.Status.Operation.Type)
		return *res, nil
	}

	log.Info().Msg("Node reconciliation successful after operation, requeuing at polling interval")
	return ctrl.Result{RequeueAfter: defaultRefreshInterval()}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&talosv1alpha1.Node{}).
		// set a watch on Clusters to reconcile Nodes when Cluster changes (for example, KubernetesVersion)
		Watches(&talosv1alpha1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				nodeList := &talosv1alpha1.NodeList{}
				err := r.List(
					ctx,
					nodeList,
					&client.ListOptions{
						FieldSelector: fields.SelectorFromSet(
							fields.Set{"spec.clusterRef": obj.GetName()},
						),
					},
				)
				if err != nil {
					return []reconcile.Request{}
				}
				requests := make([]reconcile.Request, 0, len(nodeList.Items))
				for _, node := range nodeList.Items {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      node.Name,
							Namespace: node.Namespace,
						},
					})
				}
				return requests
			}),
		).
		Watches(&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				// when a ConfigMap in the same namespace changes, reconcile all Nodes in the same namespace
				nodeList := &talosv1alpha1.NodeList{}
				err := r.List(ctx, nodeList, &client.ListOptions{Namespace: obj.GetNamespace()})
				if err != nil {
					return []reconcile.Request{}
				}
				requests := make([]reconcile.Request, 0, len(nodeList.Items))
				for _, node := range nodeList.Items {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      node.Name,
							Namespace: node.Namespace,
						},
					})
				}
				return requests
			}),
		).

		// ARCHITECTURAL DECISION:
		// *not* setting a watch for secrets, because if those are updated the entire cluster is
		// 	probably about to have a bad day.
		// give people the opportunity to fix their mistakes.
		// alternatively, they can bump whatever object they want to trigger a reconcile if they
		// 	insist on shooting themselves in the foot.
		Named("node").
		Complete(r)
}
