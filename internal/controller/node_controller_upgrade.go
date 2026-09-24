package controller

import (
	"context"
	"fmt"
	"io"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/RTLDeutschland/talos-operator/internal/logz"
	"github.com/blang/semver/v4"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
)

/*
handleNodeUpgrade handles upgrade operations, either as OperationTypeUpgrade or OperationTypeApply.

More specifically, this handles the new upgrade logic around Talos 1.13's LifecycleService API,
with a fallback to the MachineService.Upgrade API for older Talos versions.

Ref: https://docs.siderolabs.com/talos/v1.13/getting-started/what's-new-in-talos
*/
func (r *NodeReconciler) handleNodeUpgrade(
	ctx context.Context,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	log := logz.New(ctx, "node.upgrade")

	// do our usual mutex checks:
	if r.isOperationComplete(node) {
		return nil, nil
	}

	opStatus := node.Status.Operation

	// grab a cluster mutex to avoid concurrent updates
	otherOpsInProgress, err := r.areOtherOperationsInProgress(ctx, cluster, node)
	if err != nil {
		return nil, fmt.Errorf("error checking for other operations in progress: %w", err)
	}
	if otherOpsInProgress {
		return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}

	// handle both operation types
	switch opStatus.Type {
	case NodeOperationTypeUpgrade:
		// acquire the mutex
		mutex := r.Mutexes.For(cluster.Namespace + ":" + cluster.Name)
		hasMutex := mutex.TryLock()
		if !hasMutex {
			// another operation is ongoing for this cluster
			return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
		}
		defer mutex.Unlock()
	case NodeOperationTypeApply:
		// assume we are invoked by handleNodeApply which already has the mutex, so we skip it here.
	default:
		// not an upgrade/apply operation, so we skip
		return nil, nil
	}

	// create shared state across various upgrade phases:

	systemContainerd := &common.ContainerdInstance{
		Driver:    common.ContainerDriver_CONTAINERD,
		Namespace: common.ContainerdNamespace_NS_SYSTEM,
	}

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
		if err := r.updateOpStatus(
			ctx,
			NodeOperationPhaseFailed,
			NodeReasonConfigGenerationFailed,
			fmt.Sprintf("Failed to generate machine config: %v", err),
		); err != nil {
			return nil, fmt.Errorf("error updating operation status: %w", err)
		}
		return nil, nil
	}

	// get running OS version:
	talosVersion, talosSchematicID, err := getNodeTalosRelease(tCtx, talosClient)
	if err != nil {
		if err := r.updateOpStatus(
			ctx,
			NodeOperationPhaseFailed,
			NodeReasonNodeUnreachable,
			fmt.Sprintf("Failed to get node Talos release: %v", err),
		); err != nil {
			return nil, fmt.Errorf("error updating operation status: %w", err)
		}
		return nil, nil
	}

	// parse desired install image / schematic
	desiredImage, _ := GetInstallImage(cfg)
	desiredTalosVersion, desiredSchematicID, desiredImageErr := parseImageTalosRelease(
		desiredImage,
	)
	hasSchematic := len(desiredSchematicID) == 64
	if desiredImageErr != nil {
		if err := r.updateOpStatus(
			ctx,
			NodeOperationPhaseFailed,
			NodeConditionReasonFailedImageValidation,
			fmt.Sprintf("Failed to parse install image Talos release: %v", desiredImageErr),
		); err != nil {
			return nil, fmt.Errorf("error updating operation status: %w", err)
		}
		return nil, nil
	}

	switch opStatus.Phase {
	case NodeOperationPhasePending:
		// do we need to upgrade?
		var nodeNeedsUpgrade bool
		if hasSchematic && talosSchematicID != desiredSchematicID {
			nodeNeedsUpgrade = true
		}
		if talosVersion.NE(desiredTalosVersion) {
			nodeNeedsUpgrade = true
		}

		if !nodeNeedsUpgrade {
			// node is already at desired version, so we skip
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseDone,
				NodeOperationReasonSkippedUpgrade,
				"Node is already at desired version",
			)
			if err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}

		if meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionKubernetesReady) {
			// next step is drain because Kubernetes is ready
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseUpgradeDrain,
				NodeOperationReasonKubernetesReady,
				"Node upgrade requested and Kubernetes is ready, proceeding to drain",
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
		} else {
			// if Kubernetes is not ready, we skip drain and go straight to preflight
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseUpgradePreflight,
				NodeOperationReasonKubernetesNotReadySkippedDrain,
				"Node upgrade requested but Kubernetes is not ready, skipping drain and proceeding to preflight",
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
		}

		return &ctrl.Result{RequeueAfter: RequeueImmediate}, nil

	case NodeOperationPhaseUpgradeDrain:
		res, err := r.doNodeDrain(
			ctx,
			remoteClient,
			node,
			cluster,
			NodeOperationPhaseUpgradePreflight,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to drain: %w", err)
		}
		if res != nil {
			return res, nil
		}

	case NodeOperationPhaseUpgradePreflight:
		// test if the image exists on the remote, otherwise both upgrade paths run into undefined behavior
		imageRef, err := name.ParseReference(desiredImage)
		if err != nil {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedImageValidation,
				fmt.Sprintf("Failed to parse desired image reference: %v", err),
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}
		desc, err := remote.Head(imageRef)
		if err != nil {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedImageValidation,
				fmt.Sprintf("Failed to fetch desired image from registry: %v", err),
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}
		if desc == nil {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedImageValidation,
				"Received nil descriptor when fetching desired image from registry",
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}

		// check which variant of upgrade we should be doing
		talosVersion, _, err := getNodeTalosRelease(tCtx, talosClient)
		if err != nil {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeReasonNodeUnreachable,
				fmt.Sprintf("Failed to get node Talos release: %v", err),
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}

		// FIXME(deprecation): When dropping support for Talos < 1.13
		if talosVersion.GTE(semver.Version{Major: 1, Minor: 13}) {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseUpgrade113ImagePull,
				NodeOperationReasonUpgradePathPost113,
				"Node requires upgrade and is running Talos 1.13+",
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
		} else {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseUpgradeLegacyUpgrade,
				NodeOperationReasonUpgradePathPre113,
				"Node requires upgrade and is running Talos <1.13",
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
		}

		return &ctrl.Result{RequeueAfter: RequeueImmediate}, nil

	case NodeOperationPhaseUpgrade113ImagePull:
		respStream, err := talosClient.ImageClient.Pull(tCtx, &machine.ImageServicePullRequest{
			Containerd: systemContainerd,
			ImageRef:   desiredImage,
		})
		if err != nil {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedGRPCCallImagePull,
				fmt.Sprintf("Failed to start image pull: %v", err),
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}
		defer respStream.CloseSend()

		// deal with the response stream
		var ok bool
		for {
			resp, err := respStream.Recv()
			if err == io.EOF {
				break
			}

			if err != nil {
				if err := r.updateOpStatus(
					ctx,
					NodeOperationPhaseFailed,
					NodeOperationReasonFailedGRPCCallImagePull,
					fmt.Sprintf("Error during image pull: %v", err),
				); err != nil {
					return nil, fmt.Errorf("error updating operation status: %w", err)
				}
				return nil, nil
			}

			switch msg := resp.GetResponse().(type) {
			case *machine.ImageServicePullResponse_PullProgress:
				layer := msg.PullProgress.LayerId
				human := msg.PullProgress.GetProgress().Fmt()
				log.Debug().Msgf("Install image pull progress update: %s: %s", layer, human)
			case *machine.ImageServicePullResponse_Name:
				log.Info().Str("image", msg.Name).Msg("Pulled install image successfully")
				ok = true
			}
		}
		if !ok {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedGRPCCallImagePullBadResult,
				"Image pull completed without receiving image name",
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}

		if err := r.updateOpStatus(
			ctx,
			NodeOperationPhaseUpgrade113Upgrade,
			NodeOperationReasonSuccessfulImagePull,
			"Install image pulled successfully, proceeding to upgrade preflight decision",
		); err != nil {
			return nil, fmt.Errorf("error updating operation status: %w", err)
		}
		return &ctrl.Result{RequeueAfter: RequeueImmediate}, nil

	case NodeOperationPhaseUpgrade113Upgrade:
		// same deal as with GRPC image pull, just for the upgrade API
		respStream, err := talosClient.LifecycleClient.Upgrade(
			tCtx,
			&machine.LifecycleServiceUpgradeRequest{
				Containerd: systemContainerd,
				Source: &machine.InstallArtifactsSource{
					ImageName: desiredImage,
				},
			},
		)
		if err != nil {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedGRPCCallLifecycleClientUpgrade,
				fmt.Sprintf("Failed to start upgrade: %v", err),
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}

		var msg string
		for {
			resp, err := respStream.Recv()
			if err == io.EOF {
				break
			}

			if err != nil {
				if err := r.updateOpStatus(
					ctx,
					NodeOperationPhaseFailed,
					NodeOperationReasonFailedGRPCCallLifecycleClientUpgrade,
					fmt.Sprintf("Error during upgrade: %v", err),
				); err != nil {
					return nil, fmt.Errorf("error updating operation status: %w", err)
				}
				return nil, nil
			}

			switch progress := resp.Progress.GetResponse().(type) {
			case nil:
			case *machine.LifecycleServiceInstallProgress_Message:
				// store the message for later error reporting
				msg = progress.Message
			case *machine.LifecycleServiceInstallProgress_ExitCode:
				if progress.ExitCode != 0 {
					if err := r.updateOpStatus(
						ctx,
						NodeOperationPhaseFailed,
						NodeOperationReasonFailedUpgrade,
						fmt.Sprintf(
							"Upgrade process exited with code %d, message %q",
							progress.ExitCode,
							msg,
						),
					); err != nil {
						return nil, fmt.Errorf("error updating operation status: %w", err)
					}
					return nil, nil
				}
			}
		}
		// we assume that because no exit code != 0 was reported, the upgrade was successful

		// the new API expects us to initiate the reboot:
		err = talosClient.Reboot(tCtx, client.WithRebootMode(machine.RebootRequest_DEFAULT))
		if err != nil {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedReboot,
				fmt.Sprintf("Failed to reboot after upgrade: %v", err),
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}

		if err := r.updateOpStatus(
			ctx,
			NodeOperationPhaseUpgradeVerifying,
			NodeOperationReasonSuccessfulLifecycleClientUpgrade,
			"Node upgraded successfully, proceeding to verifying",
		); err != nil {
			return nil, fmt.Errorf("error updating operation status: %w", err)
		}

	case NodeOperationPhaseUpgradeLegacyUpgrade:
		// no image pre-pull before 1.13 so we *hope* that it finishes in 5 minutes
		ttCtx, cancel := context.WithTimeout(tCtx, 5*time.Minute)
		defer cancel()

		_, err := talosClient.UpgradeWithOptions( // nolint:staticcheck // SA1019 legacy support path
			ttCtx,
			client.WithUpgradeImage(desiredImage),
		)
		if err != nil {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedUpgrade,
				fmt.Sprintf("Failed to start legacy upgrade: %v", err),
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}

		// advance to verifying
		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseUpgradeVerifying,
			NodeOperationReasonSuccessfulLegacyUpgradeRequest,
			"Legacy upgrade requested successfully, proceeding to verifying",
		)
		if err != nil {
			return nil, fmt.Errorf("error updating operation status: %w", err)
		}
		return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil

	case NodeOperationPhaseUpgradeVerifying:
		// check phase timeout
		timedOut := r.hasPhaseTimedOut(node, cluster)
		if timedOut {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonTimedOutVerifyingUpgrade,
				"Timed out while waiting for node to reboot after upgrade",
			)
			if err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return nil, nil
		}

		// wait for the node to reboot
		ok := r.hasNodeRebootedYet(tCtx, talosClient, remoteClient, node)
		if !ok {
			// not rebooted yet, so we requeue to check again later
			return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
		}

		// check that image now matches the desired image
		if !talosVersion.EQ(desiredTalosVersion) ||
			(hasSchematic && talosSchematicID != desiredSchematicID) {
			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeWarning,
				EventReasonFailed,
				EventActionOSUpgrade,
				"Node OS upgrade verification failed: expected version %s (schematic %s), got version %s (schematic %s)",
				desiredTalosVersion,
				desiredSchematicID,
				talosVersion,
				talosSchematicID,
			)
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedUpgrade,
				fmt.Sprintf(
					"Node OS upgrade verification failed: expected version %s (schematic %s), got version %s (schematic %s)",
					desiredTalosVersion,
					desiredSchematicID,
					talosVersion,
					talosSchematicID,
				),
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		// update boot ID so that the apply verify stage can detect its own reboot
		bootID, err := getNodeBootID(tCtx, talosClient)
		if err != nil {
			return nil, fmt.Errorf("failed to get node boot ID after upgrade: %w", err)
		}
		err = r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.BootID = bootID
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to reset node BootID status for upgrade verification: %w",
				err,
			)
		}

		// proceed to uncordon
		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseUpgradeUncordon,
			NodeOperationReasonSuccessfulUpgrade,
			"Node rebooted successfully after upgrade, proceeding to uncordon",
		)
		if err != nil {
			return nil, fmt.Errorf("error updating operation status: %w", err)
		}

		return &ctrl.Result{RequeueAfter: RequeueImmediate}, nil

	case NodeOperationPhaseUpgradeUncordon:
		// finish the upgrade by uncordoning the node again
		err := r.doNodeUncordon(ctx, remoteClient, node)
		if err != nil {
			if err := r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedUncordon,
				fmt.Sprintf("Failed to uncordon node after upgrade: %v", err),
			); err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
		}

		// if we're called as a subroutine of an Apply operation, go back to Applying, otherwise we're done
		if opStatus.Type == NodeOperationTypeApply {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseApplying,
				NodeOperationReasonSuccessfulUpgrade,
				"Node uncordoned successfully after upgrade, proceeding to apply",
			)
			if err != nil {
				return nil, fmt.Errorf("error updating operation status: %w", err)
			}
			return &ctrl.Result{RequeueAfter: RequeueImmediate}, nil
		}

		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseDone,
			NodeOperationReasonSuccessfulUpgrade,
			"Node uncordoned successfully after upgrade, operation complete",
		)
		if err != nil {
			return nil, fmt.Errorf("error updating operation status: %w", err)
		}

	default:
		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseFailed,
			NodeOperationReasonProgramError,
			"Unsupported phase passed to handleNodeUpgrade",
		)
		if err != nil {
			return nil, fmt.Errorf("error updating operation status: %w", err)
		}
		return nil, nil
	}

	return nil, nil
}
