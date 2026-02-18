package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"nexus/pkg/coordination"
)

var docAssignCmd = &cobra.Command{
	Use:     "assign [task-id]",
	Short:   "Assign a reviewer to a document",
	Example: `  nexus doc assign doc-123 --reviewer auto`,
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]
		reviewer, _ := cmd.Flags().GetString("reviewer")

		if reviewer == "auto" {
			assigner := coordination.NewDocReviewerAssigner(db)
			reviewerID, err := assigner.AssignReviewer(docTask)
			if err != nil {
				return err
			}
			reviewer = reviewerID

			fmt.Printf("🎯 Auto-assigned reviewer: %s\n", reviewer)
			fmt.Println(assigner.GetReviewerInstructions(docTask))
		}

		fmt.Printf("✅ Assigned reviewer '%s' to document '%s'\n", reviewer, docTask.Title)
		return nil
	},
}

func init() {
	docAssignCmd.Flags().String("reviewer", "auto", "Reviewer ID or 'auto' for auto-assignment")
	docCmd.AddCommand(docAssignCmd)
}
