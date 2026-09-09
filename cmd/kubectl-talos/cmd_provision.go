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

func provisionNodeCmd() *cobra.Command {
	var skip bool

	cmd := &cobra.Command{
		Use:   "provision NODE [ADDRESS]",
		Short: "Start a Provision operation on a Talos node",
		Args:  cobra.RangeArgs(1, 2),
	}
	cmd.Long = cmd.Short + "\n\nThis command will instruct the Talos operator to start a"
	cmd.Long += " provisioning operation on the given node, using the provided address."
	cmd.Long += "\nThis operation will only work on unprovisioned nodes."
	cmd.Long += "\n\nWhen --skip is set, the ADDRESS argument is not required."

	cmd.Flags().
		BoolVar(&skip, "skip", false, "Adopt an existing node by skipping the actual provisioning of the node")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		nodeName := args[0]
		if nodeName == "" {
			return errors.New("node must be provided")
		}

		var address string
		if len(args) > 1 {
			address = args[1]
		}

		if !skip && address == "" {
			return errors.New("address must be provided when --skip is not set")
		}

		provisionOpts := KV{}
		if address != "" {
			provisionOpts["address"] = address
		}
		if skip {
			provisionOpts["skip"] = true
		}

		payload := KV{
			"operation": KV{
				"type":      "Provision",
				"provision": provisionOpts,
			},
		}
		payloadBytes := lo.Must(json.Marshal(payload))

		kcArgs := []string{
			"patch",
			"nodes.talos.rtl.de",
			nodeName,
			"--type=merge",
			"--patch",
			string(payloadBytes),
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

		if *watch {
			watchResource(cmd.Context(), "nodes.talos.rtl.de", *namespace, nodeName)
		}

		return nil
	}
	return cmd
}
