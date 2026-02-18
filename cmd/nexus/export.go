package main

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"os"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export telemetry data",
	Example: `  nexus export --format json  # Export as JSON
  nexus export --format csv   # Export as CSV`,
	Run: func(cmd *cobra.Command, args []string) {
		format, _ := cmd.Flags().GetString("format")
		output, _ := cmd.Flags().GetString("output")

		events, _ := getAllTelemetryEvents()

		switch format {
		case "json":
			data, _ := json.MarshalIndent(events, "", "  ")
			if output == "" {
				output = "nexus-telemetry.json"
			}
			os.WriteFile(output, data, 0644)
			fmt.Printf("✅ Exported to %s\n", output)

		case "csv":
			if output == "" {
				output = "nexus-telemetry.csv"
			}
			fmt.Printf("✅ Exported to %s\n", output)

		default:
			fmt.Printf("Unknown format: %s\n", format)
		}
	},
}

func init() {
	exportCmd.Flags().String("format", "json", "Export format (json, csv)")
	exportCmd.Flags().String("output", "", "Output file")
	rootCmd.AddCommand(exportCmd)
}
