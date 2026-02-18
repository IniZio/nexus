package main

import (
	"github.com/spf13/cobra"
)

var docCmd = &cobra.Command{
	Use:   "doc",
	Short: "Manage documentation tasks",
	Long:  `Create, verify, and publish documentation using Nexus task management.`,
}

func init() {
	rootCmd.AddCommand(docCmd)
}
