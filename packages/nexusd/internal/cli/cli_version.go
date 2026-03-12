package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var cliVersionCmd = &cobra.Command{
	Use:   "cli-version",
	Short: "Print Nexus CLI binary version",
	Run: func(cmd *cobra.Command, args []string) {
		if jsonOutput {
			fmt.Printf(`{"cli_version":"%s"}`, version)
		} else {
			fmt.Printf("nexus cli version %s\n", version)
		}
	},
}
