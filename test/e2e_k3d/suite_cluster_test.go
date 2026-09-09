//go:build e2e

package e2e_k3d_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	crClient "sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
)

var _ = Describe("Cluster reconciler tests", func() {

	It(
		"should automatically generate a cluster secret when creting a Cluster resource without a secretRef",
		func(ctx SpecContext) {
			// create cluster
			cluster := &talosv1alpha1.Cluster{}
			cluster.Name = "test-cluster"
			cluster.Namespace = "default"
			Expect(k3dClient.Create(ctx, cluster)).To(Succeed())

			// refresh cluster to check if required fields were populated
			Eventually(func() error {
				err := k3dClient.Get(ctx, crClient.ObjectKeyFromObject(cluster), cluster)
				if err != nil {
					return fmt.Errorf("failed to get cluster: %w", err)
				}

				if cluster.Spec.KubernetesVersion == "" {
					return fmt.Errorf("kubernetesVersion is not set")
				}
				if cluster.Spec.SecretsRef == "" {
					return fmt.Errorf("secretsRef is not set")
				}

				return nil
			}, "10s", "50ms").Should(Succeed())

			// check that the secret exists (takes some time to generate- secret creation is computationally expensive)
			Eventually(func() error {
				secret := &corev1.Secret{}
				secret.Name = cluster.Spec.SecretsRef
				secret.Namespace = cluster.Namespace
				err := k3dClient.Get(ctx, crClient.ObjectKeyFromObject(secret), secret)
				if err != nil {
					return fmt.Errorf("failed to get secret: %w", err)
				}
				return nil
			}, "10s", "50ms").Should(Succeed())
		},
	)

})
