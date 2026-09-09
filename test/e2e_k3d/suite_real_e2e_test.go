//go:build e2e

package e2e_k3d_test

import (
	"encoding/json"
	"fmt"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const ClusterVIP = "192.168.22.9"       // change me to fit your own local network, but ensure it's outside DHCP range.
const ClusterFQDNSuffix = "q0.internal" // again, same deal

var _ = Describe("A real-world e2e test", Ordered, func() {
	if !*realE2E {
		return
	}

	ns := &corev1.Namespace{}
	ns.Name = "e2e-test"

	clusterName := "pandora"

	installImagePatch := `{version: v1alpha1, machine: {install: {image: factory.talos.dev/nocloud-installer-secureboot/ce4c980550dd2ab1b17bbf2b08801c7eb59418eafe8f279833297925d67c7515:v1.12.7}}}`
	installImageDowngradePatch := `{version: v1alpha1, machine: {install: {image: factory.talos.dev/nocloud-installer-secureboot/ce4c980550dd2ab1b17bbf2b08801c7eb59418eafe8f279833297925d67c7515:v1.12.6}}}`

	kubernetesVersion := "v1.35.2"
	kubernetesVersionPlusOne := "v1.35.3"

	It("should create the real e2e cluster", func(ctx SpecContext) {
		// create a namespace for the E2E test
		Expect(k3dClient.Create(ctx, ns)).To(Succeed())

		// assemble the cluster
		cluster := &talosv1alpha1.Cluster{}
		cluster.Name = clusterName
		cluster.Namespace = ns.Name
		// CAUTION: use spaces for indentation inside the YAML string
		// vscode: "editor.renderWhitespace": "all"
		cluster.Spec.KubernetesVersion = kubernetesVersion
		cluster.Spec.Domain = ClusterFQDNSuffix
		cluster.Spec.Patches = []string{
			installImagePatch,
			// adjust if needed, this one contains qemu-guest-agent

			// add a hosts entry for the cluster VIP
			// (we're not going to configure our gateway to DNS this for us)
			fmt.Sprintf(
				`{apiVersion: v1alpha1, kind: StaticHostConfig, name: %s, hostnames: ["pandora.%s"]}`,
				ClusterVIP,
				ClusterFQDNSuffix,
			),
		}
		cluster.Spec.ControlPlanePatches = []string{
			fmt.Sprintf(
				`{apiVersion: v1alpha1, kind: Layer2VIPConfig, name: "%s", link: eth0}`,
				ClusterVIP,
			),
		}
		cluster.Spec.Options.ArgoCDSecret.Enabled = lo.ToPtr(true)
		cluster.Spec.Options.ArgoCDSecret.Namespace = "default"

		// create and wait for the operator to pick up the cluster
		Expect(k3dClient.Create(ctx, cluster)).To(Succeed())
		Eventually(func() error {
			if err := k3dClient.Get(
				ctx,
				client.ObjectKeyFromObject(cluster),
				cluster,
			); err != nil {
				return fmt.Errorf("failed to get cluster: %w", err)
			}
			if cluster.Spec.KubernetesVersion == "" {
				return fmt.Errorf("cluster does not have KubernetesVersion set")
			}
			return nil
		}, "10s", "100ms").Should(Succeed())

		// get the IP address map from terraform output
		ipAddressMapRaw, err := ShellOut(
			`tofu -chdir=pve show -json | jq '[.values.root_module.resources[] | select(.type == "proxmox_virtual_environment_vm") | {key: .values.name, value: ([.values.ipv4_addresses[] | .[]] | map(select(startswith("127.") | not)) | first)}] | from_entries'`,
		)
		Expect(err).ToNot(HaveOccurred(), "failed to get IP address map: %v", err)
		ipAddressMap := make(map[string]string)
		Expect(
			json.Unmarshal([]byte(ipAddressMapRaw), &ipAddressMap),
		).To(Succeed(), "failed to unmarshal IP address map: %v", err)

		// create all cluster nodes
		for _, role := range []string{NodeRoleControlplane, NodeRoleWorker} {
			for i := range 3 {
				node := &talosv1alpha1.Node{}
				node.Name = fmt.Sprintf("pandora-%c%d", role[0], i+1)
				node.Annotations = map[string]string{
					// because we are in a challenging network / DNS environment,
					// we set the connect host override annotation so the operator contacts
					// the nodes directly via IP address:
					NodeAnnotationConnectHostOverride: ipAddressMap[node.Name],
				}
				node.Namespace = ns.Name
				node.Spec.ClusterRef = cluster.Name
				node.Spec.Role = role
				Expect(k3dClient.Create(ctx, node)).To(Succeed())
			}
		}

		// wait for cluster nodes to be populated
		Eventually(func() error {
			nodes := &talosv1alpha1.NodeList{}
			err := k3dClient.List(ctx, nodes, &client.ListOptions{Namespace: ns.Name})
			if err != nil {
				return fmt.Errorf("failed to list nodes: %w", err)
			}
			for _, node := range nodes.Items {
				if node.Status.KubernetesVersions == nil {
					return fmt.Errorf("node %s does not have KubernetesVersions set", node.Name)
				}
			}
			return nil
		}, "10s", "500ms").Should(Succeed())
	})

	It("should have .GetConnectHost() and .GetFQDN() working correctly", func(ctx SpecContext) {
		cluster := &talosv1alpha1.Cluster{}
		Expect(
			k3dClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: clusterName}, cluster),
		).To(Succeed())
		nodes := &talosv1alpha1.NodeList{}
		Expect(k3dClient.List(ctx, nodes, &client.ListOptions{Namespace: ns.Name})).To(Succeed())

		for _, node := range nodes.Items {
			Expect(
				node.GetConnectHost(cluster),
			).To(Equal(node.Annotations[NodeAnnotationConnectHostOverride]))
			Expect(
				node.GetFQDN(cluster),
			).To(Equal(fmt.Sprintf("%s.%s", node.Name, ClusterFQDNSuffix)))
		}
	})

	It("should provision the real e2e nodes", func(ctx SpecContext) {
		// get the IP address map from terraform output
		ipAddressMapRaw, err := ShellOut(
			`tofu -chdir=pve show -json | jq '[.values.root_module.resources[] | select(.type == "proxmox_virtual_environment_vm") | {key: .values.name, value: ([.values.ipv4_addresses[] | .[]] | map(select(startswith("127.") | not)) | first)}] | from_entries'`,
		)
		Expect(err).ToNot(HaveOccurred(), "failed to get IP address map: %v", err)
		ipAddressMap := make(map[string]string)
		Expect(
			json.Unmarshal([]byte(ipAddressMapRaw), &ipAddressMap),
		).To(Succeed(), "failed to unmarshal IP address map: %v", err)

		// begin provisioning of all nodes
		for _, role := range []string{NodeRoleControlplane, NodeRoleWorker} {
			for i := range 3 {
				node := &talosv1alpha1.Node{}
				node.Name = fmt.Sprintf("pandora-%c%d", role[0], i+1)
				node.Namespace = ns.Name
				Expect(k3dClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())

				op := &talosv1alpha1.NodeOperation{}
				op.Type = "Provision"
				op.Provision.Address = ipAddressMap[node.Name]
				node.Operation = op
				Expect(k3dClient.Update(ctx, node)).To(Succeed())
			}
		}

		// wait for the provisioning operation to end
		Eventually(func() error {
			nodes := &talosv1alpha1.NodeList{}
			err := k3dClient.List(ctx, nodes, &client.ListOptions{Namespace: ns.Name})
			if err != nil {
				return fmt.Errorf("failed to list nodes: %w", err)
			}
			for _, node := range nodes.Items {
				if node.Status.Operation == nil {
					return fmt.Errorf("node %s does not have Operation set", node.Name)
				}
				if !meta.IsStatusConditionTrue(node.Status.Conditions, "Provisioned") {
					return fmt.Errorf("node %s is not marked as Provisioned true", node.Name)
				}
				readyCondition := meta.FindStatusCondition(node.Status.Conditions, "Ready")
				if readyCondition == nil {
					return fmt.Errorf("node %s does not have Ready condition", node.Name)
				}
				if readyCondition.Status != metav1.ConditionTrue {
					return fmt.Errorf("node %s is not Ready", node.Name)
				}
				if time.Since(readyCondition.LastTransitionTime.Time) < 30*time.Second {
					return fmt.Errorf(
						"node %s was marked Ready true less than 30 seconds ago, might still be stabilizing",
						node.Name,
					)
				}
				// finally, all nodes exiting provisioned should eventually have ConfigInSync true,
				// given the new automatic apply if node needs initial updates
				if !meta.IsStatusConditionTrue(node.Status.Conditions, "ConfigInSync") {
					return fmt.Errorf("node %s does not have ConfigInSync true", node.Name)
				}
			}
			return nil
		}, "15m", "2s").Should(Succeed()) // change this if needed, occasionally takes quite long
	})

	It("should eventually bootstrap the cluster", func(ctx SpecContext) {
		// wait for cluster to be marked as Bootstrapped true
		Eventually(func() error {
			cluster := &talosv1alpha1.Cluster{}
			if err := k3dClient.Get(
				ctx,
				client.ObjectKey{Namespace: ns.Name, Name: clusterName},
				cluster,
			); err != nil {
				return fmt.Errorf("failed to get cluster: %w", err)
			}
			if !meta.IsStatusConditionTrue(cluster.Status.Conditions, "Bootstrapped") {
				return fmt.Errorf("cluster is not marked as Bootstrapped true")
			}
			return nil
		}, "3m", "2s").Should(Succeed())
	})

	It("should eventually have Kubernetes ready", func(ctx SpecContext) {
		// seeing as we're using the default flannel CNI, no extra steps needed

		// wait for cluster to be marked as Ready true
		Eventually(func() error {
			nodes := &talosv1alpha1.NodeList{}
			if err := k3dClient.List(
				ctx,
				nodes,
				&client.ListOptions{Namespace: ns.Name},
			); err != nil {
				return fmt.Errorf("failed to list nodes: %w", err)
			}
			for _, node := range nodes.Items {
				if !meta.IsStatusConditionTrue(node.Status.Conditions, "KubernetesReady") {
					return fmt.Errorf("node %s is not marked as KubernetesReady true", node.Name)
				}
			}
			return nil
		}, "5m", "2s").Should(Succeed())
	})

	It("should be able to downgrade an individual node's OS", func(ctx SpecContext) {
		node := &talosv1alpha1.Node{}
		node.Name = "pandora-c1"
		node.Namespace = ns.Name
		Expect(k3dClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())

		beforeOperationInit := time.Now()

		// this actually tests two different things, specifically if when an Apply is triggered,
		// that the node resource is still refreshed before the operation starts.
		//
		// this is important because otherwise the operation would end up skipped, because it
		// still thinks the config is in sync.
		node.Spec.Patches = []string{installImageDowngradePatch}
		node.Operation = &talosv1alpha1.NodeOperation{
			Type: NodeOperationTypeApply,
		}
		Expect(k3dClient.Update(ctx, node)).To(Succeed())

		// wait for individual node downgrade to succeed
		Eventually(func() error {
			if err := k3dClient.Get(
				ctx,
				client.ObjectKeyFromObject(node),
				node,
			); err != nil {
				return fmt.Errorf("failed to get node: %w", err)
			}
			if node.Status.Operation == nil {
				return fmt.Errorf("node does not have Operation set")
			}
			if node.Status.Operation.LastTransitionTime.Time.Before(beforeOperationInit) {
				return fmt.Errorf("node operation has not yet been updated")
			}
			if node.Status.Operation.Type != NodeOperationTypeApply {
				return fmt.Errorf("node operation type is not Apply")
			}
			if node.Status.Operation.Phase != NodeOperationPhaseDone {
				return fmt.Errorf("node operation state is not Done")
			}
			if !meta.IsStatusConditionTrue(node.Status.Conditions, "ConfigInSync") {
				return fmt.Errorf("node does not have ConfigInSync true")
			}
			return nil
		}, "5m", "2s").Should(Succeed())

		// clean up the patches
		node.Spec.Patches = nil
		Expect(k3dClient.Update(ctx, node)).To(Succeed())
	})

	It("should be able to do a rolling cluster downgrade", func(ctx SpecContext) {
		// patch the cluster's install image patch
		cluster := &talosv1alpha1.Cluster{}
		Expect(
			k3dClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: clusterName}, cluster),
		).To(Succeed())
		cluster.Spec.Patches[0] = installImageDowngradePatch
		Expect(k3dClient.Update(ctx, cluster)).To(Succeed())

		// wait for all nodes to indicate ConfigInSync = False
		Eventually(func() error {
			nodes := &talosv1alpha1.NodeList{}
			if err := k3dClient.List(
				ctx,
				nodes,
				&client.ListOptions{Namespace: ns.Name},
			); err != nil {
				return fmt.Errorf("failed to list nodes: %w", err)
			}
			for _, node := range nodes.Items {
				// skip c1 cause we messed with it earlier, that one will be in sync already
				if node.Name == "pandora-c1" {
					continue
				}
				if meta.IsStatusConditionTrue(node.Status.Conditions, "ConfigInSync") {
					return fmt.Errorf("node %s still has ConfigInSync true", node.Name)
				}
			}
			return nil
		}, "30s", "1s").Should(Succeed())

		// start the RollingApply and wait on it
		beforeOperationInit := time.Now()
		Expect(
			k3dClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: clusterName}, cluster),
		).To(Succeed())
		cluster.Operation = &talosv1alpha1.ClusterOperation{
			Type: ClusterOperationTypeRollingApply,
		}
		Expect(k3dClient.Update(ctx, cluster)).To(Succeed())

		Eventually(func() error {
			if err := k3dClient.Get(
				ctx,
				client.ObjectKeyFromObject(cluster),
				cluster,
			); err != nil {
				return fmt.Errorf("failed to get cluster: %w", err)
			}
			if cluster.Status.Operation == nil {
				return fmt.Errorf("cluster does not have Operation set")
			}
			if cluster.Status.Operation.LastTransitionTime.Time.Before(beforeOperationInit) {
				return fmt.Errorf("cluster operation has not yet been updated")
			}
			if cluster.Status.Operation.Type != ClusterOperationTypeRollingApply {
				return fmt.Errorf("cluster operation type is not RollingApply")
			}
			if cluster.Status.Operation.Phase != ClusterOperationPhaseDone {
				return fmt.Errorf("cluster operation state is not Done")
			}
			return nil
		}, "20m", "1s").Should(Succeed())
	})

	It(
		"should be able to do a rolling cluster upgrade via maintenance window",
		func(ctx SpecContext) {
			beforeOperationInit := time.Now()

			// patch the cluster's install image patch & enable maintenance window
			cluster := &talosv1alpha1.Cluster{}
			Expect(
				k3dClient.Get(
					ctx,
					client.ObjectKey{Namespace: ns.Name, Name: clusterName},
					cluster,
				),
			).To(Succeed())
			cluster.Spec.Patches[0] = installImagePatch
			cluster.Spec.Options.MaintenanceWindow.Enabled = lo.ToPtr(true)
			cluster.Spec.Options.MaintenanceWindow.Cron = "0 * * * *" // 24/7 window, permanently enabled
			Expect(k3dClient.Update(ctx, cluster)).To(Succeed())

			// wait for all nodes to indicate ConfigInSync = False
			Eventually(func() error {
				nodes := &talosv1alpha1.NodeList{}
				if err := k3dClient.List(
					ctx,
					nodes,
					&client.ListOptions{Namespace: ns.Name},
				); err != nil {
					return fmt.Errorf("failed to list nodes: %w", err)
				}
				for _, node := range nodes.Items {
					if meta.IsStatusConditionTrue(node.Status.Conditions, "ConfigInSync") {
						return fmt.Errorf("node %s still has ConfigInSync true", node.Name)
					}
				}
				return nil
			}, "30s", "1s").Should(Succeed())

			// wait for RollingApply to start and end on its own
			Eventually(func() error {
				if err := k3dClient.Get(
					ctx,
					client.ObjectKeyFromObject(cluster),
					cluster,
				); err != nil {
					return fmt.Errorf("failed to get cluster: %w", err)
				}
				if cluster.Status.Operation == nil {
					return fmt.Errorf("cluster does not have Operation set")
				}
				if cluster.Status.Operation.LastTransitionTime.Time.Before(beforeOperationInit) {
					return fmt.Errorf("cluster operation has not yet been updated")
				}
				if cluster.Status.Operation.Type != ClusterOperationTypeRollingApply {
					return fmt.Errorf("cluster operation type is not RollingApply")
				}
				if cluster.Status.Operation.Phase != ClusterOperationPhaseDone {
					return fmt.Errorf("cluster operation state is not Done")
				}
				return nil
			}, "20m", "1s").Should(Succeed())
		},
	)

	It("should be able to update Kubernetes via maintenance window", func(ctx SpecContext) {
		beforeOperationInit := time.Now()
		cluster := &talosv1alpha1.Cluster{}
		Expect(
			k3dClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: clusterName}, cluster),
		).To(Succeed())
		cluster.Spec.KubernetesVersion = kubernetesVersionPlusOne
		Expect(k3dClient.Update(ctx, cluster)).To(Succeed())

		Eventually(func() error {
			if err := k3dClient.Get(
				ctx,
				client.ObjectKeyFromObject(cluster),
				cluster,
			); err != nil {
				return fmt.Errorf("failed to get cluster: %w", err)
			}
			if cluster.Status.Operation == nil {
				return fmt.Errorf("cluster does not have Operation set")
			}
			if cluster.Status.Operation.LastTransitionTime.Time.Before(beforeOperationInit) {
				return fmt.Errorf("cluster operation has not yet been updated")
			}
			if cluster.Status.Operation.Type != ClusterOperationTypeKubernetesUpgrade {
				return fmt.Errorf("cluster operation type is not KubernetesUpgrade")
			}
			if cluster.Status.Operation.Phase != NodeOperationPhaseDone {
				return fmt.Errorf("cluster operation state is not Done")
			}
			return nil
		}, "20m", "1s").Should(Succeed())
	})

	It("should have generated cluster secrets by now", func(ctx SpecContext) {
		secretsThatShouldExist := []client.ObjectKey{
			{Name: clusterName + "-kubeconfig", Namespace: ns.Name},
			{Name: clusterName + "-talosconfig", Namespace: ns.Name},
			{Name: fmt.Sprintf("%s-%s-argocd-cluster", clusterName, ns.Name), Namespace: "default"},
		}
		for _, key := range secretsThatShouldExist {
			secret := &corev1.Secret{}
			err := k3dClient.Get(ctx, key, secret)
			Expect(
				err,
			).ToNot(HaveOccurred(), "failed to get secret %s/%s: %v", key.Namespace, key.Name, err)
			Expect(secret).ToNot(BeNil())
			Expect(secret.Data).ToNot(BeEmpty())
		}
	})

})
