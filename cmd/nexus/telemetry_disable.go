package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var telemetryDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable telemetry collection",
	Run: func(cmd *cobra.Command, args []string) {
		config := loadTelemetryConfig()
		config.Enabled = false
		saveTelemetryConfig(config)
		fmt.Println("⏸️  Telemetry disabled")
		fmt.Println("No new data will be collected")
		fmt.Println("Existing data remains in ~/.nexus/telemetry.db")
	},
}

func init() {
	telemetryCmd.AddCommand(telemetryDisableCmd)
}
