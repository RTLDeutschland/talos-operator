package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/RTLDeutschland/talos-operator/internal/logz"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// hasPhaseTimedOut checks if the current operation phase has exceeded the cluster-defined timeout duration.
func (r *NodeReconciler) hasPhaseTimedOut(
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) bool {
	opStatus := node.Status.Operation
	phaseTimeout, err := cluster.Spec.Options.GetPhaseTimeout()
	if err != nil {
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionApply,
			"Invalid phase timeout duration, using default: %v",
			err,
		)
	}
	return time.Since(opStatus.LastTransitionTime.Time) > phaseTimeout
}

// handleNodeApply performs any necessary updates on the Node.
func (r *NodeReconciler) handleNodeApply(
	ctx context.Context,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	// no apply operation requested:
	if r.isOperationComplete(node) {
		return nil, nil
	}
	if node.Status.Operation.Type != NodeOperationTypeApply {
		return nil, nil
	}

	log := logz.New(ctx, "node.apply")

	opStatus := node.Status.Operation

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

	// establish clients
	tCtx, talosClient, err := getTalosClient(ctx, r.Client, node, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get talos client: %w", err)
	}
	defer talosClient.Close()

	remoteClient, err := getKubernetesClient(ctx, r.Client, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes client: %w", err)
	}

	// generate the machine config
	opts := &GenerateMachineConfigOptions{
		WithoutRTLLabel:                !r.FeatureFlags.EnableRTLNodeLabel,
		WithoutCrossNamespacePatchRefs: !r.FeatureFlags.EnableCrossNamespacePatchRefs,
	}
	cfg, _, err := GenerateMachineConfig(ctx, r.Client, node, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate machine config: %w", err)
	}
	encodedCfg, err := EncodeMachineConfig(cfg, false)
	if err != nil {
		return nil, fmt.Errorf("failed to encode machine config: %w", err)
	}

	// get running OS version:
	talosVersion, talosSchematicID, err := getNodeTalosRelease(tCtx, talosClient)
	if err != nil {
		log.Info().Err(err).Msg("failed to get node Talos release, requeueing")
		return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}

	// parse desired install image / schematic
	desiredImage := cfg.Machine().Install().Image()
	desiredTalosVersion, desiredSchematicID, desiredImageErr := parseImageTalosRelease(desiredImage)
	hasSchematic := len(desiredSchematicID) == 64

	// handle node upgrade if we're in an upgrade phase
	if strings.HasPrefix(opStatus.Phase, NodeOperationPhasePrefixUpgrade) {
		res, err := r.handleNodeUpgrade(ctx, node, cluster)
		if err != nil {
			return nil, fmt.Errorf("error handling node upgrade: %w", err)
		}
		if res != nil {
			return res, nil
		}
	}

	switch opStatus.Phase {
	case NodeOperationPhasePending:
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonStarted,
			EventActionApply,
			"Node apply operation started",
		)

		// clear config drift condition, since the only way we can get here in that state is
		// a user-initiated node apply
		if meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionConfigDriftDetected) {
			err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				meta.RemoveStatusCondition(&n.Status.Conditions, NodeConditionConfigDriftDetected)
			})
			if err != nil {
				return nil, fmt.Errorf("failed to clear config drift condition: %w", err)
			}
		}

		// detect if config is already in sync, if so, mark operation as done
		if meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionConfigInSync) {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseDone,
				NodeOperationReasonSkippedConfigInSync,
				"Node configuration is already in sync, so no apply was needed",
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		nextPhase := NodeOperationPhaseApplying
		message := "No OS upgrade needed, proceeding to apply configuration"
		reason := NodeOperationReasonUpgradeNotRequired
		// does the OS need updating?
		// translation: if the Talos version of the image does not match the node status, upgrade it
		// or, if the schematic ID is valid (64 characters) and it doesn't match node status, upgrade it
		if desiredImage != "" && desiredImageErr == nil &&
			(!desiredTalosVersion.EQ(talosVersion) || (hasSchematic && desiredSchematicID != talosSchematicID)) {
			// TODO: feature flag to disable apply-upgrade trigger?

			// next phase is UpgradeDrain, which will handle the upgrade and then return to Applying
			nextPhase = NodeOperationPhaseUpgradeDrain
			message = "OS upgrade needed before apply, proceeding to drain node for upgrade"
			reason = NodeOperationReasonUpgradeRequired
		}

		err = r.updateOpStatus(ctx, nextPhase, reason, message)
		if err != nil {
			return nil, fmt.Errorf("failed to update operation phase: %w", err)
		}

	case NodeOperationPhaseApplying:
		// send a dry run to determine if the node wants to reboot or not in AUTO mode
		resp, err := talosClient.ApplyConfiguration(tCtx, &machine.ApplyConfigurationRequest{
			Mode:   machine.ApplyConfigurationRequest_AUTO,
			DryRun: true,
			Data:   encodedCfg,
		})
		if err != nil {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedApply,
				fmt.Sprintf("Failed to send apply dry-run request: %v", err),
			)
			if err != nil {
				return nil, fmt.Errorf(
					"failed to update operation phase after apply dry-run failure: %w",
					err,
				)
			}
			return nil, nil
		}

		msgs := resp.GetMessages()
		if len(msgs) == 0 {
			return nil, fmt.Errorf(
				"unexpected number of messages from ApplyConfiguration: %d",
				len(msgs),
			)
		}
		msg := msgs[0]
		needsReboot := msg.Mode == machine.ApplyConfigurationRequest_REBOOT // nolint:staticcheck // SA1019 still used as a signal from dry-run

		// if a reboot is required, drain the node first
		if needsReboot {
			k8sNode, err := remoteClient.CoreV1().
				Nodes().
				Get(ctx, node.GetShortName(), metav1.GetOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to get node from remote kubernetes: %w", err)
			}
			var ready = false
			// test if node ready, otherwise drain is unnecessary
			for _, cond := range k8sNode.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			if ready && !k8sNode.Spec.Unschedulable {
				// go to drain phase
				err = r.updateOpStatus(
					ctx,
					NodeOperationPhaseApplyDrain,
					NodeOperationReasonRebootRequired,
					"Node requires reboot after apply, draining node first",
				)
				if err != nil {
					return nil, fmt.Errorf("failed to update operation phase: %w", err)
				}
				return &ctrl.Result{RequeueAfter: RequeueImmediate}, nil
			}
		}

		// send the apply request proper
		_, err = talosClient.ApplyConfiguration(tCtx, &machine.ApplyConfigurationRequest{
			Mode:   machine.ApplyConfigurationRequest_AUTO,
			DryRun: false,
			Data:   encodedCfg,
		})
		if err != nil {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedApply,
				fmt.Sprintf("Failed to send apply request: %v", err),
			)
			if err != nil {
				return nil, fmt.Errorf(
					"failed to update operation phase after apply failure: %w",
					err,
				)
			}
			return nil, nil
		}

		// update AppliedConfigHash & update operation status
		err = r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.AppliedConfigHash = sha256Sum(encodedCfg)
			n.Status.Operation.LastTransitionTime = metav1.NewTime(time.Now())

			// update operation status based on whether a reboot was necessary
			if needsReboot {
				n.Status.Operation.Message = "Configuration applied with reboot, verifying"
				n.Status.Operation.Phase = NodeOperationPhaseApplyVerifying
				n.Status.Operation.Reason = NodeOperationReasonRebootRequired
			} else {
				n.Status.Operation.Message = "Configuration applied, no reboot required"
				n.Status.Operation.Phase = NodeOperationPhaseDone
				n.Status.Operation.Reason = NodeOperationReasonSuccessfulApply
			}
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update status with applied config hash: %w", err)
		}

	case NodeOperationPhaseApplyDrain:
		// drain the node, then hand it back to NodeOperationPhaseApplying to apply the config
		res, err := r.doNodeDrain(ctx, remoteClient, node, cluster, NodeOperationPhaseApplying)
		if err != nil {
			return nil, fmt.Errorf("failed to drain node for apply: %w", err)
		}
		if res != nil {
			return res, nil
		}

	case NodeOperationPhaseApplyVerifying:
		// check timeouts
		if r.hasPhaseTimedOut(node, cluster) {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonTimedOutVerifyingApply,
				"Timed out waiting for node to reboot and become Ready after apply",
			)
			if err != nil {
				return nil, err
			}

			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeWarning,
				EventReasonFailed,
				EventActionApply,
				"Timed out waiting for node to reboot and become Ready after apply",
			)
			return nil, nil
		}

		// has node rebooted yet?
		if !r.hasNodeRebootedYet(tCtx, talosClient, remoteClient, node) {
			return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
		}

		// check if node is Ready in k8s:
		// notably, our nodes.talos.rtl.de KubernetesReady condition *isn't* updated during operations,
		// so we can use it to determine if the node was ready before the operation started.
		if meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionKubernetesReady) {
			remoteClient, err := getKubernetesClient(ctx, r.Client, cluster)
			if err != nil {
				return nil, fmt.Errorf("failed to get remote kubernetes client: %w", err)
			}

			k8sNode, err := remoteClient.CoreV1().
				Nodes().
				Get(ctx, node.GetShortName(), metav1.GetOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to get node from remote kubernetes: %w", err)
			}
			ready := false
			for _, cond := range k8sNode.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			if !ready {
				return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
			}
		}

		// uncordon the node, only if it was not cordoned before apply.
		// node status is not updated during operations, so we can use KubernetesUnschedulable to determine this.
		if !meta.IsStatusConditionTrue(
			node.Status.Conditions,
			NodeConditionKubernetesUnschedulable,
		) {
			err = r.doNodeUncordon(ctx, remoteClient, node)
			if err != nil {
				return nil, fmt.Errorf("failed to uncordon node after apply: %w", err)
			}
		}

		// mark config as in-sync
		err = r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionConfigInSync,
				Status:  metav1.ConditionTrue,
				Reason:  NodeConditionReasonConfigInSync,
				Message: "Node configuration is in sync",
			})
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to update node status with config in sync condition: %w",
				err,
			)
		}

		// mark operation as complete
		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseDone,
			OperationReasonCompleted,
			"Node apply operation complete",
		)
		if err != nil {
			return nil, err
		}
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonCompleted,
			EventActionApply,
			"Node apply operation complete",
		)
	}

	return &ctrl.Result{RequeueAfter: RequeueImmediate}, nil
}
