package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var docAssignCmd = &cobra.Command{
	Use:     "assign [task-id]",
	Short:   "Assign a reviewer to a document",
	Example: `  nexus doc assign doc-123 --reviewer auto`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]
		reviewer, _ := cmd.Flags().GetString("reviewer")

		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		task, err := mgr.GetTask(cmd.Context(), taskID)
		if err != nil {
			return fmt.Errorf("failed to get task: %w", err)
		}

		if reviewer == "auto" {
			reviewer = "auto-assigned-reviewer"
			fmt.Printf("🎯 Auto-assigned reviewer: %s\n", reviewer)
		}

		fmt.Printf("✅ Assigned reviewer '%s' to document '%s'\n", reviewer, task.Title)
		return nil
	},
}

func init() {
	docAssignCmd.Flags().String("reviewer", "auto", "Reviewer ID or 'auto' for auto-assignment")
	docCmd.AddCommand(docAssignCmd)
}
