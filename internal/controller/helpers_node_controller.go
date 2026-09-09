package controller

import (
	"fmt"
	"slices"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"context"

	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	. "github.com/RTLDeutschland/talos-operator/internal/constants"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// updateOpStatus bump's the Node's operation status with the provided phase, reason, and message.
func (r *NodeReconciler) updateOpStatus(
	ctx context.Context,
	phase, reason, message string,
) error {
	node := ctx.Value(CtxKeyNode).(*talosv1alpha1.Node) // get node from context

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// refresh node on every attempt to avoid update conflicts
		if err := r.Get(ctx, client.ObjectKeyFromObject(node), node); err != nil {
			return err
		}

		node.Status.Operation.Phase = phase
		node.Status.Operation.LastTransitionTime = metav1.Now()
		node.Status.Operation.Reason = reason
		node.Status.Operation.Message = message

		return r.Status().Update(ctx, node)
	})
	if err != nil {
		return fmt.Errorf("failed to update operation status: %w", err)
	}

	return nil
}

// updateNode updates the entire Node object with retry logic on conflicts.
// The updateFn receives the latest Node object and should modify it as needed.
func (r *NodeReconciler) updateNode(
	ctx context.Context,
	updateFn func(*talosv1alpha1.Node),
) error {
	node := ctx.Value(CtxKeyNode).(*talosv1alpha1.Node)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// refresh node on every attempt to avoid update conflicts
		if err := r.Get(ctx, client.ObjectKeyFromObject(node), node); err != nil {
			return err
		}

		// apply the update function
		updateFn(node)

		return r.Update(ctx, node)
	})
}

// updateNodeStatus updates only the Node status with retry logic on conflicts.
// The updateFn receives the latest Node object and should modify its status as needed.
func (r *NodeReconciler) updateNodeStatus(
	ctx context.Context,
	updateFn func(*talosv1alpha1.Node),
) error {
	node := ctx.Value(CtxKeyNode).(*talosv1alpha1.Node)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// refresh node on every attempt to avoid update conflicts
		if err := r.Get(ctx, client.ObjectKeyFromObject(node), node); err != nil {
			return err
		}

		// apply the update function
		updateFn(node)

		return r.Status().Update(ctx, node)
	})
}

// isOperationComplete returns true if the Node has no ongoing operation.
func (r *NodeReconciler) isOperationComplete(node *talosv1alpha1.Node) bool {
	if node.Status.Operation == nil {
		return true
	}
	return node.Status.Operation.Phase == NodeOperationPhaseDone ||
		node.Status.Operation.Phase == NodeOperationPhaseFailed
}

// areOtherOperationsInProgress checks if there are other Nodes in the same Cluster with ongoing operations.
//
// Generally, check this first before acquriring the cluster mutex.
//
// This acts as a distributed cluster mutex to avoid concurrent operations on multiple nodes, and also prevents a
// different node operation from starting if a mutex was accidentally or intentionally unlocked too early,
// for instance due to an error or timeout.
func (r *NodeReconciler) areOtherOperationsInProgress(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
	node *talosv1alpha1.Node,
) (bool, error) {
	// list all nodes in the same cluster
	nodeList := &talosv1alpha1.NodeList{}
	err := r.List(
		ctx,
		nodeList,
		client.InNamespace(node.Namespace),
		client.MatchingFields{"spec.clusterRef": cluster.Name},
	)
	if err != nil {
		// in doubt, assume there are other operations in progress
		return true, err
	}

	notInProgressPhases := []string{
		NodeOperationPhasePending,
		NodeOperationPhaseDone,
		NodeOperationPhaseFailed,
	}
	for _, n := range nodeList.Items {
		if node.Name == n.Name {
			continue
		}
		// not not in progress means that there's an operation in progress
		if n.Status.Operation != nil &&
			!slices.Contains(notInProgressPhases, n.Status.Operation.Phase) {
			return true, nil
		}
	}
	return false, nil
}
