package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

func bootstrapNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap NODE",
		Short: "Start a Bootstrap operation on a Talos node",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Long = cmd.Short + "\n\nThis command will instruct the Talos operator to start a"
	cmd.Long += " bootstrap operation on the given node, using the provided address."
	cmd.Long += "\nThis operation only ever needs to be done once per cluster."

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		node := args[0]
		if node == "" {
			return errors.New("node must be provided")
		}

		payload := KV{
			"operation": KV{
				"type": "Bootstrap",
			},
		}
		payloadBytes := lo.Must(json.Marshal(payload))

		kcArgs := []string{
			"patch", "nodes.talos.rtl.de", node, "--type=merge", "--patch", string(payloadBytes),
		}
		if *namespace != "" {
			kcArgs = append([]string{"-n", *namespace}, kcArgs...)
		}

		kCmd := exec.CommandContext(cmd.Context(), "kubectl", kcArgs...)
		kCmd.Stdout = os.Stdout
		kCmd.Stderr = os.Stderr
		err := kCmd.Run()
		if err != nil {
			return fmt.Errorf("error running kubectl: %w", err)
		}

		return nil
	}
	return cmd
}
