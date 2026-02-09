package main

import (
	"fmt"

	"github.com/nexus/nexus/pkg/auth"
	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Clear local authentication session",
	Long:  `Remove the local session file and clear authentication credentials.`,
	RunE:  runLogout,
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}

func runLogout(cmd *cobra.Command, args []string) error {
	if !auth.IsLoggedIn() {
		fmt.Println("Not currently logged in.")
		return nil
	}

	if err := auth.ClearSession(); err != nil {
		return fmt.Errorf("failed to clear session: %w", err)
	}

	fmt.Println("✓ Successfully logged out")
	fmt.Println("Your local session has been cleared.")

	return nil
}
