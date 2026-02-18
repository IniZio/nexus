package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var telemetryStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show telemetry status",
	Run: func(cmd *cobra.Command, args []string) {
		config := loadTelemetryConfig()

		fmt.Println("📊 Telemetry Status")
		fmt.Println("------------------")
		fmt.Printf("Enabled: %v\n", config.Enabled)
		fmt.Printf("Anonymized: %v\n", config.Anonymize)
		fmt.Printf("Data location: ~/.nexus/telemetry.db\n")
		fmt.Printf("Retention: %d days\n", config.RetentionDays)

		stats, _ := getTelemetryStats(7)
		fmt.Printf("\nEvents (last 7 days): %d\n", stats.TotalEvents)
		fmt.Printf("Sessions: %d\n", stats.TotalSessions)
	},
}

func init() {
	telemetryCmd.AddCommand(telemetryStatusCmd)
}
