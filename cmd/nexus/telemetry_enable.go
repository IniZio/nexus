package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var telemetryEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable telemetry collection",
	Run: func(cmd *cobra.Command, args []string) {
		config := loadTelemetryConfig()
		config.Enabled = true
		saveTelemetryConfig(config)
		fmt.Println("✅ Telemetry enabled")
		fmt.Println("Data is stored locally in ~/.nexus/telemetry.db")
		fmt.Println("You can export or delete your data anytime with 'nexus telemetry export' or 'nexus telemetry purge'")
	},
}

func init() {
	telemetryCmd.AddCommand(telemetryEnableCmd)
}
