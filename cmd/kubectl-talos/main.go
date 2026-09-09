package main

import (
	"os"

	"github.com/spf13/cobra"
)

// linker flags:
var (
	Version string
)

type KV map[string]any

var namespace *string = new(string)
var watch *bool = new(bool)

func rootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "kubectl-talos",
		Short:   "kubectl-talos is a kubectl plugin for interacting with Talos cluster resources managed by the Talos operator",
		Version: Version,
	}
	cmd.CompletionOptions.HiddenDefaultCmd = true
	cmd.PersistentFlags().StringVarP(namespace, "namespace", "n", "", "Kubernetes namespace")
	cmd.PersistentFlags().
		BoolVarP(watch, "watch", "w", isTerminal(), "Watch the operation as it progresses")
	cmd.AddCommand(
		provisionNodeCmd(),
		bootstrapNodeCmd(),
		applyNodeCmd(),
		resetNodeCmd(),
		rebootNodeCmd(),
		shutdownNodeCmd(),
		rollingApplyClusterCmd(),
		rollingRebootClusterCmd(),
		k8sUpgradeClusterCmd(),
		kubectlCmd(),
		talosctlCmd(),
		createCmdGroup(),
	)
	return cmd
}

func main() {
	cmd := rootCommand()
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
