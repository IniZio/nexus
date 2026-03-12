package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var branchUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Select a branch",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("branch use %q is not implemented yet; run 'nexus branch --help' for available workflows", args[0])
	},
}

func init() {
	branchCmd.AddCommand(branchUseCmd)
}
