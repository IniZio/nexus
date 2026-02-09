package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/ssh"
	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
	Long:  `Manage SSH keys for secure access.`,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication status",
	Long:  `Display current authentication status for GitHub and SSH.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runAuthStatus()
	},
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(sshSetupCmd)
}

func runAuthStatus() error {
	fmt.Println("🔍 Authentication Status")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	fmt.Println("ℹ️  GitHub authentication is handled via OAuth callback")

	configPath := config.GetUserConfigPath()
	if _, err := os.Stat(configPath); err == nil {
		userCfg, err := config.LoadUserConfig(configPath)
		if err == nil && userCfg != nil {
			fmt.Printf("✅ Saved config: %s\n", configPath)
			if userCfg.SSH.KeyPath != "" {
				fmt.Printf("✅ SSH key: %s\n", userCfg.SSH.KeyPath)
			}
		}
	} else {
		fmt.Println("⚠️  No saved configuration found")
	}

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

var sshSetupCmd = &cobra.Command{
	Use:   "ssh-setup",
	Short: "Set up SSH keys and upload to GitHub",
	Long: `Generate or use existing SSH keys and upload them to GitHub.
This enables secure authentication for workspace access.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runSSHSetup()
	},
}

func runSSHSetup() error {
	fmt.Println("🔑 SSH Key Setup")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	home := os.Getenv("HOME")
	if home == "" {
		return fmt.Errorf("HOME environment variable not set")
	}

	sshDir := filepath.Join(home, ".ssh")

	hasEd25519, hasRSA, err := ssh.DetectExistingKeys(sshDir)
	if err != nil {
		return fmt.Errorf("failed to detect existing keys: %w", err)
	}

	var keyPath string
	if !hasEd25519 && !hasRSA {
		fmt.Println("Generating new ED25519 SSH key...")
		if err := ssh.EnsureSSHKey(sshDir, "ed25519"); err != nil {
			return fmt.Errorf("failed to generate SSH key: %w", err)
		}
		keyPath = filepath.Join(sshDir, "id_ed25519")
		fmt.Println("✅ SSH key generated")
	} else if hasEd25519 {
		keyPath = filepath.Join(sshDir, "id_ed25519")
		fmt.Println("✅ Using existing ED25519 key")
	} else {
		keyPath = filepath.Join(sshDir, "id_rsa")
		fmt.Println("✅ Using existing RSA key")
	}

	pubKey, err := ssh.ReadPublicKey(keyPath + ".pub")
	if err != nil {
		return fmt.Errorf("failed to read public key: %w", err)
	}

	fmt.Println("")
	fmt.Println("📋 Public Key:")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println(pubKey)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	fmt.Println("")
	fmt.Println("📋 SSH key generated and ready for use")

	configPath := config.GetUserConfigPath()
	configDir := filepath.Dir(configPath)

	if err := config.EnsureConfigDirectory(configDir); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	var userCfg *config.UserConfig
	if _, err := os.Stat(configPath); err == nil {
		userCfg, _ = config.LoadUserConfig(configPath)
		if userCfg == nil {
			userCfg = &config.UserConfig{}
		}
	} else {
		userCfg = &config.UserConfig{}
	}

	userCfg.SSH.KeyPath = keyPath
	userCfg.SSH.PublicKey = pubKey

	if err := config.SaveUserConfig(configPath, userCfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println("")
	fmt.Println("✅ SSH setup complete!")

	return nil
}
