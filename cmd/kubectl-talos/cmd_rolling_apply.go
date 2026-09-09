package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

func rollingApplyClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rolling-apply CLUSTER",
		Short: "Start a RollingApply operation on a Talos cluster",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Long = cmd.Short + "\n\nThis command will instruct the Talos operator to start a"
	cmd.Long += " RollingApply operation on the given cluster."
	cmd.Long += "\nThis will apply configuration changes to all nodes in the cluster in a rolling fashion,"
	cmd.Long += " respecting node draining and ensuring high availability throughout the process."

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		clusterName := args[0]
		data := KV{
			"operation": KV{
				"type": "RollingApply",
			},
		}

		// serialize it:
		dataBytes := lo.Must(json.Marshal(data))

		// build kubectl args:
		kcArgs := []string{
			"patch",
			"clusters.talos.rtl.de",
			clusterName,
			"--type=merge",
			"--patch",
			string(dataBytes),
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
			err := watchResource(cmd.Context(), "clusters.talos.rtl.de", *namespace, clusterName)
			if err != nil {
				return fmt.Errorf("error watching operation: %w", err)
			}
		}

		return nil
	}
	return cmd
}
