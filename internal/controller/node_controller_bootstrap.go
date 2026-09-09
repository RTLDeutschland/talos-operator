package controller

import (
	"context"
	"fmt"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *NodeReconciler) handleNodeBootstrap(
	ctx context.Context,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if r.isOperationComplete(node) {
		return nil, nil
	}
	if node.Status.Operation.Type != NodeOperationTypeBootstrap {
		return nil, nil
	}

	// grab a cluster mutex to avoid concurrent updates
	otherOpsInProgress, err := r.areOtherOperationsInProgress(ctx, cluster, node)
	if err != nil {
		return nil, fmt.Errorf("error checking for other operations in progress: %w", err)
	}
	if otherOpsInProgress {
		return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}

	opStatus := node.Status.Operation
	switch opStatus.Phase {
	case NodeOperationPhasePending:
		// update operation status
		err := r.updateOpStatus(
			ctx,
			NodeOperationPhaseApplying,
			OperationReasonStarted,
			"Started bootstrap of node",
		)
		if err != nil {
			return nil, err
		}

		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonStarted,
			EventActionBootstrap,
			"Bootstrap operation started",
		)

	case NodeOperationPhaseApplying:
		tCtx, talosClient, err := getTalosClient(ctx, r.Client, node, cluster)
		if err != nil {
			return nil, err
		}
		defer talosClient.Close()

		ttCtx, cancel := context.WithTimeout(tCtx, 10*time.Second)
		defer cancel() // avoid context leak

		err = talosClient.Bootstrap(ttCtx, nil)
		if err != nil {
			logger.V(0).Error(err, "failed to bootstrap node")

			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeWarning,
				EventReasonFailed,
				EventActionBootstrap,
				"Bootstrap operation failed: %v", err,
			)

			updateErr := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedBootstrap,
				"Bootstrap of node failed: "+err.Error(),
			)
			if updateErr != nil {
				return nil, fmt.Errorf(
					"failed to update node operation status after bootstrap failure: %w",
					updateErr,
				)
			}

			return nil, nil
		}

		// set operation phase to Completed
		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseDone,
			OperationReasonCompleted,
			"Bootstrap of node completed successfully",
		)
		if err != nil {
			return nil, err
		}

		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonCompleted,
			EventActionBootstrap,
			"Bootstrap operation completed successfully",
		)
	}

	return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
}
