package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"nexus/pkg/telemetry"
)

var insightsCmd = &cobra.Command{
	Use:   "insights",
	Short: "Show usage insights and suggestions",
	Run: func(cmd *cobra.Command, args []string) {
		if telemetryDB == nil {
			if err := initTelemetryDB(); err != nil {
				fmt.Printf("Error: %v\n", err)
				return
			}
		}

		analyzer := telemetry.NewInsightsAnalyzer(telemetryDB)
		insights, _ := analyzer.GenerateInsights()

		if len(insights) == 0 {
			fmt.Println("✅ No issues detected! Keep up the good work.")
			return
		}

		fmt.Println("💡 Insights")
		fmt.Println("==========")

		for _, insight := range insights {
			icon := "ℹ️"
			if insight.Severity == "high" {
				icon = "⚠️"
			}
			fmt.Printf("\n%s %s\n", icon, insight.Title)
			fmt.Printf("   %s\n", insight.Description)
		}
	},
}

func init() {
	rootCmd.AddCommand(insightsCmd)
}
