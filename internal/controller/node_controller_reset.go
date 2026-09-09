package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/samber/lo"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *NodeReconciler) handleNodeReset(
	ctx context.Context,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	if r.isOperationComplete(node) {
		return nil, nil
	}
	if node.Status.Operation.Type != NodeOperationTypeReset {
		return nil, nil
	}

	var opStatus = node.Status.Operation
	var err error

	// grab a cluster mutex to avoid concurrent updates
	otherOpsInProgress, err := r.areOtherOperationsInProgress(ctx, cluster, node)
	if err != nil {
		return nil, fmt.Errorf("error checking for other operations in progress: %w", err)
	}
	if otherOpsInProgress {
		return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}

	mutex := r.Mutexes.For(cluster.Namespace + ":" + cluster.Name)
	hasMutex := mutex.TryLock()
	if !hasMutex {
		// another operation is ongoing for this cluster
		return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}
	defer mutex.Unlock()

	tCtx, talosClient, err := getTalosClient(ctx, r.Client, node, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get Talos client: %w", err)
	}
	defer talosClient.Close()

	switch opStatus.Phase {
	case NodeOperationPhasePending:
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonStarted,
			EventActionReset,
			"Started reset of node",
		)

		// drain the node first
		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseDraining,
			OperationReasonStarted,
			"Started reset of node",
		)
		if err != nil {
			return nil, err
		}

	case NodeOperationPhaseDraining:
		remoteClient, err := getKubernetesClient(ctx, r.Client, cluster)
		if err != nil {
			return nil, err
		}

		res, err := r.doNodeDrain(ctx, remoteClient, node, cluster, NodeOperationPhaseApplying)
		if err != nil {
			return nil, fmt.Errorf("failed to drain: %w", err)
		}
		if res != nil {
			return res, nil
		}

	case NodeOperationPhaseApplying:
		resetReq := &machine.ResetRequest{}

		// if no mode is specified, try to infer from provided labels/disks
		if opStatus.Reset.Mode == "" {
			if len(opStatus.Reset.SystemLabelsToWipe) > 0 &&
				len(opStatus.Reset.UserDisksToWipe) == 0 {
				opStatus.Reset.Mode = NodeResetModeSystemDisk
			} else if len(opStatus.Reset.UserDisksToWipe) > 0 &&
				len(opStatus.Reset.SystemLabelsToWipe) == 0 {
				opStatus.Reset.Mode = NodeResetModeUserDisks
			} else {
				opStatus.Reset.Mode = NodeResetModeAll
			}
		}

		// translate reset options
		switch opStatus.Reset.Mode {
		case NodeResetModeAll:
			resetReq.Mode = machine.ResetRequest_ALL
		case NodeResetModeSystemDisk:
			resetReq.Mode = machine.ResetRequest_SYSTEM_DISK
		case NodeResetModeUserDisks:
			resetReq.Mode = machine.ResetRequest_USER_DISKS
		default:
			resetReq.Mode = machine.ResetRequest_ALL
		}
		if len(opStatus.Reset.SystemLabelsToWipe) > 0 {
			for _, label := range opStatus.Reset.SystemLabelsToWipe {
				resetReq.SystemPartitionsToWipe = append(
					resetReq.SystemPartitionsToWipe,
					&machine.ResetPartitionSpec{
						Label: label,
						Wipe:  true,
					},
				)
			}
		}
		if len(opStatus.Reset.UserDisksToWipe) > 0 {
			resetReq.UserDisksToWipe = opStatus.Reset.UserDisksToWipe
		}

		// determine graceful mode
		graceful := true
		if opStatus.Reset.Graceful != nil {
			graceful = *opStatus.Reset.Graceful
		} else if node.Spec.Role == Controlplane {
			// auto-detect: if we're the last controlplane, don't attempt graceful reset
			// since there's no etcd cluster to leave
			nodes := &talosv1alpha1.NodeList{}
			err := r.List(ctx, nodes, &client.ListOptions{
				FieldSelector: fields.SelectorFromSet(fields.Set{
					"spec.clusterRef": cluster.Name,
					"spec.role":       Controlplane,
				}),
			})
			if err != nil {
				return nil, fmt.Errorf("failed to list controlplane nodes: %w", err)
			}

			// count other healthy controlplane nodes
			healthyCount := 0
			for _, otherNode := range nodes.Items {
				if otherNode.Name == node.Name {
					continue
				}
				// a node is considered healthy if it's not being deleted, has no ongoing operation, and is ready
				if otherNode.DeletionTimestamp == nil && r.isOperationComplete(&otherNode) &&
					meta.IsStatusConditionTrue(otherNode.Status.Conditions, NodeConditionReady) {
					healthyCount++
				}
			}

			if healthyCount == 0 {
				graceful = false
			}
		}
		resetReq.Graceful = graceful
		resetReq.Reboot = opStatus.Reset.Reboot
		err = talosClient.ResetGeneric(tCtx, resetReq)
		if err != nil {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedReset,
				fmt.Sprintf("Failed to reset node: %v", err),
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseVerifying,
			OperationReasonStarted,
			"Node reset successfully",
		)
		if err != nil {
			return nil, err
		}

	case NodeOperationPhaseVerifying:
		// verify that the talosClient call fails
		tCtx, talosClient, err := getTalosClient(ctx, r.Client, node, cluster)
		if err != nil {
			return nil, fmt.Errorf("failed to get Talos client for verification: %w", err)
		}
		defer talosClient.Close()
		ttCtx, cancel := context.WithTimeout(tCtx, 2*time.Second)
		defer cancel()

		_, err = talosClient.MachineClient.Version(ttCtx, nil)
		if err == nil {
			return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
		}

		// set node conditions to not ready / not provisioned, assuming the state partition was wiped
		if opStatus.Reset.Mode != NodeResetModeUserDisks &&
			(len(opStatus.Reset.SystemLabelsToWipe) == 0 || slices.Contains(opStatus.Reset.SystemLabelsToWipe, "STATE")) {

			// remove finalizer to allow deletion
			newFinalizers := lo.Filter(node.Finalizers, func(item string, index int) bool {
				return item != NodeFinalizer
			})
			err = r.updateNode(ctx, func(n *talosv1alpha1.Node) {
				n.Finalizers = newFinalizers
			})
			if err != nil {
				return nil, fmt.Errorf("failed to remove finalizer after reset: %w", err)
			}

			// set conditions
			err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				n.Status.Conditions = []metav1.Condition{}
				meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
					Type:               NodeConditionReady,
					Status:             metav1.ConditionFalse,
					Reason:             NodeConditionReasonReset,
					Message:            "Node has been reset and is no longer ready",
					LastTransitionTime: metav1.Now(),
				})
				meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
					Type:               NodeConditionProvisioned,
					Status:             metav1.ConditionFalse,
					Reason:             NodeConditionReasonReset,
					Message:            "Node has been reset and is no longer provisioned",
					LastTransitionTime: metav1.Now(),
				})
			})
			if err != nil {
				return nil, fmt.Errorf("failed to update node conditions after reset: %w", err)
			}
			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeNormal,
				EventReasonCompleted,
				EventActionReset,
				"Detected system partition wipe, marking node as not provisioned and not ready",
			)

			// try to delete node from Kubernetes cluster
			err = nil
			remoteClient, err := getKubernetesClient(ctx, r.Client, cluster)
			if err == nil {
				err = remoteClient.CoreV1().
					Nodes().
					Delete(ctx, node.GetShortName(), metav1.DeleteOptions{})
			}
			if err != nil {
				r.Recorder.Eventf(
					node,
					nil,
					corev1.EventTypeWarning,
					EventReasonFailed,
					EventActionReset,
					"Failed to delete node from Kubernetes cluster after reset: %v",
					err,
				)
			} else {
				r.Recorder.Eventf(
					node,
					nil,
					corev1.EventTypeNormal,
					EventReasonDeleted,
					EventActionReset,
					"Deleted node from Kubernetes cluster after reset",
				)
			}
		}

		// TODO: add verification step for system partition wipe to check if the node ends up
		// - reboot=false, actually down
		// - reboot=true, back in maintenance mode

		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseDone,
			OperationReasonCompleted,
			"Node reset operation completed successfully",
		)
		if err != nil {
			return nil, err
		}
	}

	return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
}
