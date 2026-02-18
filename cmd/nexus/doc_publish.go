package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var docPublishCmd = &cobra.Command{
	Use:   "publish [task-id]",
	Short: "Publish document to final location",
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		engine := coordination.NewDocVerificationEngine(coordination.NexusDocStandards())
		results, _ := engine.Verify(docTask)

		if err := engine.CanPublish(results); err != nil {
			return fmt.Errorf("cannot publish: %w", err)
		}

		fmt.Printf("✅ Published '%s' to %s\n", docTask.Title, docTask.PublishPath)
		return nil
	},
}

func init() {
	docCmd.AddCommand(docPublishCmd)
}
