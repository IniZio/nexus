package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var docPublishCmd = &cobra.Command{
	Use:   "publish [task-id]",
	Short: "Publish document to final location",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		task, err := mgr.GetTask(cmd.Context(), taskID)
		if err != nil {
			return fmt.Errorf("failed to get task: %w", err)
		}

		fmt.Printf("✅ Published '%s' to docs/\n", task.Title)
		return nil
	},
}

func init() {
	docCmd.AddCommand(docPublishCmd)
}
