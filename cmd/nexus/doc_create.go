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

		docTask := coordination.DocTask{
			Task: coordination.Task{
				Title:  title,
				Type:   "documentation",
				Status: "pending",
			},
			DocType: coordination.DocType(docType),
		}

		if docTask.DocType == coordination.DocTypeADR {
			adrMgr := coordination.NewADRManager(db)
			number, err := adrMgr.GetNextADRNumber()
			if err != nil {
				return err
			}
			docTask.ADRNumber = number
			fmt.Printf("Creating ADR-%03d...\n", number)
		}

		registry := coordination.NewDocTemplateRegistry()
		variant := registry.SelectVariant(docTask.DocType)
		docTask.TemplateVariant = variant

		wsName := fmt.Sprintf("doc-%s", slugify(title))
		fmt.Printf("Creating workspace '%s'...\n", wsName)

		fmt.Printf("✅ Created doc task: %s\n", docTask.ID)
		fmt.Printf("   Type: %s\n", docType)
		fmt.Printf("   Template: %s\n", variant)
		fmt.Printf("   Workspace: %s\n", wsName)

		return nil
	},
}

func init() {
	docCreateCmd.Flags().String("type", "how-to", "Document type (tutorial, how-to, explanation, reference, adr)")
	docCmd.AddCommand(docCreateCmd)
}
