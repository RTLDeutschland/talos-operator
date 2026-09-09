//go:build e2e

// This file concerns itself primarily with node reconciler tests.

package e2e_k3d_test

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
)

var _ = Describe("Node reconciler tests", func() {

	It("should populate valid fresh Node status",
		func(ctx SpecContext) {
			// create a namespace for this test
			ns := &corev1.Namespace{}
			ns.Name = "test-node-status"
			Expect(k3dClient.Create(ctx, ns)).To(Succeed())

			// create cluster for nodes to belong to
			cluster := &talosv1alpha1.Cluster{}
			cluster.Name = "test-cluster2"
			cluster.Namespace = ns.Name
			cluster.Spec.Patches = []string{
				"{version: v1alpha1, machine: {install: {image: ghcr.io/siderolabs/talos:v1.12.6}}}", // hooray YAML
			}
			Expect(k3dClient.Create(ctx, cluster)).To(Succeed())

			// refresh cluster until KubernetesVersion is populated
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
			}, "5s", "50ms").Should(Succeed())

			// create a normal cluster layout with valid roles
			for _, role := range []string{NodeRoleControlplane, NodeRoleWorker} {
				for i := range 3 {
					node := &talosv1alpha1.Node{}
					node.Name = fmt.Sprintf("test-%s-%d", role, i+1)
					node.Namespace = ns.Name
					node.Spec.ClusterRef = cluster.Name
					node.Spec.Role = role
					Expect(k3dClient.Create(ctx, node)).To(Succeed())
				}
			}

			// test that a fake role fails
			node := &talosv1alpha1.Node{}
			node.Name = "test-invalid-role"
			node.Namespace = ns.Name
			node.Spec.ClusterRef = cluster.Name
			node.Spec.Role = "invalid-role"
			Expect(k3dClient.Create(ctx, node)).NotTo(Succeed())

			// test that an empty role fails
			node = &talosv1alpha1.Node{}
			node.Name = "test-empty-role"
			node.Namespace = ns.Name
			node.Spec.ClusterRef = cluster.Name
			node.Spec.Role = ""
			Expect(k3dClient.Create(ctx, node)).NotTo(Succeed())

			// TODO(webhook validation): test that invalid clusterRef fails

			// test that all valid nodes are marked as Ready false and Provisioned false
			Eventually(func() error {
				nodes := &talosv1alpha1.NodeList{}
				err := k3dClient.List(ctx, nodes, &client.ListOptions{Namespace: ns.Name})
				if err != nil {
					return fmt.Errorf("failed to list nodes: %w", err)
				}

				for _, node := range nodes.Items {
					if node.Spec.Role == "invalid-role" || node.Spec.Role == "" {
						continue // skip invalid role nodes
					}
					if !meta.IsStatusConditionFalse(node.Status.Conditions, "Ready") {
						return fmt.Errorf("node %s is not marked as Ready false", node.Name)
					}
					if !meta.IsStatusConditionFalse(node.Status.Conditions, "Provisioned") {
						return fmt.Errorf("node %s is not marked as Provisioned false", node.Name)
					}
					if node.Status.EffectiveConfig == "" {
						return fmt.Errorf("node %s does not have EffectiveConfig set", node.Name)
					}
					// test that EffectiveConfig is rendered as multi-doc
					if !strings.Contains(node.Status.EffectiveConfig, "\n---\n") {
						return fmt.Errorf(
							"node %s EffectiveConfig does not contain multiple documents? (should be a hostname document)",
							node.Name,
						)
					}
					if node.Status.KubernetesVersions == nil {
						return fmt.Errorf("node %s does not have KubernetesVersions set", node.Name)
					}
					if node.Status.KubernetesVersions.Kubelet != cluster.Spec.KubernetesVersion {
						return fmt.Errorf(
							"node %s does not have Kubelet version set to cluster KubernetesVersion",
							node.Name,
						)
					}
				}
				return nil
			}, "10s", "50ms").Should(Succeed())
		},
	)

	It("should complain about invalid machine config", func(ctx SpecContext) {
		// create a namespace for this test
		ns := &corev1.Namespace{}
		ns.Name = "test-node-invalid-machine-config"
		Expect(k3dClient.Create(ctx, ns)).To(Succeed())

		// create cluster
		cluster := &talosv1alpha1.Cluster{}
		cluster.Name = "test-cluster-invalid-machine-config"
		cluster.Namespace = ns.Name
		// intentionally no machine install image resulting in an invalid config
		Expect(k3dClient.Create(ctx, cluster)).To(Succeed())

		// create node
		node := &talosv1alpha1.Node{}
		node.Name = "test-node-invalid-machine-config"
		node.Namespace = ns.Name
		node.Spec.ClusterRef = cluster.Name
		node.Spec.Role = NodeRoleControlplane
		Expect(k3dClient.Create(ctx, node)).To(Succeed())

		Eventually(func() error {
			if err := k3dClient.Get(
				ctx,
				client.ObjectKeyFromObject(node),
				node,
			); err != nil {
				return fmt.Errorf("failed to get node: %w", err)
			}
			cond := meta.FindStatusCondition(node.Status.Conditions, "Ready")
			if cond == nil {
				return fmt.Errorf("node does not have Ready condition")
			}
			if cond.Status != "False" {
				return fmt.Errorf("Ready condition is not False")
			}
			if cond.Reason != "ConfigGenerationFailed" {
				return fmt.Errorf("Ready condition reason is not ConfigGenerationFailed")
			}
			return nil
		}, "10s", "100ms").Should(Succeed())
	})

})
