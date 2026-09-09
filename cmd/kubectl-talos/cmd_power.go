package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

func rebootNodeCmd() *cobra.Command {
	var unsafe bool

	cmd := &cobra.Command{
		Use:   "reboot NODE",
		Short: "Start a Reboot operation on a Talos node",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Long = cmd.Short + "\n\nThis command will instruct the Talos operator to start a reboot" +
		" operation on the given node."

	cmd.Flags().BoolVar(&unsafe, "unsafe", false,
		"Skip Kubernetes drain and uncordon operations, and force-reboot the node")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runPowerCmd(cmd, args[0], "Reboot", unsafe)
	}
	return cmd
}

func shutdownNodeCmd() *cobra.Command {
	var unsafe bool

	cmd := &cobra.Command{
		Use:   "shutdown NODE",
		Short: "Start a Shutdown operation on a Talos node",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Long = cmd.Short + "\n\nThis command will instruct the Talos operator to start a shutdown" +
		" operation on the given node."

	cmd.Flags().BoolVar(&unsafe, "unsafe", false,
		"Skip Kubernetes drain and uncordon operations")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runPowerCmd(cmd, args[0], "Shutdown", unsafe)
	}
	return cmd
}

func runPowerCmd(cmd *cobra.Command, nodeName, opType string, unsafe bool) error {
	op := KV{"type": opType}
	if unsafe {
		op["options"] = KV{"unsafe": true}
	}

	data := KV{"operation": op}
	dataBytes := lo.Must(json.Marshal(data))

	kcArgs := []string{
		"patch", "nodes.talos.rtl.de", nodeName, "--type=merge", "--patch", string(dataBytes),
	}
	if *namespace != "" {
		kcArgs = append([]string{"-n", *namespace}, kcArgs...)
	}

	kCmd := exec.CommandContext(cmd.Context(), "kubectl", kcArgs...)
	kCmd.Stdout = os.Stdout
	kCmd.Stderr = os.Stderr
	if err := kCmd.Run(); err != nil {
		return fmt.Errorf("error running kubectl: %w", err)
	}

	if *watch {
		err := watchResource(cmd.Context(), "nodes.talos.rtl.de", *namespace, nodeName)
		if err != nil {
			return fmt.Errorf("error watching operation: %w", err)
		}
	}

	return nil
}
