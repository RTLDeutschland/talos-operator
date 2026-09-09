package controller

import (
	"context"
	"fmt"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *ClusterReconciler) handleBootstrapOperation(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	opStatus := cluster.Status.Operation

	switch opStatus.Phase {
	case ClusterOperationPhasePending:
		nodes, err := r.listEligibleNodes(ctx, cluster, Controlplane)
		if err != nil {
			return nil, err
		}

		// require at least 1 provisioned controlplane node to bootstrap
		if len(nodes) == 0 {
			err := r.updateOpStatus(
				ctx,
				ClusterOperationPhaseFailed,
				ClusterOperationReasonNoControlPlanes,
				"No provisioned controlplane nodes found",
				nil,
			)
			if err != nil {
				return nil, fmt.Errorf("error updating cluster operation status: %w", err)
			}
			return nil, nil
		}

		// check if any other node operations are in progress
		inProgressName := r.otherOpsInProgress(nodes)
		if inProgressName != "" {
			err = r.updateOpStatus(
				ctx,
				ClusterOperationPhaseFailed,
				OperationReasonConflict,
				fmt.Sprintf(
					"Another node operation is in progress on node `%s`, refusing to start cluster operation",
					inProgressName,
				),
				nil,
			)
			if err != nil {
				return nil, fmt.Errorf("error updating cluster operation status: %w", err)
			}
			return nil, nil
		}

		// ensure all nodes are ready before bootstrapping
		for _, node := range nodes {
			if !meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionReady) {
				err := r.updateOpStatus(
					ctx,
					ClusterOperationPhaseFailed,
					ClusterOperationReasonNodeNotReady,
					fmt.Sprintf(
						"Node `%s` is not Ready, cannot proceed with bootstrap",
						node.Name,
					),
					nil,
				)
				if err != nil {
					return nil, fmt.Errorf("error updating cluster operation status: %w", err)
				}
				return nil, nil
			}
		}

		// ensure all nodes have config in sync before bootstrapping
		for _, node := range nodes {
			if meta.IsStatusConditionFalse(node.Status.Conditions, NodeConditionConfigInSync) &&
				// with edge case for needs initial update:
				// a node might actually be running the requested config, but was not able to
				// OS upgrade during provisioning because the cluster was not bootstrapped yet.
				!meta.IsStatusConditionTrue(
					node.Status.Conditions,
					NodeConditionNeedsInitialUpdate,
				) {
				err := r.updateOpStatus(
					ctx,
					ClusterOperationPhaseFailed,
					ClusterOperationReasonNodeConfigOutOfSync,
					fmt.Sprintf(
						"Node `%s` has configuration out of sync, cannot proceed with bootstrap",
						node.Name,
					),
					nil,
				)
				if err != nil {
					return nil, fmt.Errorf("error updating cluster operation status: %w", err)
				}
				return nil, nil
			}
		}

		// create bootstrap sub-operation on the first (alphabetically) controlplane node
		bootstrapNode := &nodes[0]
		subOp := talosv1alpha1.ClusterManagedNodeOperationStatus{
			NodeName: bootstrapNode.Name,
		}
		subOp.Type = NodeOperationTypeBootstrap
		subOp.Phase = NodeOperationPhasePending
		subOp.Reason = OperationReasonInitialized
		subOp.Message = "Bootstrap operation initialized by cluster controller"
		subOp.LastTransitionTime = metav1.Now()

		subOps := []talosv1alpha1.ClusterManagedNodeOperationStatus{subOp}

		err = r.updateOpStatus(
			ctx,
			ClusterOperationPhaseInProgress,
			OperationReasonStarted,
			fmt.Sprintf("Bootstrapping cluster on node `%s`", bootstrapNode.Name),
			subOps,
		)
		if err != nil {
			return nil, fmt.Errorf("error updating cluster operation status: %w", err)
		}
		r.Recorder.Eventf(
			cluster,
			bootstrapNode,
			corev1.EventTypeNormal,
			EventReasonStarted,
			EventActionBootstrap,
			"Cluster bootstrap started on node %s",
			bootstrapNode.Name,
		)

	case ClusterOperationPhaseInProgress:
		// run sub-operations
		res, err := r.runSubOperations(ctx, cluster)
		if err != nil {
			return nil, fmt.Errorf("error running sub-operations: %w", err)
		}
		if res != nil {
			return res, nil
		}

	case ClusterOperationPhasePostflight:
		err := r.updateOpStatus(
			ctx,
			ClusterOperationPhaseDone,
			OperationReasonCompleted,
			"Cluster bootstrap completed successfully",
			opStatus.SubOperations,
		)
		if err != nil {
			return nil, fmt.Errorf("error updating cluster operation status: %w", err)
		}
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeNormal,
			EventReasonCompleted,
			EventActionBootstrap,
			"Cluster bootstrap completed successfully",
		)
	}

	return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
}
