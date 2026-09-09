package controller

import (
	"context"
	"fmt"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *ClusterReconciler) isOperationComplete(cluster *talosv1alpha1.Cluster) bool {
	if cluster.Status.Operation == nil {
		return true
	}
	return cluster.Status.Operation.Phase == ClusterOperationPhaseDone ||
		cluster.Status.Operation.Phase == ClusterOperationPhaseFailed
}

func (r *ClusterReconciler) handleRollingRebootOperation(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	opStatus := cluster.Status.Operation

	switch opStatus.Phase {
	case ClusterOperationPhasePending:
		// generate a plan
		nodes, err := r.listEligibleNodes(ctx, cluster)
		if err != nil {
			return nil, err
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

		// make a plan
		var plan []talosv1alpha1.ClusterManagedNodeOperationStatus
		for _, node := range nodes {
			step := talosv1alpha1.ClusterManagedNodeOperationStatus{
				NodeName: node.Name,
			}
			step.Type = NodeOperationTypeReboot
			step.Phase = NodeOperationPhasePending
			plan = append(plan, step)
		}

		// start the operation
		err = r.updateOpStatus(
			ctx,
			ClusterOperationPhaseInProgress,
			OperationReasonStarted,
			"Rolling reboot operation has started",
			plan,
		)
		if err != nil {
			return nil, fmt.Errorf("error updating cluster operation status: %w", err)
		}

	case ClusterOperationPhaseInProgress:
		res, err := r.runSubOperations(ctx, cluster)
		if err != nil {
			return nil, fmt.Errorf("error running sub-operations: %w", err)
		}
		if res != nil {
			return res, nil
		}

	case ClusterOperationPhasePostflight:
		// mark operation done
		err := r.updateOpStatus(
			ctx,
			ClusterOperationPhaseDone,
			OperationReasonCompleted,
			"Rolling reboot operation is complete",
			opStatus.SubOperations,
		)
		if err != nil {
			return nil, fmt.Errorf("error updating cluster operation status: %w", err)
		}
	}

	return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
}
