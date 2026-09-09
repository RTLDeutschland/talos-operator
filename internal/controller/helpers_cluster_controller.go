package controller

import (
	"context"
	"fmt"
	"slices"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	. "github.com/RTLDeutschland/talos-operator/internal/constants"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// updateOpStatus bump's the Cluster's operation status with the provided phase, reason, and message.
func (r *ClusterReconciler) updateOpStatus(
	ctx context.Context,
	phase, reason, message string, subOps []talosv1alpha1.ClusterManagedNodeOperationStatus,
) error {
	cluster := ctx.Value(CtxKeyCluster).(*talosv1alpha1.Cluster) // get cluster from context

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// refresh cluster on every attempt to avoid update conflicts
		if err := r.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
			return err
		}

		cluster.Status.Operation.Phase = phase
		cluster.Status.Operation.LastTransitionTime = metav1.Now()
		cluster.Status.Operation.Reason = reason
		cluster.Status.Operation.Message = message
		cluster.Status.Operation.SubOperations = subOps

		return r.Status().Update(ctx, cluster)
	})
	if err != nil {
		return fmt.Errorf("failed to update operation status: %w", err)
	}

	return nil
}

// updateCluster updates the entire Cluster object with retry logic on conflicts.
// The updateFn receives the latest Cluster object and should modify it as needed.
func (r *ClusterReconciler) updateCluster(
	ctx context.Context,
	updateFn func(*talosv1alpha1.Cluster),
) error {
	cluster := ctx.Value(CtxKeyCluster).(*talosv1alpha1.Cluster)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// refresh cluster on every attempt to avoid update conflicts
		if err := r.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
			return err
		}

		// apply the update function
		updateFn(cluster)

		return r.Update(ctx, cluster)
	})
}

// updateClusterStatus updates only the Cluster status with retry logic on conflicts.
// The updateFn receives the latest Cluster object and should modify its status as needed.
func (r *ClusterReconciler) updateClusterStatus(
	ctx context.Context,
	updateFn func(*talosv1alpha1.Cluster),
) error {
	cluster := ctx.Value(CtxKeyCluster).(*talosv1alpha1.Cluster)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// refresh cluster on every attempt to avoid update conflicts
		if err := r.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
			return err
		}

		// apply the update function
		updateFn(cluster)

		return r.Status().Update(ctx, cluster)
	})
}

// listEligibleNodes returns provisioned nodes (ConditionProvisioned == true) for the
// given cluster, sorted with controlplane nodes first and then alphabetically by name
// within each role group.
//
// Pass an optional role (Controlplane or Worker) to restrict the query to that role only.
func (r *ClusterReconciler) listEligibleNodes(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
	role ...string,
) ([]talosv1alpha1.Node, error) {
	if len(role) > 1 {
		panic("listEligibleNodes: at most one role may be specified")
	}

	matchingFields := client.MatchingFields{
		"spec.clusterRef": cluster.Name,
	}
	if len(role) == 1 {
		matchingFields["spec.role"] = role[0]
	}

	nodeList := &talosv1alpha1.NodeList{}
	if err := r.List(
		ctx,
		nodeList,
		client.InNamespace(cluster.Namespace),
		matchingFields,
	); err != nil {
		if len(role) == 1 {
			return nil, fmt.Errorf(
				"error listing %s nodes for cluster %s: %w",
				role[0],
				cluster.Name,
				err,
			)
		}
		return nil, fmt.Errorf("error listing nodes for cluster %s: %w", cluster.Name, err)
	}

	eligible := make([]talosv1alpha1.Node, 0, len(nodeList.Items))
	for _, node := range nodeList.Items {
		if meta.IsStatusConditionTrue(node.Status.Conditions, NodeConditionProvisioned) {
			eligible = append(eligible, node)
		}
	}

	slices.SortFunc(eligible, func(a, b talosv1alpha1.Node) int {
		if a.Spec.Role == Controlplane && b.Spec.Role != Controlplane {
			return -1
		}
		if a.Spec.Role != Controlplane && b.Spec.Role == Controlplane {
			return 1
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})

	return eligible, nil
}

// otherOpsInProgress checks if any of the given nodes have an ongoing operation (i.e. Operation.Phase is not Done or Failed).
func (r *ClusterReconciler) otherOpsInProgress(
	nodes []talosv1alpha1.Node,
) string {
	for _, node := range nodes {
		if node.Status.Operation != nil &&
			(node.Status.Operation.Phase != NodeOperationPhaseDone && node.Status.Operation.Phase != NodeOperationPhaseFailed) {
			return node.Name
		}
	}
	return ""
}
