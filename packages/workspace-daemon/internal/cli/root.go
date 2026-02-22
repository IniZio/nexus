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

	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(configCmd)

	rootCmd.AddCommand(execCmd)
	rootCmd.AddCommand(consoleCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(destroyCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(urlCmd)
	rootCmd.AddCommand(useCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(pauseCmd)
	rootCmd.AddCommand(resumeCmd)
	rootCmd.AddCommand(createCmd)

	rootCmd.AddCommand(sessionsCmd)
	rootCmd.AddCommand(checkpointCmd)
	rootCmd.AddCommand(servicesCmd)
	rootCmd.AddCommand(proxyCmd)
	rootCmd.AddCommand(syncCmd)

	sessionsCmd.AddCommand(
		sessionsListCmd,
		sessionsAttachCmd,
		sessionsKillCmd,
	)

	checkpointCmd.AddCommand(
		checkpointCreateCmd,
		checkpointListCmd,
		restoreCmd,
	)

	servicesCmd.AddCommand(
		servicesListCmd,
		servicesLogsCmd,
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

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show configuration",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("API URL: %s\n", apiURL)
		fmt.Printf("Token: %s\n", token)
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
