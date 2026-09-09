package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

func applyNodeCmd() *cobra.Command {
	var unsafe bool

	cmd := &cobra.Command{
		Use:   "apply NODE",
		Short: "Start an Apply operation on a Talos node",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Long = cmd.Short + "\n\nThis command will instruct the Talos operator to start an apply"
	cmd.Long += " operation on the given node."
	cmd.Long += "\nThis may be useful for applying a single node outside of a cluster-wide"
	cmd.Long += " RollingApply, or to resolve a ConfigOutOfSync state."
	cmd.Flags().BoolVar(&unsafe, "unsafe", false, "Skip Kubernetes drain and uncordon operations")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		nodeName := args[0]
		applyOp := KV{
			"type": "Apply",
		}

		// build operation options
		if unsafe {
			applyOp["options"] = KV{
				"unsafe": true,
			}
		}

		data := KV{
			"operation": applyOp,
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
