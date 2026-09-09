package controller

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/samber/lo"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	tclient "github.com/siderolabs/talos/pkg/machinery/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// handleNodeProvisioning handles the provisioning of a Talos node in maintenance mode.
func (r *NodeReconciler) handleNodeProvisioning(
	ctx context.Context,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	if r.isOperationComplete(node) {
		return nil, nil
	}
	if node.Status.Operation.Type != NodeOperationTypeProvision {
		return nil, nil
	}
	if node.Status.Operation.Provision.Address == "" && !node.Status.Operation.Provision.Skip {
		meta.SetStatusCondition(&node.Status.Conditions, metav1.Condition{
			Type:    NodeConditionProvisioned,
			Status:  metav1.ConditionFalse,
			Reason:  NodeConditionReasonInvalidArguments,
			Message: "Provision operation requires a valid address",
		})
		err := r.updateOpStatus(
			ctx,
			NodeOperationPhaseFailed,
			NodeOperationReasonInvalidArguments,
			"Provision operation requires a valid address",
		)
		if err != nil {
			return nil, err
		}
		return nil, nil
	}

	var opStatus = node.Status.Operation
	var err error

	// authenticated Talos client, only available once the node has left maintenance mode
	var tCtx context.Context
	var talosClient *tclient.Client
	if opStatus.Phase != NodeOperationPhaseProvisioning {
		tCtx, talosClient, err = getTalosClient(ctx, r.Client, node, cluster)
		if err != nil {
			return nil, fmt.Errorf("failed to create Talos client: %w", err)
		}
		defer talosClient.Close()
	}

	switch opStatus.Phase {
	case NodeOperationPhasePending:
		if meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionProvisioned) {
			// prevent an already provisioned node from accidentally provisioning a different node
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonAlreadyProvisionedNode,
				"Node is already provisioned, ignoring request",
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		if opStatus.Provision.Skip {
			// set conditions
			err = r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				meta.SetStatusCondition(&n.Status.Conditions,
					metav1.Condition{
						Type:    NodeConditionProvisioned,
						Status:  metav1.ConditionTrue,
						Reason:  NodeConditionReasonSkippedProvisioning,
						Message: "Provisioning operation was skipped as per request",
					},
				)
			})
			if err != nil {
				return nil, fmt.Errorf(
					"failed to update node status for skipped provisioning: %w",
					err,
				)
			}

			// send event
			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeNormal,
				EventReasonSkipped,
				EventActionProvision,
				"Node provisioning was skipped as per request",
			)

			// mark op as complete
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseProvisionVerifying,
				NodeOperationReasonSkippedProvisioning,
				"Provisioning operation was skipped as per request",
			)
			if err != nil {
				return nil, err
			}
			return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
		}

		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonStarted,
			EventActionProvision,
			"Node provisioning started (address: %q)",
			opStatus.Provision.Address,
		)

		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseProvisioning,
			OperationReasonStarted,
			"Started provisioning of node",
		)
		if err != nil {
			return nil, err
		}

	case NodeOperationPhaseProvisioning:
		insecureCtx := tclient.WithNode(ctx, opStatus.Provision.Address)
		insecureClient, err := tclient.New(
			insecureCtx,
			tclient.WithEndpoints(opStatus.Provision.Address),
			tclient.WithTLSConfig(&tls.Config{InsecureSkipVerify: true}),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create insecure Talos client: %w", err)
		}
		defer insecureClient.Close()

		// test connectivity
		timeoutCtx, cancel := context.WithTimeout(insecureCtx, 1*time.Second)
		defer cancel()
		_, err = insecureClient.Disks(timeoutCtx)
		if err != nil {
			if strings.Contains(err.Error(), "tls: certificate required") {
				// the target provisioning address already has a provisioned Talos node, so we abort.
				err = r.updateOpStatus(
					ctx,
					NodeOperationPhaseFailed,
					NodeOperationReasonAlreadyProvisionedTarget,
					"Target address is an already provisioned Talos node",
				)
				if err != nil {
					return nil, err
				}

				return nil, nil
			}

			// node is not yet reachable
			err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
					Type:    NodeConditionProvisioned,
					Status:  metav1.ConditionFalse,
					Reason:  NodeReasonNodeUnreachable,
					Message: "Node is not reachable and not provisioned (will keep trying): " + err.Error(),
				})
			})
			if err != nil {
				return nil, fmt.Errorf(
					"failed to update node provisioned status to unreachable: %w",
					err,
				)
			}
			return &reconcile.Result{RequeueAfter: RequeueStandardDelay}, nil
		}

		// assemble the config and send it
		opts := &GenerateMachineConfigOptions{
			WithoutRTLLabel:                !r.FeatureFlags.EnableRTLNodeLabel,
			WithoutCrossNamespacePatchRefs: !r.FeatureFlags.EnableCrossNamespacePatchRefs,
		}
		cfg, _, err := GenerateMachineConfig(ctx, r.Client, node, opts)
		if err != nil {
			err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
					Type:    NodeConditionProvisioned,
					Status:  metav1.ConditionFalse,
					Reason:  NodeReasonConfigGenerationFailed,
					Message: "Failed to generate Talos config for node: " + err.Error(),
				})
			})
			if err != nil {
				return nil, fmt.Errorf(
					"failed to update node provisioned status with config generation failure: %w",
					err,
				)
			}
			return nil, nil
		}
		encodedCfg, err := EncodeMachineConfig(cfg, false)
		if err != nil {
			err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
					Type:    NodeConditionProvisioned,
					Status:  metav1.ConditionFalse,
					Reason:  NodeReasonConfigSerializationFailed,
					Message: "Failed to encode Talos config for node: " + err.Error(),
				})
			})
			if err != nil {
				return nil, fmt.Errorf(
					"failed to update node provisioned status with config encoding failure: %w",
					err,
				)
			}
			return nil, nil
		}

		// send Apply to the node in maintenance mode
		insecureTimeoutCtx, cancel := context.WithTimeout(insecureCtx, 30*time.Second)
		defer cancel()
		_, err = insecureClient.ApplyConfiguration(
			insecureTimeoutCtx,
			&machine.ApplyConfigurationRequest{
				Mode: machine.ApplyConfigurationRequest_AUTO,
				Data: encodedCfg,
			},
		)
		if err != nil {
			err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
					Type:    NodeConditionProvisioned,
					Status:  metav1.ConditionFalse,
					Reason:  NodeConditionReasonFailedApply,
					Message: "Failed to apply Talos config to node: " + err.Error(),
				})
			})
			if err != nil {
				return nil, fmt.Errorf(
					"failed to update node provisioned status with apply failure: %w",
					err,
				)
			}

			return &reconcile.Result{RequeueAfter: RequeueStandardDelay}, nil
		}

		err = r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.AppliedConfigHash = sha256Sum(encodedCfg)
			n.Status.Operation.Phase = NodeOperationPhaseProvisionVerifying
			n.Status.Operation.Reason = NodeOperationReasonSuccessfulApply
			n.Status.Operation.Message = "Applied Talos config, waiting for node to leave maintenance mode"
			n.Status.Operation.LastTransitionTime = metav1.Now()
			meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
				Type:    NodeConditionProvisioned,
				Status:  metav1.ConditionTrue,
				Reason:  NodeConditionReasonSuccessfulApply,
				Message: "Successfully applied Talos config to node in maintenance mode",
			})
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update node status after successful apply: %w", err)
		}

	case NodeOperationPhaseProvisionVerifying:
		phaseTimeout, err := cluster.Spec.Options.GetPhaseTimeout()
		if err != nil {
			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeWarning,
				EventReasonValidationError,
				EventActionProvision,
				"Invalid phase timeout duration, using default: %v",
				err,
			)
		}
		if time.Since(opStatus.LastTransitionTime.Time) > phaseTimeout {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonTimedOutVerifyingProvision,
				"Timed out waiting for node to become provisioned",
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		// check if we can connect to the node via the authenticated client (i.e., it has left maintenance mode)
		ttCtx, cancel := context.WithTimeout(tCtx, 1*time.Second)
		defer cancel()
		_, err = talosClient.Disks(ttCtx)
		if err != nil {
			err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
					Type:    NodeConditionReady,
					Status:  metav1.ConditionFalse,
					Reason:  NodeConditionReasonWaitingForNode,
					Message: "Node is still in maintenance mode, waiting to become provisioned: " + err.Error(),
				})
			})
			if err != nil {
				return nil, fmt.Errorf(
					"failed to update node provisioned status while waiting for node to become provisioned: %w",
					err,
				)
			}
			return &reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// add finalizer
		err = r.updateNode(ctx, func(n *talosv1alpha1.Node) {
			n.Finalizers = lo.Uniq(append(n.Finalizers, NodeFinalizer))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to add finalizer to node after provisioning: %w", err)
		}

		// detect if the running OS image matches the desired image from config;
		// disk-installed nodes don't upgrade on provision, so we may need to upgrade now.
		opts := &GenerateMachineConfigOptions{
			WithoutRTLLabel:                !r.FeatureFlags.EnableRTLNodeLabel,
			WithoutCrossNamespacePatchRefs: !r.FeatureFlags.EnableCrossNamespacePatchRefs,
		}
		cfg, _, err := GenerateMachineConfig(ctx, r.Client, node, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to generate machine config for image check: %w", err)
		}
		desiredImage := cfg.Machine().Install().Image()
		desiredTalosVersion, desiredSchematicID, desiredImageErr := parseImageTalosRelease(
			desiredImage,
		)

		talosVersion, talosSchematicID, err := getNodeTalosRelease(tCtx, talosClient)
		if err != nil {
			return nil, fmt.Errorf("failed to get node Talos release for image check: %w", err)
		}

		nodeNeedsUpdate := desiredImage != "" && desiredImageErr == nil &&
			(!desiredTalosVersion.Equals(talosVersion) || (desiredSchematicID != "" && desiredSchematicID != talosSchematicID))

		if nodeNeedsUpdate {
			reason := strings.Join([]string{
				"Node is running an OS image that doesn't match the desired image. ",
				"An Apply should be started to resolve this.",
			}, " ")
			err := r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				meta.SetStatusCondition(&n.Status.Conditions, metav1.Condition{
					Type:    NodeConditionNeedsInitialUpdate,
					Status:  metav1.ConditionTrue,
					Reason:  NodeConditionReasonNodeNeedsInitialUpdate,
					Message: reason,
				})
			})
			if err != nil {
				return nil, fmt.Errorf(
					"failed to set node condition for initial update needed: %w",
					err,
				)
			}
		}

		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseDone,
			OperationReasonCompleted,
			"Node provisioned",
		)
		if err != nil {
			return nil, err
		}

		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonCompleted,
			EventActionProvision,
			"Node provisioning is complete",
		)
	}

	return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
}
