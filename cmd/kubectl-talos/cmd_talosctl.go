package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func talosctlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "talosctl CLUSTER -- ARGS...",
		Short:                 "Execute talosctl commands against the given operator-managed Talos cluster",
		Args:                  cobra.MinimumNArgs(2),
		DisableFlagsInUseLine: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		// same exact pattern as kubectl, except talosctl and talosconfig

		cluster := args[0]
		talosctlArgs := args[1:]

		secretArgs := []string{
			"get",
			"secret",
			cluster + "-talosconfig",
			"-o",
			"jsonpath={.data.talosconfig}",
		}
		if *namespace != "" {
			secretArgs = append([]string{"-n", *namespace}, secretArgs...)
		}
		secretCmd := exec.CommandContext(cmd.Context(), "kubectl", secretArgs...)
		talosconfigBytes64, err := secretCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("error fetching talosconfig: %w\n%s", err, string(talosconfigBytes64))
		}

		talosconfigFile, err := os.CreateTemp("", "talosconfig")
		if err != nil {
			return fmt.Errorf("error creating temp file: %w", err)
		}
		defer os.Remove(talosconfigFile.Name())
		// decode base64 and write to temp file
		_, err = io.Copy(
			talosconfigFile,
			base64.NewDecoder(base64.StdEncoding, bytes.NewBuffer(talosconfigBytes64)),
		)
		if err != nil {
			return fmt.Errorf("error writing talosconfig: %w", err)
		}
		err = talosconfigFile.Close()
		if err != nil {
			return fmt.Errorf("error closing talosconfig file: %w", err)
		}

		// execute talosctl with the provided args and the temp talosconfig
		tCmd := exec.CommandContext(cmd.Context(), "talosctl", talosctlArgs...)
		tCmd.Env = append(os.Environ(), "TALOSCONFIG="+talosconfigFile.Name())
		tCmd.Stdout = os.Stdout
		tCmd.Stderr = os.Stderr
		tCmd.Stdin = os.Stdin

		err = tCmd.Run()
		_, isExitErr := err.(*exec.ExitError)
		if err != nil && !isExitErr {
			return fmt.Errorf("error running talosctl: %w", err)
		}

		return nil
	}
	return cmd
}
