package main

import (
	"github.com/spf13/cobra"
)

var telemetryCmd = &cobra.Command{
	Use:   "telemetry",
	Short: "Manage telemetry settings",
}

func init() {
	rootCmd.AddCommand(telemetryCmd)
}
