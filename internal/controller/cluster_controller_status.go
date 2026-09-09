package controller

import (
	"context"
	"fmt"
	"io"
	"time"
	_ "time/tzdata"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/robfig/cron/v3"
	"github.com/samber/lo"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *ClusterReconciler) updateMaintenanceStatus(cluster *talosv1alpha1.Cluster) bool {
	mw := cluster.Spec.Options.MaintenanceWindow

	// check if the feature is enabled or not, and update NextMaintenanceWindow accordingly:
	mwEnabled := !lo.IsEmpty(mw) && (mw.Enabled == nil || *mw.Enabled)
	if !mwEnabled || mw.Cron == "" || mw.Duration == "" {
		if !cluster.Status.NextMaintenanceWindow.IsZero() {
			cluster.Status.NextMaintenanceWindow = metav1.Time{}
			return true
		}
		return false
	}

	// parse duration:
	dur, err := time.ParseDuration(mw.Duration)
	if err != nil {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionStatusReconcile,
			"Invalid maintenance window duration: %v",
			err,
		)
		if !cluster.Status.NextMaintenanceWindow.IsZero() {
			cluster.Status.NextMaintenanceWindow = metav1.Time{}
			return true
		}
		return false
	}

	// parse schedule:
	p := cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.DowOptional | cron.Descriptor,
	)
	sched, err := p.Parse(mw.Cron)
	if err != nil {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionStatusReconcile,
			"Invalid maintenance window cron expression: %v",
			err,
		)
		if !cluster.Status.NextMaintenanceWindow.IsZero() {
			cluster.Status.NextMaintenanceWindow = metav1.Time{}
			return true
		}
		return false
	}

	// parse timezone (default to UTC)
	tz := mw.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionStatusReconcile,
			"Invalid maintenance window timezone: %v",
			err,
		)
		if !cluster.Status.NextMaintenanceWindow.IsZero() {
			cluster.Status.NextMaintenanceWindow = metav1.Time{}
			return true
		}
		return false
	}

	// update the next maintenance window:
	nowInTz := metav1.Now().In(loc)
	next := metav1.NewTime(sched.Next(nowInTz.Add(-dur)))
	if cluster.Status.NextMaintenanceWindow.IsZero() ||
		!cluster.Status.NextMaintenanceWindow.Equal(&next) {
		cluster.Status.NextMaintenanceWindow = next
		return true
	}

	return false
}

func (r *ClusterReconciler) updateStatus(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
) error {
	clusterNodes, err := r.listEligibleNodes(ctx, cluster)
	if err != nil {
		return fmt.Errorf("error listing nodes for cluster %s: %w", cluster.Name, err)
	}

	// do status checks
	clusterNodesNeedApply := false
	for _, node := range clusterNodes {
		if meta.IsStatusConditionFalse(node.Status.Conditions, NodeConditionConfigInSync) {
			clusterNodesNeedApply = true
			break
		}
	}

	clusterNodesNeedK8sUpdates := false
	for _, node := range clusterNodes {
		if meta.IsStatusConditionFalse(node.Status.Conditions, NodeConditionKubernetesInSync) {
			clusterNodesNeedK8sUpdates = true
			break
		}
	}

	statusUpdate := false

	if clusterNodesNeedApply &&
		!meta.IsStatusConditionFalse(cluster.Status.Conditions, ClusterConditionNodesInSync) {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:    ClusterConditionNodesInSync,
			Status:  metav1.ConditionFalse,
			Reason:  ClusterConditionReasonNodesOutOfSync,
			Message: "One or more cluster nodes have configuration out of sync",
		})
		statusUpdate = true
	}
	if !clusterNodesNeedApply &&
		!meta.IsStatusConditionTrue(cluster.Status.Conditions, ClusterConditionNodesInSync) {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:    ClusterConditionNodesInSync,
			Status:  metav1.ConditionTrue,
			Reason:  ClusterConditionReasonAllNodesInSync,
			Message: "All cluster nodes have configuration in sync",
		})
		statusUpdate = true
	}

	if clusterNodesNeedK8sUpdates &&
		!meta.IsStatusConditionFalse(
			cluster.Status.Conditions,
			ClusterConditionNodesKubernetesUpToDate,
		) {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:    ClusterConditionNodesKubernetesUpToDate,
			Status:  metav1.ConditionFalse,
			Reason:  ClusterConditionReasonNodesK8sUpdateNeeded,
			Message: "One or more cluster nodes need Kubernetes updates",
		})
		statusUpdate = true
	}
	if !clusterNodesNeedK8sUpdates &&
		!meta.IsStatusConditionTrue(
			cluster.Status.Conditions,
			ClusterConditionNodesKubernetesUpToDate,
		) {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:    ClusterConditionNodesKubernetesUpToDate,
			Status:  metav1.ConditionTrue,
			Reason:  ClusterConditionReasonAllNodesK8sUpToDate,
			Message: "All cluster nodes are up to date",
		})
		statusUpdate = true
	}

	// check if cluster is bootstrapped
	controlPlaneNodes := make([]talosv1alpha1.Node, 0, len(clusterNodes))
	for _, node := range clusterNodes {
		if node.Spec.Role == Controlplane {
			controlPlaneNodes = append(controlPlaneNodes, node)
		}
	}
	// iterate over the nodes and check if any of them are bootstrapped, in which case the cluster is bootstrapped
	bootstrapped := false
	for _, node := range controlPlaneNodes {
		tCtx, talosClient, err := getTalosClient(ctx, r.Client, &node, cluster)
		if err != nil {
			return fmt.Errorf("error creating Talos client for cluster status update: %w", err)
		}
		defer talosClient.Close()
		listResp, err := talosClient.LS(tCtx, &machine.ListRequest{Root: "/var/lib/etcd"})
		if err != nil {
			return fmt.Errorf("error listing machines for cluster status update: %w", err)
		}
		nEntries := 0
		for {
			fileInfo, err := listResp.Recv()
			if err == io.EOF {
				break
			} else if err != nil {
				return fmt.Errorf("error receiving file info for cluster status update: %w", err)
			}
			if fileInfo == nil {
				return fmt.Errorf("received nil file info for cluster status update")
			}
			if fileInfo.Name == "/var/lib/etcd" {
				continue
			}
			nEntries++
		}
		if nEntries > 0 {
			bootstrapped = true
			break
		}
	}
	if bootstrapped &&
		!meta.IsStatusConditionTrue(cluster.Status.Conditions, ClusterConditionBootstrapped) {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:    ClusterConditionBootstrapped,
			Status:  metav1.ConditionTrue,
			Reason:  ClusterConditionReasonBootstrapped,
			Message: "Cluster is bootstrapped",
		})
		statusUpdate = true
	}
	if !bootstrapped &&
		!meta.IsStatusConditionFalse(cluster.Status.Conditions, ClusterConditionBootstrapped) {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:    ClusterConditionBootstrapped,
			Status:  metav1.ConditionFalse,
			Reason:  ClusterConditionReasonNotBootstrapped,
			Message: "Cluster is not bootstrapped",
		})
		statusUpdate = true
	}

	// determine the next maintenance window
	maintenanceStatusUpdated := r.updateMaintenanceStatus(cluster)
	statusUpdate = statusUpdate || maintenanceStatusUpdated

	if statusUpdate {
		if err := r.Status().Update(ctx, cluster); err != nil {
			return fmt.Errorf("error updating cluster status: %w", err)
		}
	}

	return nil
}
