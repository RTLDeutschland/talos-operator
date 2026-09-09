package controller

import (
	"context"
	"fmt"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/RTLDeutschland/talos-operator/internal/logz"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	SubOpPhaseApplying = "Applying"
)

// runSubOperations processes the sub-operations of a cluster operation.
// takes phase InProgress and returns with phase Postflight when done.
func (r *ClusterReconciler) runSubOperations(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
) (*reconcile.Result, error) {
	log := logz.New(ctx, "cluster.runSubOperations")
	opStatus := cluster.Status.Operation

	if opStatus.Phase == ClusterOperationPhaseInProgress {
		// find the subOp we're working on
		var i int
		var subOp talosv1alpha1.ClusterManagedNodeOperationStatus
		for i = range opStatus.SubOperations {
			if opStatus.SubOperations[i].Phase != NodeOperationPhaseDone {
				subOp = opStatus.SubOperations[i]
				break
			}
		}

		// check if we found a subOp to work on
		if subOp.Type == "" {
			// all done
			err := r.updateOpStatus(
				ctx,
				ClusterOperationPhasePostflight,
				OperationReasonCompleted,
				opStatus.Type+" sub-operations are complete",
				opStatus.SubOperations,
			)
			if err != nil {
				return nil, fmt.Errorf("error updating cluster operation status: %w", err)
			}
			return nil, nil
		}

		// update cluster operation status message
		statusMessage := fmt.Sprintf(
			"Running operation %d of %d on node `%s`",
			i+1,
			len(opStatus.SubOperations),
			subOp.NodeName,
		)
		if opStatus.Message != statusMessage {
			err := r.updateOpStatus(
				ctx,
				ClusterOperationPhaseInProgress,
				opStatus.Reason,
				statusMessage,
				opStatus.SubOperations,
			)
			if err != nil {
				return nil, fmt.Errorf("error updating cluster operation status: %w", err)
			}
			opStatus = cluster.Status.Operation
		}

		log.Debug().
			Str("node.name", subOp.NodeName).
			Str("operation.type", subOp.Type).
			Str("operation.phase", subOp.Phase).
			Msg("Working on sub-operation")

		// fetch the node
		node := &talosv1alpha1.Node{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      subOp.NodeName,
		}, node)
		if err != nil {
			err = r.updateOpStatus(
				ctx,
				ClusterOperationPhaseFailed,
				ClusterOperationReasonNodeGetFailed,
				fmt.Sprintf("Failed to fetch node `%s`: %v", subOp.NodeName, err),
				opStatus.SubOperations,
			)
			if err != nil {
				return nil, fmt.Errorf("error updating cluster operation status: %w", err)
			}
			return nil, nil
		}

		// do we need to start an operation?
		if subOp.Phase == NodeOperationPhasePending {
			// start the node operation
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := r.Get(ctx, client.ObjectKeyFromObject(node), node); err != nil {
					return err
				}

				node.Status.Operation = &talosv1alpha1.NodeOperationStatus{
					NodeOperation:      subOp.NodeOperation,
					Phase:              NodeOperationPhasePending,
					Reason:             OperationReasonInitialized,
					Message:            "Node operation has been initialized by cluster controller",
					LastTransitionTime: metav1.Now(),
				}

				return r.Status().Update(ctx, node)
			})
			if err != nil {
				return nil, fmt.Errorf(
					"error starting node operation on node `%s`: %w",
					node.Name,
					err,
				)
			}

			subOp.Phase = SubOpPhaseApplying
			subOp.LastTransitionTime = metav1.Now()
			subOp.Reason = OperationReasonStarted
			subOp.Message = "Node operation has been started"
			opStatus.SubOperations[i] = subOp
			err = r.updateOpStatus(
				ctx,
				opStatus.Phase,
				opStatus.Reason,
				opStatus.Message,
				opStatus.SubOperations,
			)
			if err != nil {
				return nil, fmt.Errorf("error updating cluster operation status: %w", err)
			}
			return &reconcile.Result{RequeueAfter: RequeueShortDelay}, nil
		}

		// monitor the node operation
		if subOp.Phase == SubOpPhaseApplying {
			// check if the node operation is done
			if node.Status.Operation != nil &&
				(node.Status.Operation.Phase == NodeOperationPhaseDone || node.Status.Operation.Phase == NodeOperationPhaseFailed) {
				// operation is done
				subOp.Phase = node.Status.Operation.Phase
				subOp.Reason = node.Status.Operation.Reason
				subOp.Message = node.Status.Operation.Message
				subOp.LastTransitionTime = metav1.Now()
				opStatus.SubOperations[i] = subOp
				err = r.updateOpStatus(
					ctx,
					opStatus.Phase,
					opStatus.Reason,
					opStatus.Message,
					opStatus.SubOperations,
				)
				if err != nil {
					return nil, fmt.Errorf("error updating cluster operation status: %w", err)
				}
				opStatus = cluster.Status.Operation
				// we don't return here to check for failure
			} else {
				// otherwise, keep waiting

				phaseTimeout, err := cluster.Spec.Options.GetPhaseTimeout()
				if err != nil {
					r.Recorder.Eventf(
						cluster,
						nil,
						corev1.EventTypeWarning,
						EventReasonValidationError,
						EventActionRunningOperation,
						"Invalid phase timeout duration, using default: %v",
						err,
					)
				}
				// add some extra time to the duration to account for the node controller's own timeout logic
				phaseTimeout += 2 * time.Minute

				// with a check for a timeout condition:
				if time.Since(subOp.LastTransitionTime.Time) > phaseTimeout {
					// timeout
					err = r.updateOpStatus(
						ctx,
						ClusterOperationPhaseFailed,
						OperationReasonTimedOut,
						fmt.Sprintf(
							"Node operation on node `%s` timed out",
							subOp.NodeName,
						),
						opStatus.SubOperations,
					)
					if err != nil {
						return nil, fmt.Errorf("error updating cluster operation status: %w", err)
					}
					return nil, nil
				}

				return &reconcile.Result{RequeueAfter: RequeueShortDelay}, nil
			}
		}

		// check for failure (which can't be hit any other way except fallthrough from above)
		if subOp.Phase == NodeOperationPhaseFailed {
			err = r.updateOpStatus(
				ctx,
				ClusterOperationPhaseFailed,
				ClusterOperationReasonSubOperationFailed,
				fmt.Sprintf(
					"Node operation on node `%s` failed: %s",
					subOp.NodeName,
					subOp.Message,
				),
				opStatus.SubOperations,
			)
			if err != nil {
				return nil, fmt.Errorf("error updating cluster operation status: %w", err)
			}
			return nil, nil
		}

		return &reconcile.Result{RequeueAfter: RequeueShortDelay}, nil
	}

	return nil, nil
}
