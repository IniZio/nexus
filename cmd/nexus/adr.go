package main

import (
	"github.com/spf13/cobra"
)

var adrCmd = &cobra.Command{
	Use:   "adr",
	Short: "Manage Architecture Decision Records",
}

var adrListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all ADRs",
	RunE: func(cmd *cobra.Command, args []string) error {
		return nil
	},
}

func init() {
	adrCmd.AddCommand(adrListCmd)
	rootCmd.AddCommand(adrCmd)
}
