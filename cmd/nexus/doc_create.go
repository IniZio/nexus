package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"nexus/pkg/coordination"
)

var docCreateCmd = &cobra.Command{
	Use:   "create [title]",
	Short: "Create a new documentation task",
	Example: `  nexus doc create "How to Debug Port Conflicts" --type how-to
  nexus doc create "Worktree Isolation Architecture" --type adr`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		title := args[0]
		docType, _ := cmd.Flags().GetString("type")

		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		task, err := mgr.CreateTask(cmd.Context(), coordination.CreateTaskRequest{
			Title:       fmt.Sprintf("[%s] %s", docType, title),
			Description: fmt.Sprintf("Documentation task of type: %s", docType),
		})
		if err != nil {
			return fmt.Errorf("failed to create task: %w", err)
		}

		wsName := fmt.Sprintf("doc-%s", slugify(title))
		fmt.Printf("✅ Created doc task: %s\n", task.ID)
		fmt.Printf("   Title: %s\n", title)
		fmt.Printf("   Type: %s\n", docType)
		fmt.Printf("   Workspace: %s\n", wsName)

		return nil
	},
}

func init() {
	docCreateCmd.Flags().String("type", "how-to", "Document type (tutorial, how-to, explanation, reference, adr)")
	docCmd.AddCommand(docCreateCmd)
}
