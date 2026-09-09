package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

func resetNodeCmd() *cobra.Command {
	var (
		wipeMode           string
		systemLabelsToWipe []string
		userDisksToWipe    []string
		reboot             bool
		graceful           bool
		unsafe             bool
		cloudReset         bool
	)

	cmd := &cobra.Command{
		Use:   "reset NODE",
		Short: "Start a Reset operation on a Talos node",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Long = cmd.Short + "\n\nThis command will instruct the Talos operator to start a reset"
	cmd.Long += " operation on the given node."

	cmd.Flags().
		StringVar(&wipeMode, "wipe-mode", "all", "Disk reset mode (all, system-disk, user-disks)")
	cmd.Flags().
		StringSliceVar(&systemLabelsToWipe, "system-labels-to-wipe", nil,
			"If set, wipe only selected system disk partitions by label")
	cmd.Flags().
		StringSliceVar(&userDisksToWipe, "user-disks-to-wipe", nil, "If set, wipe only the specified user disks")
	cmd.Flags().
		BoolVar(&reboot, "reboot", false, "Reboot the node after resetting instead of shutting down")
	cmd.Flags().
		BoolVar(&graceful, "graceful", true, "Attempt to gracefully leave etcd (if applicable)")
	cmd.Flags().BoolVar(&unsafe, "unsafe", false, "Skip Kubernetes drain and uncordon operations")
	cmd.Flags().BoolVar(
		&cloudReset,
		"cloud-reset",
		false,
		("Wipe only STATE and EPHEMERAL system partitions to avoid\n" +
			"having to reinstall the OS in a cloud environment"),
	)

	cmd.MarkFlagsMutuallyExclusive("cloud-reset", "system-labels-to-wipe")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		nodeName := args[0]

		// handle cloud-reset flag
		if cloudReset {
			systemLabelsToWipe = []string{"STATE", "EPHEMERAL"}
		}

		resetOp := KV{
			"type": "Reset",
		}

		// build operation options
		if unsafe {
			resetOp["options"] = KV{
				"unsafe": true,
			}
		}

		// build reset options
		resetOptions := KV{}
		if cmd.Flags().Changed("wipe-mode") {
			resetOptions["mode"] = wipeMode
		}
		if len(systemLabelsToWipe) > 0 {
			resetOptions["systemLabelsToWipe"] = systemLabelsToWipe
		}
		if len(userDisksToWipe) > 0 {
			resetOptions["userLabelsToWipe"] = userDisksToWipe
		}
		if cmd.Flags().Changed("reboot") {
			resetOptions["reboot"] = reboot
		}
		if cmd.Flags().Changed("graceful") {
			resetOptions["graceful"] = graceful
		}

		// only add reset options if any were specified
		if len(resetOptions) > 0 {
			resetOp["reset"] = resetOptions
		}

		data := KV{
			"operation": resetOp,
		}

		// serialize it:
		dataBytes := lo.Must(json.Marshal(data))

		// build kubectl args:
		kcArgs := []string{
			"patch", "nodes.talos.rtl.de", nodeName, "--type=merge", "--patch", string(dataBytes),
		}
		if *namespace != "" {
			kcArgs = append([]string{"-n", *namespace}, kcArgs...)
		}

		// run kubectl:
		kCmd := exec.CommandContext(cmd.Context(), "kubectl", kcArgs...)
		kCmd.Stdout = os.Stdout
		kCmd.Stderr = os.Stderr
		err := kCmd.Run()
		if err != nil {
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
	return cmd
}
