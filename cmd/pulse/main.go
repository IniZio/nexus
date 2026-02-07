package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nexus/nexus/cmd/pulse/internal/server"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "pulse",
		Short: "Pulse - Linear-inspired project management",
		Long: `Pulse is a fast, keyboard-first project management tool
inspired by Linear. Manage issues, cycles, and team velocity.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	rootCmd.Version = fmt.Sprintf("Pulse %s (build: %s %s)", version, commit, date)

	var addr string
	var dataDir string

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Pulse server",
		Long:  `Start the Pulse web server for project management.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Handle shutdown
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigChan
				cancel()
			}()

			// Ensure data directory exists
			if err := os.MkdirAll(dataDir, 0755); err != nil {
				return fmt.Errorf("failed to create data dir: %w", err)
			}

			// Start Pulse server with in-memory workspace storage
			pulseServer := server.NewServer(nil, addr)
			if err := pulseServer.Start(ctx); err != nil {
				return fmt.Errorf("failed to start pulse server: %w", err)
			}

			return nil
		},
	}

	startCmd.Flags().StringVar(&addr, "addr", "localhost:3002", "Address to listen on")
	startCmd.Flags().StringVar(&dataDir, "data-dir", "./.pulse-data", "Data directory")

	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(createVersionCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func createVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number of Pulse",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Pulse %s\n", version)
		},
	}
}
