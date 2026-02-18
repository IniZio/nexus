package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var docListCmd = &cobra.Command{
	Use:   "list",
	Short: "List documentation tasks",
	RunE: func(cmd *cobra.Command, args []string) error {
		status, _ := cmd.Flags().GetString("status")
		docType, _ := cmd.Flags().GetString("type")

		fmt.Println("📚 Documentation Tasks:")
		fmt.Println("------------------------")
		for _, doc := range docs {
			fmt.Printf("%s | %s | %s | %s\n", doc.ID, doc.DocType, doc.Status, doc.Title)
		}

		return nil
	},
}

func init() {
	docListCmd.Flags().String("status", "", "Filter by status")
	docListCmd.Flags().String("type", "", "Filter by document type")
	docCmd.AddCommand(docListCmd)
}
