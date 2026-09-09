package controller

import (
	"context"
	"fmt"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"

	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
)

func (r *ClusterReconciler) handleRollingApplyOperation(
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

			// ensure all nodes are ready
			if !meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionReady) {
				err = r.updateOpStatus(
					ctx,
					ClusterOperationPhaseFailed,
					ClusterOperationReasonNodeNotReady,
					fmt.Sprintf(
						"Node `%s` is not Ready, cannot proceed with rolling apply",
						node.Name,
					),
					nil,
				)
				if err != nil {
					return nil, fmt.Errorf("error updating cluster operation status: %w", err)
				}
				return nil, nil
			}

			// detect if a node has config drift detected
			if meta.IsStatusConditionTrue(
				node.Status.Conditions,
				NodeConditionConfigDriftDetected,
			) {
				// fail the operation because the node needs to be brought in sync individually first
				err = r.updateOpStatus(
					ctx,
					ClusterOperationPhaseFailed,
					ClusterOperationReasonNodeConfigDrift,
					fmt.Sprintf(
						"Node `%s` has configuration drift detected, please run the Apply operation on the node individually before running a rolling apply",
						node.Name,
					),
					nil,
				)
				if err != nil {
					return nil, fmt.Errorf("error updating cluster operation status: %w", err)
				}
				return nil, nil
			}

			step := talosv1alpha1.ClusterManagedNodeOperationStatus{
				NodeName: node.Name,
			}
			step.Type = NodeOperationTypeApply
			step.Phase = NodeOperationPhasePending
			plan = append(plan, step)
		}

		// start the operation
		err = r.updateOpStatus(
			ctx,
			ClusterOperationPhaseInProgress,
			OperationReasonStarted,
			"Rolling apply operation has started",
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
			"Rolling apply operation is complete",
			opStatus.SubOperations,
		)
		if err != nil {
			return nil, fmt.Errorf("error updating cluster operation status: %w", err)
		}
	}

	return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
}
