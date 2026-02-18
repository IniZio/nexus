package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var docListCmd = &cobra.Command{
	Use:   "list",
	Short: "List documentation tasks",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		tasks, err := mgr.ListTasks(cmd.Context(), "")
		if err != nil {
			return fmt.Errorf("failed to list tasks: %w", err)
		}

		fmt.Println("📚 Documentation Tasks:")
		fmt.Println("------------------------")

		count := 0
		for _, task := range tasks {
			fmt.Printf("%s | %s | %s\n", task.ID, task.Status, task.Title)
			count++
		}

		if count == 0 {
			fmt.Println("No documentation tasks found.")
		}

		return nil
	},
}

func init() {
	docListCmd.Flags().String("status", "", "Filter by status")
	docListCmd.Flags().String("type", "", "Filter by document type")
	docCmd.AddCommand(docListCmd)
}
