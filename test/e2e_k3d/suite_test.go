//go:build e2e

// Package e2e_k3d_test contains end-to-end tests for the talos-operator,
// specifically just testing controller logic inside k3d without any other external dependencies
// like Talos nodes.
package e2e_k3d_test

import (
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"sort"
	"testing"

	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	crClient "sigs.k8s.io/controller-runtime/pkg/client"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
)

var realE2E = flag.Bool("real-e2e", false, "run real-world e2e tests against actual Talos VMs")

// TestE2EK3d is the entry point for the e2e tests in this package.
func TestE2EK3d(t *testing.T) {
	flag.Parse()
	RegisterFailHandler(Fail)
	RunSpecs(t, "Talos Operator E2E K3d Suite")
}

// Shell is a helper function to run shell commands and print their output to the GinkgoWriter.
func Shell(cmd string) error {
	execCmd := exec.Command("/usr/bin/env", "bash", "-e", "-c", cmd)
	execCmd.Stdout = GinkgoWriter
	execCmd.Stderr = GinkgoWriter
	err := execCmd.Run()
	if err != nil {
		return fmt.Errorf("failed to run command `%s`: %w", cmd, err)
	}

	return nil
}

// ShellOut is a helper function to run shell commands and return their output as a string.
func ShellOut(cmd string) (string, error) {
	execCmd := exec.Command("/usr/bin/env", "bash", "-e", "-c", cmd)
	execCmd.Stderr = GinkgoWriter
	output, err := execCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run command `%s`: %w", cmd, err)
	}

	return string(output), nil
}

var k3dClient crClient.Client
var k3dClientSet *kubernetes.Clientset
var suiteFailed bool

var _ = AfterEach(func() {
	if CurrentSpecReport().Failed() {
		suiteFailed = true
	}
})

var _ = BeforeSuite(func(ctx SpecContext) {
	// run cleanups first in case a previous test suite failed
	_ = cleanupSuite(ctx)

	// set up k3d
	Expect(Shell("k3d registry create talos-operator-e2e -p 5543")).To(Succeed())
	Expect(
		Shell(
			`k3d cluster create talos-operator-e2e --no-lb --k3s-arg "--disable=traefik@server:0" --agents=1 --kubeconfig-update-default=false --kubeconfig-switch-context=false --registry-use=talos-operator-e2e`,
		),
	).To(Succeed())
	Eventually(
		Shell(`KUBECONFIG=$(k3d kubeconfig write talos-operator-e2e) kubectl cluster-info`),
		"2m",
		"500ms",
	).Should(Succeed())

	// initialize k3d client
	kubeconfigData, err := exec.Command("k3d", "kubeconfig", "get", "talos-operator-e2e").Output()
	Expect(err).ToNot(HaveOccurred(), "failed to get kubeconfig from k3d: %v", err)

	kubeconfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	Expect(err).ToNot(HaveOccurred(), "failed to create rest config from kubeconfig: %v", err)

	k3dClient, err = crClient.New(kubeconfig, crClient.Options{})
	Expect(err).ToNot(HaveOccurred(), "failed to create k3d client: %v", err)
	Expect(k3dClient).ToNot(BeNil(), "k3d client should not be nil")

	err = talosv1alpha1.AddToScheme(k3dClient.Scheme())
	Expect(err).ToNot(HaveOccurred(), "failed to add talosv1alpha1 to scheme: %v", err)

	// initialize clientset for gathering logs in AfterSuite
	k3dClientSet, err = kubernetes.NewForConfig(kubeconfig)
	Expect(err).ToNot(HaveOccurred(), "failed to create k3d clientset: %v", err)
	Expect(k3dClientSet).ToNot(BeNil(), "k3d clientset should not be nil")

	// deploy the operator to k3d
	Expect(Shell(`just docker-build`)).To(Succeed())
	Expect(Shell(`docker tag controller:latest localhost:5543/controller:latest`)).To(Succeed())
	Expect(Shell(`docker push localhost:5543/controller:latest`)).To(Succeed())
	Expect(
		Shell(`KUBECONFIG=$(k3d kubeconfig write talos-operator-e2e) kubectl apply -k .`),
	).To(Succeed())

	if *realE2E {
		// download qcow2 image once, we don't clean up this terraform
		Expect(Shell(`tofu -chdir=pve_init init`)).To(Succeed())
		// terraform provider is flaky, try three times
		Expect(func() error {
			var err error
			for range 3 {
				err = Shell(`tofu -chdir=pve_init apply -auto-approve`)
				if err == nil {
					break
				}
			}
			return err
		}()).To(Succeed())

		// create VMs
		Expect(Shell(`tofu -chdir=pve init`)).To(Succeed())
		Expect(func() error {
			var err error
			for range 3 {
				err = Shell(`tofu -chdir=pve apply -auto-approve`)
				if err == nil {
					break
				}
			}
			return err
		}()).To(Succeed())

		// patch operator deployment with the relevant host aliases
		deployment := &appsv1.Deployment{}
		deployment.Name = "talos-operator-controller-manager"
		deployment.Namespace = "talos-operator-system"
		Expect(
			k3dClient.Get(ctx, crClient.ObjectKeyFromObject(deployment), deployment),
		).To(Succeed())
		aliases := []corev1.HostAlias{}
		aliases = append(aliases, corev1.HostAlias{
			IP:        ClusterVIP,
			Hostnames: []string{"pandora." + ClusterFQDNSuffix},
		})
		deployment.Spec.Template.Spec.HostAliases = aliases
		Expect(k3dClient.Update(ctx, deployment)).To(Succeed())
	}

	// wait for operator to be running and ready
	Eventually(func() error {
		deployment := &appsv1.Deployment{}
		deployment.Name = "talos-operator-controller-manager"
		deployment.Namespace = "talos-operator-system"
		err := k3dClient.Get(ctx, crClient.ObjectKeyFromObject(deployment), deployment)
		if err != nil {
			return fmt.Errorf("failed to get deployment: %w", err)
		}
		if deployment.Status.ReadyReplicas < 1 {
			return fmt.Errorf("deployment is not ready")
		}
		if deployment.Status.UpdatedReplicas < 1 {
			return fmt.Errorf("deployment is not updated")
		}
		return nil
	}, "1m", "100ms").Should(Succeed())
})

// cleanupSuite removes any infrastructure created for the tests.
func cleanupSuite(ctx SpecContext) error {
	errs := make([]error, 0, 3)

	err := Shell("k3d cluster delete talos-operator-e2e")
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to delete k3d cluster: %w", err))
	}
	err = Shell("k3d registry delete talos-operator-e2e")
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to delete k3d registry: %w", err))
	}

	if *realE2E {
		// terraform provider is flaky, try three times
		err = func() error {
			var err error
			for range 3 {
				err = Shell(`tofu -chdir=pve destroy -auto-approve`)
				if err == nil {
					break
				}
			}
			return err
		}()
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to destroy pve infrastructure: %w", err))
		}
	}

	return errors.Join(errs...)
}

var _ = AfterSuite(func(ctx SpecContext) {
	if suiteFailed && k3dClientSet != nil {
		// add operator pod logs to the test report for debugging
		pods := &corev1.PodList{}
		Expect(
			k3dClient.List(ctx, pods, crClient.InNamespace("talos-operator-system")),
		).To(Succeed())
		for _, pod := range pods.Items {
			logs, err := k3dClientSet.CoreV1().
				Pods(pod.Namespace).
				GetLogs(pod.Name, &corev1.PodLogOptions{}).
				DoRaw(ctx)
			if err != nil {
				fmt.Fprintf(GinkgoWriter, "failed to get logs for pod %s: %v\n", pod.Name, err)
				continue
			}
			AddReportEntry(pod.Name+".log", string(logs))
		}

		// dump clusters
		clusterList := &talosv1alpha1.ClusterList{}
		Expect(k3dClient.List(ctx, clusterList)).To(Succeed())
		clusters := clusterList.Items
		sort.Slice(clusters, func(i, j int) bool {
			return clusters[i].Name < clusters[j].Name
		})
		for _, cluster := range clusters {
			cluster.ObjectMeta.ManagedFields = nil // reduce noise
			data, err := yaml.Marshal(cluster)
			if err != nil {
				fmt.Fprintf(GinkgoWriter, "failed to marshal cluster %s: %v\n", cluster.Name, err)
				continue
			}
			AddReportEntry(cluster.Name+".yaml", string(data))
		}

		// dump nodes
		nodeList := &talosv1alpha1.NodeList{}
		Expect(k3dClient.List(ctx, nodeList)).To(Succeed())
		nodes := nodeList.Items
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].Name < nodes[j].Name
		})
		for _, node := range nodes {
			node.ObjectMeta.ManagedFields = nil // reduce noise
			data, err := yaml.Marshal(node)
			if err != nil {
				fmt.Fprintf(GinkgoWriter, "failed to marshal node %s: %v\n", node.Name, err)
				continue
			}
			AddReportEntry(node.Name+".yaml", string(data))
		}
	} else {
		// only clean up if suite did not fail
		err := cleanupSuite(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to clean up suite: %v", err)
	}
})
