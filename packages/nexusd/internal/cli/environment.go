package cli

import "github.com/spf13/cobra"

func bindWorkspaceSurface(target *cobra.Command) {
	target.AddCommand(workspaceCreateCmd)
	target.AddCommand(workspaceStartCmd)
	target.AddCommand(workspaceStopCmd)
	target.AddCommand(workspaceDeleteCmd)
	target.AddCommand(workspaceListCmd)
	target.AddCommand(workspaceSSHCmd)
	target.AddCommand(workspaceExecCmd)
	target.AddCommand(workspaceInjectKeyCmd)
	target.AddCommand(workspaceStatusCmd)
	target.AddCommand(workspaceLogsCmd)
	target.AddCommand(workspaceUseCmd)
	target.AddCommand(workspaceCheckpointCmd)
}
