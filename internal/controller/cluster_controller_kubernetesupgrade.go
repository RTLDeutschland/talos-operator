package controller

import (
	"context"
	"fmt"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/cosi-project/runtime/pkg/safe"
	k8sresources "github.com/siderolabs/talos/pkg/machinery/resources/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *ClusterReconciler) handleKubernetesUpgrade(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	opStatus := cluster.Status.Operation

	// TODO preflight: check that the requested OCI images & tags exist before updating anything
	// TODO if longhorn installed: verify that longhorn's node-drain-policy is either `block-for-eviction` OR `block-for-eviction-if-contains-last-replica`

	switch opStatus.Phase {
	case ClusterOperationPhasePending:
		controlPlaneNodes, err := r.listEligibleNodes(ctx, cluster, Controlplane)
		if err != nil {
			return nil, err
		}

		workerNodes, err := r.listEligibleNodes(ctx, cluster, Worker)
		if err != nil {
			return nil, err
		}

		allNodes := make([]talosv1alpha1.Node, 0, len(controlPlaneNodes)+len(workerNodes))
		allNodes = append(allNodes, controlPlaneNodes...)
		allNodes = append(allNodes, workerNodes...)

		// check if any other node operations are in progress
		inProgressName := r.otherOpsInProgress(allNodes)
		if inProgressName != "" {
			err = r.updateOpStatus(
				ctx,
				ClusterOperationPhaseFailed,
				"Conflict",
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

		// run preflight checks
		for _, node := range allNodes {
			// ensure all nodes are ready
			if !meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionReady) {
				err = r.updateOpStatus(
					ctx,
					ClusterOperationPhaseFailed,
					"NodeNotReady",
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
					"NodeConfigDriftDetected",
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
		}

		// four stage plan:
		// - update apiserver on all controlplanes
		// - update controller-manager on all controlplanes
		// - update scheduler on all controlplanes
		// - update kubelet on all workers

		// apiserver:
		var subOps []talosv1alpha1.ClusterManagedNodeOperationStatus
		for _, node := range controlPlaneNodes {
			nodeOp := talosv1alpha1.ClusterManagedNodeOperationStatus{
				NodeName: node.Name,
			}
			nodeOp.Phase = ClusterOperationPhasePending
			nodeOp.Type = NodeOperationTypeKubernetesComponentUpgrade
			nodeOp.KubernetesComponentUpgrade.ComponentName = KubernetesComponentAPIServer
			subOps = append(subOps, nodeOp)
		}

		// controller-manager:
		for _, node := range controlPlaneNodes {
			nodeOp := talosv1alpha1.ClusterManagedNodeOperationStatus{
				NodeName: node.Name,
			}
			nodeOp.Phase = ClusterOperationPhasePending
			nodeOp.Type = NodeOperationTypeKubernetesComponentUpgrade
			nodeOp.KubernetesComponentUpgrade.ComponentName = KubernetesComponentControllerManager
			subOps = append(subOps, nodeOp)
		}

		// scheduler:
		for _, node := range controlPlaneNodes {
			nodeOp := talosv1alpha1.ClusterManagedNodeOperationStatus{
				NodeName: node.Name,
			}
			nodeOp.Phase = ClusterOperationPhasePending
			nodeOp.Type = NodeOperationTypeKubernetesComponentUpgrade
			nodeOp.KubernetesComponentUpgrade.ComponentName = KubernetesComponentScheduler
			subOps = append(subOps, nodeOp)
		}

		// kubelet for both controlplanes and workers:
		for _, node := range allNodes {
			nodeOp := talosv1alpha1.ClusterManagedNodeOperationStatus{
				NodeName: node.Name,
			}
			nodeOp.Phase = ClusterOperationPhasePending
			nodeOp.Type = NodeOperationTypeKubernetesComponentUpgrade
			nodeOp.KubernetesComponentUpgrade.ComponentName = KubernetesComponentKubelet
			subOps = append(subOps, nodeOp)
		}

		// start the operation
		err = r.updateOpStatus(
			ctx,
			ClusterOperationPhaseInProgress,
			OperationReasonStarted,
			"Kubernetes upgrade operation has started",
			subOps,
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
		controllerNodes, err := r.listEligibleNodes(ctx, cluster, Controlplane)
		if err != nil {
			return nil, err
		}
		if len(controllerNodes) < 1 {
			err = r.updateOpStatus(
				ctx,
				ClusterOperationPhaseFailed,
				ClusterOperationReasonNoControlPlanes,
				"No control plane nodes found for postflight checks",
				nil,
			)
			if err != nil {
				return nil, fmt.Errorf("error updating cluster operation status: %w", err)
			}
			return nil, nil
		}

		// update kubernetes manifests from Talos
		tCtx, talosClient, err := getTalosClient(ctx, r.Client, &controllerNodes[0], cluster)
		if err != nil {
			return nil, fmt.Errorf("failed to get talos client: %w", err)
		}
		defer talosClient.Close()

		manifestList, err := safe.StateListAll[*k8sresources.Manifest](tCtx, talosClient.COSI)
		if err != nil {
			return nil, fmt.Errorf("error fetching manifests from Talos: %w", err)
		}

		plainManifests := make([]*unstructured.Unstructured, 0, manifestList.Len()*2)
		manifestList.ForEach(func(m *k8sresources.Manifest) {
			for _, singleManifest := range m.TypedSpec().Items {
				plainManifests = append(
					plainManifests,
					&unstructured.Unstructured{Object: singleManifest.Object},
				)
			}
		})

		// apply manifests to cluster using cached Kubernetes client
		clientset, err := getKubernetesClient(ctx, r.Client, cluster)
		if err != nil {
			return nil, fmt.Errorf("failed to get kubernetes client: %w", err)
		}

		// get fresh kubeconfig for creating dynamic client
		kubeconfig, err := getKubeconfig(ctx, r.Client, cluster)
		if err != nil {
			return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
		}

		dynClient, err := dynamic.NewForConfig(kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create dynamic kubernetes client: %w", err)
		}

		// arbitrary manifest apply logic:
		discoveryMapperCache := memory.NewMemCacheClient(clientset.Discovery())
		discoveryMapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryMapperCache)
		for _, obj := range plainManifests {
			// skip empty objects
			if len(obj.Object) == 0 {
				continue
			}

			// skip objects without GVK
			gvk := obj.GroupVersionKind()
			if gvk.Empty() {
				continue
			}

			mapping, err := discoveryMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				return nil, fmt.Errorf("failed to get REST mapping for %s: %w", gvk.String(), err)
			}

			// apply the resource
			applyOpts := metav1.ApplyOptions{
				FieldManager: "talos-operator",
				Force:        true,
			}

			if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
				_, err := dynClient.Resource(mapping.Resource).
					Namespace(obj.GetNamespace()).
					Apply(ctx, obj.GetName(), obj, applyOpts)
				if err != nil {
					return nil, fmt.Errorf(
						"failed to apply namespaced resource %s/%s: %w",
						obj.GetNamespace(),
						obj.GetName(),
						err,
					)
				}
			} else {
				_, err := dynClient.Resource(mapping.Resource).
					Apply(ctx, obj.GetName(), obj, applyOpts)
				if err != nil {
					return nil, fmt.Errorf(
						"failed to apply cluster-scoped resource %s: %w",
						obj.GetName(),
						err,
					)
				}
			}
		}

		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeNormal,
			EventReasonCompleted,
			EventActionKubernetesApply,
			"Kubernetes manifests updated from Talos after upgrade",
		)

		err = r.updateOpStatus(
			ctx,
			ClusterOperationPhaseDone,
			OperationReasonCompleted,
			"Kubernetes upgrade operation has completed successfully",
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf("error updating cluster operation status: %w", err)
		}
	}

	return &ctrl.Result{RequeueAfter: RequeueShortDelay}, nil
}
