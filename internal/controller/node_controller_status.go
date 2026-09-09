package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/samber/lo"

	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	configresource "github.com/siderolabs/talos/pkg/machinery/resources/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
)

// tcpPing checks if a TCP connection can be established to the given address.
// It retries the connection attempt up to 2 times with a small delay between attempts.
func tcpPing(ctx context.Context, address string) bool {
	const maxRetries = 2
	const retryDelay = 200 * time.Millisecond

	d := net.Dialer{}
	d.Timeout = 1 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		conn, err := d.DialContext(ctx, "tcp", address)
		if err == nil {
			conn.Close()
			return true
		}

		// don't delay after the last attempt
		if attempt < maxRetries {
			// check if context is already cancelled before delaying
			select {
			case <-ctx.Done():
				return false
			case <-time.After(retryDelay):
				// continue to next attempt
			}
		}
	}
	return false
}

// reconcileNodeStatus updates the Node's status attributes.
func (r *NodeReconciler) reconcileNodeStatus(
	ctx context.Context,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	// if there's an ongoing operation, we cannot determine if the config is in sync
	// (but still allow status updates in Pending)
	if !r.isOperationComplete(node) && node.Status.Operation != nil &&
		node.Status.Operation.Phase != NodeOperationPhasePending {
		return nil, nil
	}

	logger := log.FromContext(ctx).WithValues("node", node.Name)

	// determine if node is reachable
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if ok := tcpPing(timeoutCtx, node.GetConnectHost(cluster)+":50000"); !ok {
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionReady,
				Status:  metav1.ConditionFalse,
				Reason:  NodeConditionReasonNodeUnreachable,
				Message: "Node is unreachable",
			})
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to update node status condition for unreachable node: %w",
				err,
			)
		}
		return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
	}

	// init shared talos client for node
	tCtx, talosClient, err := getTalosClient(ctx, r.Client, node, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get Talos client: %w", err)
	}
	defer talosClient.Close()

	// fetch the node's Boot ID
	bootID, err := getNodeBootID(tCtx, talosClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get node boot ID: %w", err)
	}
	if node.Status.BootID != bootID {
		err = r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.BootID = bootID
		})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update node BootID status: %w", err)
	}

	// fetch the node's Talos version and schematic ID
	talosVersion, talosSchematicID, err := getNodeTalosRelease(tCtx, talosClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get node Talos release info: %w", err)
	}
	if node.Status.TalosVersion != "v"+talosVersion.String() ||
		node.Status.TalosSchematicID != talosSchematicID {
		err = r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.TalosVersion = "v" + talosVersion.String()
			n.Status.TalosSchematicID = talosSchematicID
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update node misc status: %w", err)
		}
	}

	// we have now successfully connected to the node and interacted with it, so detect if Ready is false and set to true
	if !meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionReady) {
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionReady,
				Status:  metav1.ConditionTrue,
				Reason:  NodeConditionReasonNodeReachable,
				Message: "Node is reachable",
			})
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to update node status condition for reachable node: %w",
				err,
			)
		}
	}

	// generate a expectedConfig, the one we would expect the apply operation to apply
	opts := &GenerateMachineConfigOptions{
		WithoutRTLLabel:                !r.FeatureFlags.EnableRTLNodeLabel,
		WithoutCrossNamespacePatchRefs: !r.FeatureFlags.EnableCrossNamespacePatchRefs,
	}
	expectedConfig, _, err := GenerateMachineConfig(ctx, r.Client, node, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate expected machine config: %w", err)
	}
	expectedConfigBytes, err := EncodeMachineConfig(expectedConfig, false)
	if err != nil {
		return nil, fmt.Errorf("failed to encode expected machine config: %w", err)
	}

	// determine ConfigInSync status:
	// fetch the running machine config
	// https://github.com/siderolabs/talos/blob/80ab7a0643fc8057283a8ba3eb912d0ee453c143/cmd/talosctl/cmd/talos/get.go#L95C93-L95C105
	rd, err := talosClient.ResolveResourceKind(tCtx, lo.ToPtr(""), "machineconfig")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve machineconfig RD: %w", err)
	}

	runningConfig, err := safe.StateGet[*configresource.MachineConfig](
		tCtx,
		talosClient.COSI,
		resource.NewMetadata(
			rd.TypedSpec().DefaultNamespace,
			rd.TypedSpec().Type,
			"v1alpha1",
			resource.VersionUndefined,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get running machine config: %w", err)
	}
	runningConfigBytes, err := EncodeMachineConfig(runningConfig.Provider(), false)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize running machine config: %w", err)
	}

	// test that the deployed config matches our generated config
	var runningConfigExactlyEqual bool = slices.Equal(expectedConfigBytes, runningConfigBytes)

	// test that the running OS image matches the OS image defined in the config.
	//
	// OVAs and maintenance mode disk images don't install on provision, so it's possible
	// for the config to say one thing but the machine to be running another.
	var runningImageExactlyEqual bool = true
	configuredTalosVersion, configuredSchematicID, err := parseImageTalosRelease(
		expectedConfig.Machine().Install().Image(),
	)
	if err != nil {
		updateErr := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionReady,
				Status:  metav1.ConditionFalse,
				Reason:  NodeConditionReasonFailedImageValidation,
				Message: "Node install image is invalid or missing: " + err.Error(),
			})
		})
		if updateErr != nil {
			return nil, fmt.Errorf(
				"failed to update node status with invalid install image: %w",
				updateErr,
			)
		}
		return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
	}
	if configuredSchematicID != "" && configuredSchematicID != talosSchematicID {
		logger.V(1).
			Info("Node is out of sync because of schematic ID mismatch", "configuredSchematicID", configuredSchematicID, "runningSchematicID", talosSchematicID)
		runningImageExactlyEqual = false
	}
	if !configuredTalosVersion.Equals(talosVersion) {
		logger.V(1).
			Info("Node is out of sync because of Talos version mismatch", "configuredTalosVersion", configuredTalosVersion, "runningTalosVersion", talosVersion)
		runningImageExactlyEqual = false
	}

	// compare configs
	var configInSync bool = runningConfigExactlyEqual && runningImageExactlyEqual
	var updatedConfigCondition bool = false
	if !configInSync {
		// detect config drift
		lastAppliedConfigMatchesGenerated := node.Status.AppliedConfigHash != "" &&
			node.Status.AppliedConfigHash == sha256Sum(expectedConfigBytes)
		// if we skipped upgrade because we had to during provision:
		runningImageDriftIsOurFault := !runningImageExactlyEqual && meta.IsStatusConditionTrue(
			node.Status.Conditions,
			NodeConditionNeedsInitialUpdate,
		)
		if lastAppliedConfigMatchesGenerated && !runningImageDriftIsOurFault {
			meta.SetStatusCondition(&node.Status.Conditions, metav1.Condition{
				Type:    NodeConditionConfigDriftDetected,
				Status:  metav1.ConditionTrue,
				Reason:  NodeConditionReasonConfigHasDrifted,
				Message: "Node configuration has drifted since last operator-initiated apply. Refusing to initiate automatic apply.",
			})
			updatedConfigCondition = true
		}

		meta.SetStatusCondition(&node.Status.Conditions, metav1.Condition{
			Type:    NodeConditionConfigInSync,
			Status:  metav1.ConditionFalse,
			Reason:  NodeConditionReasonConfigOutOfSync,
			Message: "Node configuration is out of sync",
		})
		updatedConfigCondition = true
	} else {
		meta.SetStatusCondition(&node.Status.Conditions, metav1.Condition{
			Type:    NodeConditionConfigInSync,
			Status:  metav1.ConditionTrue,
			Reason:  NodeConditionReasonConfigInSync,
			Message: "Node configuration is in sync",
		})
		updatedConfigCondition = true
	}
	if updatedConfigCondition {
		// take a copy to prevent refreshes overwriting our conditions
		conditions := make([]metav1.Condition, len(node.Status.Conditions))
		copy(conditions, node.Status.Conditions)
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.Conditions = conditions
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to update node status condition for config in sync: %w",
				err,
			)
		}
	}

	// determine KubernetesNeedsUpgrade status:
	// check if kubernetes versions match Cluster.Spec.KubernetesVersion
	targetKubernetesVersion := cluster.Spec.KubernetesVersion
	if node.Status.KubernetesVersions == nil {
		return nil, errors.New("node KubernetesVersions status is nil")
	}
	var kubernetesNeedsUpgrade bool = false
	if node.Spec.Role == Controlplane {
		if node.Status.KubernetesVersions.APIServer != targetKubernetesVersion ||
			node.Status.KubernetesVersions.ControllerManager != targetKubernetesVersion ||
			node.Status.KubernetesVersions.Scheduler != targetKubernetesVersion {
			kubernetesNeedsUpgrade = true
		}
	}
	if node.Status.KubernetesVersions.Kubelet != targetKubernetesVersion {
		kubernetesNeedsUpgrade = true
	}

	if kubernetesNeedsUpgrade &&
		!meta.IsStatusConditionFalse(node.Status.Conditions, NodeConditionKubernetesInSync) {
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionKubernetesInSync,
				Status:  metav1.ConditionFalse,
				Reason:  NodeConditionReasonKubernetesUpgradeNeeded,
				Message: "Kubernetes components need to be upgraded",
			})
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to update node status condition for Kubernetes needs updates: %w",
				err,
			)
		}
	} else if !kubernetesNeedsUpgrade &&
		!meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionKubernetesInSync) {
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionKubernetesInSync,
				Status:  metav1.ConditionTrue,
				Reason:  ClusterConditionNodesKubernetesUpToDate,
				Message: "Kubernetes components are up to date",
			})
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to update node status condition for Kubernetes up to date: %w",
				err,
			)
		}
	}

	res, err := r.reconcileNodeKubernetesStatus(ctx, node, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile Kubernetes status: %w", err)
	}

	return res, nil
}

func (r *NodeReconciler) reconcileNodeKubernetesStatus(
	ctx context.Context,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	// connect to Kubernetes API and fetch node readiness in Kubernetes

	// copy conditions so it isn't overwritten on conflict retry
	var conditionsUpdated bool = false
	var conditions = make([]metav1.Condition, len(node.Status.Conditions))
	copy(conditions, node.Status.Conditions)

	kubernetesClient, err := getKubernetesCRClient(ctx, r.Client, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes client: %w", err)
	}
	k8sNode := &corev1.Node{}
	k8sNode.Name = node.GetShortName()
	err = kubernetesClient.Get(ctx, client.ObjectKeyFromObject(k8sNode), k8sNode)
	if err != nil {
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionKubernetesReady,
				Status:  metav1.ConditionFalse,
				Reason:  NodeConditionReasonKubernetesNodeNotFound,
				Message: "Kubernetes node not found (is cluster bootstrapped?): " + err.Error(),
			})
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to update node status condition for Kubernetes: %w",
				err,
			)
		}
		return nil, nil
	}

	// kubernetes reachable, grab various node statuses
	var nodeReady bool
	var nodeUnschedulable bool = k8sNode.Spec.Unschedulable
	for _, condition := range k8sNode.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			nodeReady = true
			break
		}
	}
	if nodeReady &&
		!meta.IsStatusConditionTrue(conditions, NodeConditionKubernetesReady) {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:    NodeConditionKubernetesReady,
			Status:  metav1.ConditionTrue,
			Reason:  NodeConditionReasonKubernetesNodeReady,
			Message: "Kubernetes node is ready",
		})
		conditionsUpdated = true
	} else if !nodeReady &&
		!meta.IsStatusConditionFalse(conditions, NodeConditionKubernetesReady) {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:    NodeConditionKubernetesReady,
			Status:  metav1.ConditionFalse,
			Reason:  NodeConditionReasonKubernetesNodeNotReady,
			Message: "Kubernetes node is not ready",
		})
		conditionsUpdated = true
	}
	if nodeUnschedulable &&
		!meta.IsStatusConditionTrue(conditions, NodeConditionKubernetesUnschedulable) {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:    NodeConditionKubernetesUnschedulable,
			Status:  metav1.ConditionTrue,
			Reason:  NodeConditionReasonKubernetesNodeUnschedulable,
			Message: "Kubernetes node is unschedulable",
		})
		conditionsUpdated = true
	} else if !nodeUnschedulable &&
		!meta.IsStatusConditionFalse(conditions, NodeConditionKubernetesUnschedulable) {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:    NodeConditionKubernetesUnschedulable,
			Status:  metav1.ConditionFalse,
			Reason:  NodeConditionReasonKubernetesNodeSchedulable,
			Message: "Kubernetes node is schedulable",
		})
		conditionsUpdated = true
	}
	if conditionsUpdated {
		err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.Conditions = conditions
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to update node status condition for Kubernetes node status: %w",
				err,
			)
		}
	}

	// update the node's in-k8s maintenance window annotation
	err = r.nodeBumpMaintenanceWindowAnnotation(ctx, cluster, kubernetesClient, k8sNode)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to bump maintenance window annotation: %w",
			err,
		)
	}

	return nil, nil
}

// nodeBumpMaintenanceWindowAnnotation updates the Node's maintenance window annotation
// in Kubernetes.
func (r *NodeReconciler) nodeBumpMaintenanceWindowAnnotation(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
	k8sClient client.Client,
	k8sNode *corev1.Node,
) error {
	nextMaintenanceWindow := cluster.Status.NextMaintenanceWindow
	if k8sNode.Annotations == nil {
		k8sNode.Annotations = make(map[string]string)
	}
	nodeAnnotationValue, exists := k8sNode.Annotations[NodeK8sAnnotationNextMaintenanceWindow]

	shouldHaveAnnotation := !nextMaintenanceWindow.IsZero() &&
		cluster.Spec.Options.MaintenanceWindow.IsAnnotationsEnabled()

	if !shouldHaveAnnotation && exists {
		// remove annotation from k8s node if there is no upcoming maintenance window
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(k8sNode), k8sNode)
			if err != nil {
				return fmt.Errorf("failed to get Kubernetes node for annotation update: %w", err)
			}
			delete(k8sNode.Annotations, NodeK8sAnnotationNextMaintenanceWindow)
			return k8sClient.Update(ctx, k8sNode)
		})
		if err != nil {
			return fmt.Errorf(
				"failed to remove maintenance window annotation: %w",
				err,
			)
		}
	}

	// if there is a next maintenance window...
	if shouldHaveAnnotation {
		mw := cluster.Spec.Options.MaintenanceWindow
		tz, err := time.LoadLocation(mw.Timezone)
		if err != nil {
			r.Recorder.Eventf(
				cluster,
				nil,
				corev1.EventTypeWarning,
				EventReasonValidationError,
				EventActionStatusReconcile,
				"Invalid maintenance window timezone: %v",
				err,
			)
			return nil
		}
		localizedWindow := nextMaintenanceWindow.In(tz)
		annotationValue := localizedWindow.Format(time.RFC3339)

		// and if the annotation value doesn't match the cluster's next maintenance window, update it
		if !exists || nodeAnnotationValue != annotationValue {

			// update annotation on k8s node if it doesn't match the cluster's next maintenance window
			if k8sNode.Annotations == nil {
				k8sNode.Annotations = make(map[string]string)
			}
			k8sNode.Annotations[NodeK8sAnnotationNextMaintenanceWindow] = annotationValue
			err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(k8sNode), k8sNode)
				if err != nil {
					return fmt.Errorf(
						"failed to get Kubernetes node for annotation update: %w",
						err,
					)
				}
				if k8sNode.Annotations == nil {
					k8sNode.Annotations = make(map[string]string)
				}
				k8sNode.Annotations[NodeK8sAnnotationNextMaintenanceWindow] = annotationValue
				return k8sClient.Update(ctx, k8sNode)
			})
			if err != nil {
				return fmt.Errorf(
					"failed to update maintenance window annotation: %w",
					err,
				)
			}
		}
	}

	return nil
}
