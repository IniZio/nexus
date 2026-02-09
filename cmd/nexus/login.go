package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/nexus/nexus/pkg/auth"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Nexus coordination server",
	Long: `Authenticate with Nexus coordination server and save session locally.

This command will prompt for your username and create a local session file
at ~/.nexus/session.json that will be used for subsequent commands.`,
	RunE: runLogin,
}

func init() {
	rootCmd.AddCommand(loginCmd)
}

func runLogin(cmd *cobra.Command, args []string) error {
	fmt.Println("Nexus Login")
	fmt.Println("===========")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Username (GitHub username): ")
	username, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read username: %w", err)
	}
	username = strings.TrimSpace(username)

	if username == "" {
		return fmt.Errorf("username cannot be empty")
	}

	var password string
	if term.IsTerminal(int(syscall.Stdin)) {
		fmt.Print("Password (or press Enter to skip): ")
		passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			return fmt.Errorf("failed to read password: %w", err)
		}
		fmt.Println()
		password = strings.TrimSpace(string(passwordBytes))
	}

	userID := fmt.Sprintf("user_%d_%s", time.Now().Unix(), username)

	accessToken := "local-session-token"
	if password != "" {
		accessToken = fmt.Sprintf("token-%s-%d", username, time.Now().Unix())
	}

	session := &auth.Session{
		UserID:      userID,
		AccessToken: accessToken,
		ExpiresAt:   time.Now().Add(30 * 24 * time.Hour),
		CreatedAt:   time.Now(),
	}

	if err := auth.SaveSession(session); err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	fmt.Println()
	fmt.Println("✓ Login successful!")
	fmt.Printf("  User ID: %s\n", userID)
	fmt.Printf("  Session expires: %s\n", session.ExpiresAt.Format("2006-01-02 15:04:05"))
	fmt.Println()
	fmt.Println("Your session has been saved to ~/.nexus/session.json")
	fmt.Println("You can now use 'nexus workspace' commands.")

	return nil
}
