package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"

	"github.com/spf13/cobra"
)

func kubectlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "kubectl CLUSTER -- ARGS...",
		Short:                 "Execute kubectl commands against the given operator-managed Talos cluster",
		Example:               "kubectl talos kubectl -n talos-foo athena -- get nodes",
		Args:                  cobra.MinimumNArgs(2),
		DisableFlagsInUseLine: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cluster := args[0]
		kubectlArgs := args[1:]

		// fetch the kubeconfig for the given cluster
		// and execute kubectl with the provided args
		secretArgs := []string{
			"get",
			"secret",
			cluster + "-kubeconfig",
			"-o",
			"jsonpath={.data.kubeconfig}",
		}
		if *namespace != "" {
			secretArgs = append([]string{"-n", *namespace}, secretArgs...)
		}
		secretCmd := exec.CommandContext(cmd.Context(), "kubectl", secretArgs...)
		kubeconfigBytes64, err := secretCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("error fetching kubeconfig: %w\n%s", err, string(kubeconfigBytes64))
		}

		// create temporary kubeconfig so we can point kubectl at it
		kubeconfigFile, err := os.CreateTemp("", "kubeconfig")
		if err != nil {
			return fmt.Errorf("error creating temp file: %w", err)
		}
		defer os.Remove(kubeconfigFile.Name())

		// decode base64 and write to temp file
		_, err = io.Copy(
			kubeconfigFile,
			base64.NewDecoder(base64.StdEncoding, bytes.NewBuffer(kubeconfigBytes64)),
		)
		if err != nil {
			return fmt.Errorf("error writing kubeconfig: %w", err)
		}
		err = kubeconfigFile.Close()
		if err != nil {
			return fmt.Errorf("error closing kubeconfig file: %w", err)
		}

		// check if the first arg is another kubernetes tool, and launch that instead.
		// as a side effect this also chomps "kubectl" if it's the first argument of the command,
		// which is useful if it's a copy-pasted kubectl like from the Rancher GUI.
		potentialRoots := []string{"kubectl", "k9s", "helm", "cilium"}
		kubectlCommand := "kubectl"
		if slices.Contains(potentialRoots, kubectlArgs[0]) {
			kubectlCommand = kubectlArgs[0]
			kubectlArgs = kubectlArgs[1:]
		}

		// execute kubectl with the provided args and the temp kubeconfig
		kCmd := exec.CommandContext(cmd.Context(), kubectlCommand, kubectlArgs...)
		kCmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigFile.Name())
		kCmd.Stdout = os.Stdout
		kCmd.Stderr = os.Stderr
		kCmd.Stdin = os.Stdin

		err = kCmd.Run()
		_, isExitErr := err.(*exec.ExitError)
		if err != nil && !isExitErr {
			return fmt.Errorf("error running kubectl: %w", err)
		}

		return nil
	}
	return cmd
}
