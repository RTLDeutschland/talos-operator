package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/siderolabs/talos/pkg/machinery/client"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
)

func (r *NodeReconciler) handleNodePower(
	ctx context.Context,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	if r.isOperationComplete(node) {
		return nil, nil
	}

	var opStatus = node.Status.Operation
	var err error

	if opStatus.Type != NodeOperationTypeReboot &&
		opStatus.Type != NodeOperationTypeShutdown {
		return nil, nil
	}

	// grab a cluster mutex to avoid concurrent reboots
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

	var verb string
	var verbTitle string
	switch opStatus.Type {
	case NodeOperationTypeReboot:
		verb = "reboot"
		verbTitle = "Reboot"
	case NodeOperationTypeShutdown:
		verb = "shutdown"
		verbTitle = "Shutdown"
	}

	tCtx, talosClient, err := getTalosClient(ctx, r.Client, node, cluster)
	if err != nil {
		return nil, err
	}
	defer talosClient.Close()

	remoteClient, err := getKubernetesClient(ctx, r.Client, cluster)
	if err != nil {
		return nil, err
	}

	switch opStatus.Phase {
	case NodeOperationPhasePending:
		// detect if unsafe was requested and skip drain
		if opStatus.Options.Unsafe {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseApplying,
				OperationReasonStarted,
				fmt.Sprintf("%s of node in progress (unsafe)", verbTitle),
			)
			if err != nil {
				return nil, err
			}
			return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
		}

		// just do it
		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseDraining,
			OperationReasonStarted,
			fmt.Sprintf("Started %s of node", verb),
		)
		if err != nil {
			return nil, err
		}

	case NodeOperationPhaseDraining:
		res, err := r.doNodeDrain(ctx, remoteClient, node, cluster, NodeOperationPhaseApplying)
		if err != nil {
			return nil, fmt.Errorf("failed to drain: %w", err)
		}
		if res != nil {
			return res, nil
		}

	case NodeOperationPhaseApplying:
		switch opStatus.Type {
		case NodeOperationTypeReboot:
			rebootOpts := make([]client.RebootMode, 0, 1)
			if opStatus.Options.Unsafe {
				rebootOpts = append(rebootOpts, client.WithForce)
			}
			err = talosClient.Reboot(tCtx, rebootOpts...)
		case NodeOperationTypeShutdown:
			err = talosClient.Shutdown(tCtx)
		default:
			err = fmt.Errorf("unsupported operation type: %s", opStatus.Type)
		}
		if err != nil {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedPowerOperation,
				fmt.Sprintf("%s of node failed: %s", verbTitle, err.Error()),
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		// mark operation as complete
		phase := NodeOperationPhaseDone
		reason := OperationReasonCompleted
		if opStatus.Type == NodeOperationTypeReboot {
			phase = NodeOperationPhaseVerifying
			reason = NodeOperationReasonRebootRequested
		}
		if phase == EventReasonCompleted {
			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeNormal,
				EventReasonCompleted,
				EventActionPowerOperation,
				"Node shutdown complete",
			)
			return nil, nil
		}
		err = r.updateOpStatus(
			ctx,
			phase,
			reason,
			fmt.Sprintf("%s of node initiated successfully", verbTitle),
		)
		if err != nil {
			return nil, err
		}

	case NodeOperationPhaseVerifying:
		// for reboot, we need to verify the node comes back up

		phaseTimeout, err := cluster.Spec.Options.GetPhaseTimeout()
		if err != nil {
			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeWarning,
				EventReasonValidationError,
				EventActionPowerOperation,
				"Invalid phase timeout duration, using default: %v",
				err,
			)
		}

		// wait for node to come back up
		if time.Since(opStatus.LastTransitionTime.Time) > phaseTimeout {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonTimedOutVerifyingReboot,
				"Node verification after reboot timed out",
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}
		if !r.hasNodeRebootedYet(tCtx, talosClient, remoteClient, node) {
			return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
		}

		// uncordon the node
		err = r.doNodeUncordon(ctx, remoteClient, node)
		if err != nil {
			return nil, fmt.Errorf("failed to uncordon node after reboot: %w", err)
		}

		// mark operation as complete
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonCompleted,
			EventActionPowerOperation,
			"Node reboot complete",
		)

		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseDone,
			OperationReasonCompleted,
			"Node reboot operation complete",
		)
		if err != nil {
			return nil, err
		}
	}

	return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
}
