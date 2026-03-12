package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List projects",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("project list is not implemented yet; use 'nexus project --help' for available workflows")
	},
}

func init() {
	projectCmd.AddCommand(projectListCmd)
}
