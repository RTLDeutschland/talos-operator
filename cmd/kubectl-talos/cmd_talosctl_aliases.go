package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// talosctlNode runs a talosctl command against a given kubernetes nodes.talos.rtl.de
func talosctlNode(cmd *cobra.Command, nodeName string, args ...string) error {
	// init a controller-utils crClient
	crClient, defaultNamespace, err := getKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}
	if *namespace == "" {
		namespace = &defaultNamespace
	}

	// fetch node and cluster data first
	node := &v1alpha1.Node{}
	err = crClient.Get(cmd.Context(), client.ObjectKey{Namespace: *namespace, Name: nodeName}, node)
	if err != nil {
		return fmt.Errorf("failed to get node resource: %w", err)
	}

	cluster := &v1alpha1.Cluster{}
	err = crClient.Get(
		cmd.Context(),
		client.ObjectKey{Namespace: *namespace, Name: node.Spec.ClusterRef},
		cluster,
	)
	if err != nil {
		return fmt.Errorf("failed to get cluster resource: %w", err)
	}

	// fetch talossecret & write to temp file
	talosconfigSecret := &corev1.Secret{}
	err = crClient.Get(
		cmd.Context(),
		client.ObjectKey{Namespace: *namespace, Name: fmt.Sprintf("%s-talosconfig", cluster.Name)},
		talosconfigSecret,
	)
	if err != nil {
		return fmt.Errorf("failed to get talosconfig secret: %w", err)
	}

	talosconfigFile, err := os.CreateTemp("", "talosconfig")
	if err != nil {
		return fmt.Errorf("error creating temp file: %w", err)
	}
	defer os.Remove(talosconfigFile.Name())

	_, err = talosconfigFile.Write(talosconfigSecret.Data["talosconfig"])
	if err != nil {
		return fmt.Errorf("error writing to talosconfig temp file: %w", err)
	}
	err = talosconfigFile.Close()
	if err != nil {
		return fmt.Errorf("error closing talosconfig temp file: %w", err)
	}

	// exec talosctl command
	talosctlNode := node.GetFQDN(cluster)
	talosctlArgs := []string{"-n", talosctlNode}
	talosctlArgs = append(talosctlArgs, args...)

	talosctlCmd := exec.CommandContext(cmd.Context(), "talosctl", talosctlArgs...)
	talosctlCmd.Env = append(os.Environ(), fmt.Sprintf("TALOSCONFIG=%s", talosconfigFile.Name()))
	talosctlCmd.Stdout = os.Stdout
	talosctlCmd.Stderr = os.Stderr
	talosctlCmd.Stdin = os.Stdin
	err = talosctlCmd.Run()
	if err != nil {
		return fmt.Errorf("error executing talosctl command: %w", err)
	}

	return nil
}

func dashboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard NODE",
		Short: "Alias for talosctl <NODE> dashboard",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return talosctlNode(cmd, args[0], "dashboard")
		},
	}
}

func extensionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "extensions NODE",
		Short: "Alias for talosctl <NODE> get extensions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return talosctlNode(cmd, args[0], "get", "extensions")
		},
	}
}
