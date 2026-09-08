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

func (r *NodeReconciler) handleKubernetesComponentUpgrade(
	ctx context.Context,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	log := logz.New(ctx, "node.kubernetesComponentUpgrade")

	if r.isOperationComplete(node) {
		return nil, nil
	}
	if node.Status.Operation.Type != NodeOperationTypeKubernetesComponentUpgrade {
		return nil, nil
	}

	// check if other operations are in progress on the cluster
	otherOpsInProgress, err := r.areOtherOperationsInProgress(ctx, cluster, node)
	if err != nil {
		return nil, fmt.Errorf("error checking for other operations in progress: %w", err)
	}
	if otherOpsInProgress {
		return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}

	// grab a mutex
	mutex := r.Mutexes.For(cluster.Namespace + ":" + cluster.Name)
	hasMutex := mutex.TryLock()
	if !hasMutex {
		// another operation is ongoing for this cluster
		return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
	}
	defer mutex.Unlock()

	var opStatus = node.Status.Operation

	// init various clients
	tCtx, talosClient, err := getTalosClient(ctx, r.Client, node, cluster)
	if err != nil {
		return nil, err
	}
	defer talosClient.Close()

	// create a kubernetes client to determine some cluster facts
	remoteClient, err := getKubernetesClient(ctx, r.Client, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes client for k8s upgrade: %w", err)
	}

	// detect if the cluster has longhorn
	var hasLonghorn bool = false
	driver, err := remoteClient.StorageV1().CSIDrivers().Get(
		ctx, "driver.longhorn.io", metav1.GetOptions{},
	)
	if err == nil && driver != nil {
		hasLonghorn = true
	}

	// generate the machine config for all following phases
	opts := &GenerateMachineConfigOptions{
		WithoutRTLLabel:                !r.FeatureFlags.EnableRTLNodeLabel,
		WithoutCrossNamespacePatchRefs: !r.FeatureFlags.EnableCrossNamespacePatchRefs,
	}
	machineCfg, _, err := GenerateMachineConfig(ctx, r.Client, node, opts)
	if err != nil {
		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseFailed,
			NodeReasonConfigGenerationFailed,
			fmt.Sprintf(
				"Failed to generate machine config for Kubernetes component upgrade: %v",
				err,
			),
		)
		if err != nil {
			return nil, err
		}
		return nil, nil
	}

	switch opStatus.Phase {
	case NodeOperationPhasePending:
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonStarted,
			EventActionKubernetesComponentUpgrade,
			"Kubernetes %s upgrade started", opStatus.KubernetesComponentUpgrade.ComponentName,
		)

		if !meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionConfigInSync) {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonConfigOutOfSync,
				"Node configuration is out of sync, cannot perform Kubernetes upgrade",
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		// check if component is valid
		switch opStatus.KubernetesComponentUpgrade.ComponentName {
		case KubernetesComponentAPIServer,
			KubernetesComponentControllerManager,
			KubernetesComponentScheduler,
			KubernetesComponentKubelet:
			// valid component
		default:
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonUnknownComponent,
				fmt.Sprintf(
					"Unknown Kubernetes component %q for upgrade",
					opStatus.KubernetesComponentUpgrade.ComponentName,
				),
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		// check if component is valid for this node
		if node.Spec.Role == Worker {
			switch opStatus.KubernetesComponentUpgrade.ComponentName {
			case KubernetesComponentAPIServer,
				KubernetesComponentControllerManager,
				KubernetesComponentScheduler:
				err = r.updateOpStatus(
					ctx,
					NodeOperationPhaseFailed,
					NodeOperationReasonInvalidComponentForRole,
					fmt.Sprintf(
						"Kubernetes component %q cannot be upgraded on worker nodes",
						opStatus.KubernetesComponentUpgrade.ComponentName,
					),
				)
				if err != nil {
					return nil, err
				}
				return nil, nil
			}
		}

		// check if component is enabled on this node
		enabled := true
		switch opStatus.KubernetesComponentUpgrade.ComponentName {
		case KubernetesComponentControllerManager:
			enabled = machineCfg.K8sControllerManagerConfig().Enabled()
		case KubernetesComponentScheduler:
			enabled = machineCfg.K8sSchedulerConfig().Enabled()
		}
		if !enabled {
			// update our tracking information immediately
			err = r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
				n.Status.KubernetesVersions.SetComponentVersion(
					opStatus.KubernetesComponentUpgrade.ComponentName,
					cluster.Spec.KubernetesVersion,
				)
			})
			if err != nil {
				return nil, fmt.Errorf(
					"failed to update node status with new Kubernetes %s version for upgrade: %w",
					opStatus.KubernetesComponentUpgrade.ComponentName,
					err,
				)
			}
			// and skip everything else
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseDone,
				NodeOperationReasonSkippedDisabledComponent,
				fmt.Sprintf(
					"Kubernetes component %q is disabled on this node, skipping upgrade",
					opStatus.KubernetesComponentUpgrade.ComponentName,
				),
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		var nextPhase = NodeOperationPhaseApplying
		// kubelet updates + longhorn don't play nice, so we need to drain the node before upgrading kubelet
		if hasLonghorn &&
			opStatus.KubernetesComponentUpgrade.ComponentName == KubernetesComponentKubelet {
			nextPhase = NodeOperationPhaseDraining
			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeNormal,
				EventReasonStarted,
				EventActionApplyDrain,
				"Longhorn detected in cluster, upgrading kubelet via drain & reboot instead of in-place upgrade",
			)
		}

		// bump the target version
		err = r.updateNodeStatus(ctx, func(n *talosv1alpha1.Node) {
			n.Status.KubernetesVersions.SetComponentVersion(
				opStatus.KubernetesComponentUpgrade.ComponentName,
				cluster.Spec.KubernetesVersion,
			)
		})
		if err != nil {
			return nil, fmt.Errorf(
				"failed to update node status with new Kubernetes %s version for upgrade: %w",
				opStatus.KubernetesComponentUpgrade.ComponentName,
				err,
			)
		}
		err = r.updateOpStatus(
			ctx,
			nextPhase,
			OperationReasonStarted,
			fmt.Sprintf(
				"Upgrading Kubernetes component %q to version %q",
				opStatus.KubernetesComponentUpgrade.ComponentName,
				cluster.Spec.KubernetesVersion,
			),
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
		// we know that generating a machine config now, with the updated status, will update that component.
		data, err := machineCfg.Bytes()
		if err != nil {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeReasonConfigSerializationFailed,
				fmt.Sprintf(
					"Failed to serialize machine config for Kubernetes component upgrade: %v",
					err,
				),
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		// this usually means no_reboot, but we leave it to Talos' best judgement.
		applyMode := machine.ApplyConfigurationRequest_AUTO
		if opStatus.KubernetesComponentUpgrade.ComponentName == KubernetesComponentKubelet &&
			hasLonghorn {
			// take no prisoners with longhorn
			// (besides, we already drained the node)
			applyMode = machine.ApplyConfigurationRequest_REBOOT
		}

		req := &machine.ApplyConfigurationRequest{
			Mode: applyMode,
			Data: data,
		}
		_, err = talosClient.ApplyConfiguration(tCtx, req)
		if err != nil {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedApply,
				fmt.Sprintf(
					"Failed to apply configuration for Kubernetes component upgrade: %v",
					err,
				),
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseVerifying,
			NodeOperationReasonSuccessfulApply,
			"Kubernetes component upgrade applied, verifying upgrade",
		)
		if err != nil {
			return nil, err
		}

	case NodeOperationPhaseVerifying:
		// handle timeouts
		phaseTimeout, err := cluster.Spec.Options.GetPhaseTimeout()
		if err != nil {
			r.Recorder.Eventf(
				node,
				nil,
				corev1.EventTypeWarning,
				EventReasonValidationError,
				EventActionKubernetesComponentUpgrade,
				"Invalid phase timeout duration, using default: %v",
				err,
			)
		}
		if time.Since(opStatus.LastTransitionTime.Time) > phaseTimeout {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonTimedOutVerifyingUpgrade,
				"Kubernetes component upgrade verification timed out",
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		// figure out the target image of the component
		var targetImage string
		var podName string
		switch opStatus.KubernetesComponentUpgrade.ComponentName {
		case KubernetesComponentAPIServer:
			targetImage = machineCfg.K8sAPIServerConfig().Image()
			podName = "kube-apiserver-" + node.Name
		case KubernetesComponentControllerManager:
			targetImage = machineCfg.K8sControllerManagerConfig().Image()
			podName = "kube-controller-manager-" + node.Name
		case KubernetesComponentScheduler:
			targetImage = machineCfg.K8sSchedulerConfig().Image()
			podName = "kube-scheduler-" + node.Name
		case KubernetesComponentKubelet:
			targetImage = machineCfg.K8sKubeletConfig().Image()
		default:
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonUnknownComponent,
				fmt.Sprintf(
					"Unknown Kubernetes component `%s` for upgrade",
					opStatus.KubernetesComponentUpgrade.ComponentName,
				),
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		if targetImage == "" {
			err = r.updateOpStatus(
				ctx,
				NodeOperationPhaseFailed,
				NodeOperationReasonFailedImageValidation,
				fmt.Sprintf(
					"No target image found for Kubernetes component `%s`",
					opStatus.KubernetesComponentUpgrade.ComponentName,
				),
			)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}

		// check non-kubelet upgrades
		if podName != "" {
			pod, err := remoteClient.CoreV1().
				Pods("kube-system").
				Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				log.Debug().
					Err(err).
					Str("pod", podName).
					Msg("Failed to get pod in remote for verification, requeueing")
				return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
			}

			// wait until the pod has the target image and is ready
			var hasTargetImage bool = false
			for _, container := range pod.Spec.Containers {
				if container.Image == targetImage {
					hasTargetImage = true
					break
				}
			}
			if !hasTargetImage {
				// requeue and wait
				log.Debug().
					Str("pod", podName).
					Str("targetImage", targetImage).
					Msg("Waiting for pod to have target image")
				return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
			}

			var ready bool = false
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			if !ready {
				// requeue and wait
				log.Debug().
					Str("pod", podName).
					Msg("Waiting for pod to be ready after image update")
				return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
			}
		}

		// check kubelet upgrade
		if opStatus.KubernetesComponentUpgrade.ComponentName == KubernetesComponentKubelet {
			// get the node object
			k8sNode, err := remoteClient.CoreV1().
				Nodes().
				Get(ctx, node.GetShortName(), metav1.GetOptions{})
			if err != nil {
				// node info is already attached to the logger
				log.Debug().
					Err(err).
					Msg("Failed to get node in remote for verification, requeueing")
				return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
			}

			_, kubeletImageTag, ok := strings.Cut(targetImage, ":")
			if !ok {
				err = r.updateOpStatus(
					ctx,
					NodeOperationPhaseFailed,
					NodeOperationReasonFailedImageValidation,
					fmt.Sprintf("Invalid target image %q for kubelet upgrade", targetImage),
				)
				if err != nil {
					return nil, err
				}
				return nil, nil
			}

			kubeletVersion := k8sNode.Status.NodeInfo.KubeletVersion

			// wait for the two to match
			if kubeletVersion != kubeletImageTag {
				log.Debug().
					Str("current", kubeletVersion).
					Str("target", kubeletImageTag).
					Msg("Waiting for kubelet version to match target")
				return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
			}
		}

		// wait for the Node to be Ready in Kubernetes
		k8sNode, err := remoteClient.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get node %s for verification: %w", node.Name, err)
		}
		ready := false
		for _, cond := range k8sNode.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		if !ready {
			// requeue and wait
			log.Debug().Msg("Waiting for node to be ready after Kubernetes component upgrade")
			return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
		}

		// remember to uncordon the node (if we cordoned it)
		if !meta.IsStatusConditionTrue(
			node.Status.Conditions,
			NodeConditionKubernetesUnschedulable,
		) {
			err = r.doNodeUncordon(ctx, remoteClient, node)
			if err != nil {
				r.Recorder.Eventf(
					node,
					nil,
					corev1.EventTypeWarning,
					EventReasonFailed,
					EventActionKubernetesComponentUpgrade,
					"Failed to uncordon node after Kubernetes component upgrade: %v",
					err,
				)
				// requeue and try again
				log.Debug().Err(err).Msg("Failed to uncordon node, requeueing")
				return &ctrl.Result{RequeueAfter: RequeueStandardDelay}, nil
			}
		}

		// and we're done
		msg := fmt.Sprintf(
			"Kubernetes %s successfully upgraded to version %q",
			opStatus.KubernetesComponentUpgrade.ComponentName,
			cluster.Spec.KubernetesVersion,
		)
		r.Recorder.Eventf(
			node,
			nil,
			corev1.EventTypeNormal,
			EventReasonCompleted,
			EventActionKubernetesComponentUpgrade,
			msg,
		)
		err = r.updateOpStatus(
			ctx,
			NodeOperationPhaseDone,
			OperationReasonCompleted,
			msg,
		)
		if err != nil {
			return nil, err
		}
	}

	return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
}
