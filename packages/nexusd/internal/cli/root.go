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
	configCmd.AddCommand(configSetCmd)

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
	rootCmd.AddCommand(portCmd)
	rootCmd.AddCommand(syncCmd)
	rootCmd.AddCommand(healthCmd)

	portCmd.AddCommand(
		portAddCmd,
		portListCmd,
		portRemoveCmd,
	)

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
	Short: "Manage configuration",
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		cfg, err := client.GetConfig()
		exitOnError(err)

		fmt.Printf("Configuration:\n")
		fmt.Printf("  idle_timeout: %v\n", cfg.IdleTimeout)
		fmt.Printf("  auto_pause: %v\n", cfg.AutoPause)
		fmt.Printf("  auto_resume: %v\n", cfg.AutoResume)
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set configuration value",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		value := args[1]

		client := getClient()
		err := client.SetConfig(key, value)
		exitOnError(err)

		fmt.Printf("Set %s = %s\n", key, value)
	},
}

func getClient() *Client {
	return NewClient(apiURL, token)
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
