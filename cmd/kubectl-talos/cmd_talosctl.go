package main

import (
	"github.com/spf13/cobra"
)

func talosctlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "talosctl NODE -- ARGS...",
		Short:                 "Execute talosctl commands against the given Talos node",
		Args:                  cobra.MinimumNArgs(2),
		DisableFlagsInUseLine: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return talosctlNode(cmd, args[0], args[1:]...)
	}
	return cmd
}
