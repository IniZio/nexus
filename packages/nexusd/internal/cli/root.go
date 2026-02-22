package cli

import (
	"fmt"
	"os"

	"github.com/nexus/nexus/packages/nexusd/internal/config"
	"github.com/spf13/cobra"
)

var (
	version     = "0.1.0"
	cfgFile     string
	verbose     bool
	jsonOutput  bool
	quiet       bool
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

var ErrDaemonNotRunning = fmt.Errorf("daemon not running")

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "Config file path (default: ~/.nexus/config.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "Suppress non-essential output")

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(boulderCmd)
	rootCmd.AddCommand(workspaceCmd)
	rootCmd.AddCommand(traceCmd)

	configCmd.AddCommand(configGetCmd, configSetCmd)
	boulderCmd.AddCommand(boulderStatusCmd, boulderPauseCmd, boulderResumeCmd, boulderConfigCmd)
	boulderConfigCmd.AddCommand(boulderConfigGetCmd, boulderConfigSetCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		if jsonOutput {
			fmt.Printf(`{"cli_version":"%s"}`, version)
		} else {
			fmt.Printf("nexus version %s\n", version)
		}
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage configuration",
}

var configGetCmd = &cobra.Command{
	Use:   "get [key]",
	Short: "Get configuration value",
	Args:  cobra.RangeArgs(0, 1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}

		if len(args) == 0 {
			if jsonOutput {
				fmt.Printf(`%s`, toJSON(cfg))
			} else {
				fmt.Println("Current configuration:")
				fmt.Printf("  version: %s\n", cfg.Version)
				fmt.Printf("  workspace.default: %s\n", cfg.Workspace.Default)
				fmt.Printf("  workspace.auto_start: %t\n", cfg.Workspace.AutoStart)
				fmt.Printf("  workspace.storage_path: %s\n", cfg.Workspace.StoragePath)
				fmt.Printf("  boulder.enforcement_level: %s\n", cfg.Boulder.EnforcementLevel)
				fmt.Printf("  boulder.idle_threshold: %d\n", cfg.Boulder.IdleThreshold)
				fmt.Printf("  telemetry.enabled: %t\n", cfg.Telemetry.Enabled)
				fmt.Printf("  telemetry.sampling: %d\n", cfg.Telemetry.Sampling)
				fmt.Printf("  telemetry.retention_days: %d\n", cfg.Telemetry.RetentionDays)
				fmt.Printf("  daemon.host: %s\n", cfg.Daemon.Host)
				fmt.Printf("  daemon.port: %d\n", cfg.Daemon.Port)
				fmt.Printf("  cli.update.auto_install: %t\n", cfg.CLI.Update.AutoInstall)
				fmt.Printf("  cli.update.channel: %s\n", cfg.CLI.Update.Channel)
			}
			return
		}

		key := args[0]
		value, err := cfg.Get(key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"key":"%s","value":"%s"}`, key, value)
		} else {
			fmt.Println(value)
		}
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set configuration value",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		value := args[1]

		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}

		if err := cfg.Set(key, value); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"key":"%s","value":"%s"}`, key, value)
		} else {
			fmt.Printf("Set %s = %s\n", key, value)
		}
	},
}

var boulderCmd = &cobra.Command{
	Use:   "boulder",
	Short: "Boulder enforcement commands",
}

var boulderStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show enforcement status",
	Run: func(cmd *cobra.Command, args []string) {
		state, err := loadBoulderState()
		if err != nil {
			if jsonOutput {
				fmt.Printf(`{"error":"%v"}`, err)
			} else {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"status":"%s","iteration":%d}`, state.Status, state.Iteration)
		} else {
			fmt.Println("Boulder Enforcer Status")
			fmt.Println("──────────────────────")
			fmt.Printf("Status: %s\n", state.Status)
			fmt.Printf("Iteration: %d\n", state.Iteration)
		}
	},
}

var boulderPauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Pause enforcement",
	Run: func(cmd *cobra.Command, args []string) {
		state, err := loadBoulderState()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		state.Status = "PAUSED"
		if err := saveBoulderState(state); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"status":"%s"}`, state.Status)
		} else {
			fmt.Println("Boulder enforcement paused")
		}
	},
}

var boulderResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume enforcement",
	Run: func(cmd *cobra.Command, args []string) {
		state, err := loadBoulderState()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		state.Status = "ENFORCING"
		if err := saveBoulderState(state); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"status":"%s"}`, state.Status)
		} else {
			fmt.Println("Boulder enforcement resumed")
		}
	},
}

var boulderConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage boulder configuration",
}

var boulderConfigGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get boulder config value",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := loadBoulderConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		key := args[0]
		value, err := cfg.Get(key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"key":"%s","value":"%s"}`, key, value)
		} else {
			fmt.Println(value)
		}
	},
}

var boulderConfigSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set boulder config value",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := loadBoulderConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		key := args[0]
		value := args[1]

		if err := cfg.Set(key, value); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if err := saveBoulderConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"key":"%s","value":"%s"}`, key, value)
		} else {
			fmt.Printf("Set %s = %s\n", key, value)
		}
	},
}

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Workspace management commands",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Use 'nexus workspace --help' for available workspace commands")
	},
}

var traceCmd = &cobra.Command{
	Use:   "trace",
	Short: "Trace/attribution commands (Phase 2)",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Trace commands are not yet implemented (Phase 2)")
	},
}

func getConfig() *config.Config {
	if cfgFile != "" {
		cfg := config.DefaultConfig()
		if err := loadConfigFromFile(cfg, cfgFile); err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config from %s: %v\n", cfgFile, err)
			os.Exit(1)
		}
		return cfg
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

func loadConfigFromFile(cfg *config.Config, path string) error {
	_, err := os.ReadFile(path)
	return err
}

func toJSON(v interface{}) string {
	return fmt.Sprintf("%+v", v)
}
