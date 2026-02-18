package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"nexus/pkg/coordination"
)

var docVerifyCmd = &cobra.Command{
	Use:   "verify [task-id]",
	Short: "Run verification checks on a document",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("task-id required")
		}
		taskID := args[0]

		fmt.Printf("🔍 Verifying document '%s'...\n", docTask.Title)

		engine := coordination.NewDocVerificationEngine(coordination.NexusDocStandards())
		results, err := engine.Verify(docTask)
		if err != nil {
			return err
		}

		allPassed := true
		for _, r := range results {
			status := "✅"
			if !r.Passed {
				status = "❌"
				allPassed = false
			}
			fmt.Printf("%s %s", status, r.VerificationName)
			if r.AutoFixed {
				fmt.Print(" (auto-fixed)")
			}
			if r.Error != "" {
				fmt.Printf(": %s", r.Error)
			}
			fmt.Println()
		}

		if allPassed {
			fmt.Println("\n✅ All verifications passed!")
			fmt.Println("Ready for peer review.")
		} else {
			fmt.Println("\n❌ Some verifications failed.")
			fmt.Println("Fix issues before submitting for review.")
		}

		return nil
	},
}

func init() {
	docCmd.AddCommand(docVerifyCmd)
}
