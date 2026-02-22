package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version     = "0.1.0"
	apiURL      string
	token       string
	daemonToken string
)

var rootCmd = &cobra.Command{
	Use:   "nexus",
	Short: "Nexus workspace management CLI",
	Long:  `Nexus is an AI-native development environment with workspace management and tools.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return nil
	},
}

var (
	ErrDaemonNotRunning = fmt.Errorf("daemon not running")
)

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&apiURL, "url", "u", "http://localhost:8080", "API server URL")
	rootCmd.PersistentFlags().StringVarP(&token, "token", "t", "", "Authentication token")
	rootCmd.PersistentFlags().StringVar(&daemonToken, "daemon-token", "", "Daemon token for serve command")

	rootCmd.AddCommand(workspaceCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(versionCmd)

	workspaceCmd.AddCommand(
		workspaceCreateCmd,
		workspaceListCmd,
		workspaceStatusCmd,
		workspaceStartCmd,
		workspaceStopCmd,
		workspaceDeleteCmd,
		workspaceExecCmd,
		workspaceLogsCmd,
		workspaceUseCmd,
		syncCmd,
	)

	syncCmd.AddCommand(
		syncStatusCmd,
		syncPauseCmd,
		syncResumeCmd,
		syncFlushCmd,
	)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("nexus version %s\n", version)
	},
}

func getClient() *Client {
	return NewClient(apiURL, token)
}

func ensureDaemonRunning() error {
	client := getClient()
	if err := client.Health(); err != nil {
		return ErrDaemonNotRunning
	}
	return nil
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
