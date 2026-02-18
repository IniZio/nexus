package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show usage statistics",
	Example: `  nexus stats           # Last 7 days
  nexus stats --week   # Last 7 days
  nexus stats --month  # Last 30 days`,
	Run: func(cmd *cobra.Command, args []string) {
		days, _ := cmd.Flags().GetInt("days")

		stats, err := getTelemetryStats(days)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Printf("📈 Usage Statistics (Last %d Days)\n", days)
		fmt.Println("================================")
		fmt.Printf("Commands executed: %d\n", stats.TotalCommands)
		fmt.Printf("Success rate: %.1f%%\n", stats.SuccessRate)
		fmt.Printf("Avg command time: %v\n", stats.AvgCommandDuration)
		fmt.Printf("Workspaces created: %d\n", stats.WorkspacesCreated)
		fmt.Printf("Tasks completed: %d\n", stats.TasksCompleted)

		if len(stats.TopCommands) > 0 {
			fmt.Println("\nTop Commands:")
			for _, cmd := range stats.TopCommands[:5] {
				fmt.Printf("  %s: %d\n", cmd.Name, cmd.Count)
			}
		}

		if len(stats.CommonErrors) > 0 {
			fmt.Println("\nCommon Errors:")
			for _, err := range stats.CommonErrors[:3] {
				fmt.Printf("  %s: %d occurrences\n", err.Type, err.Count)
			}
		}
	},
}

func init() {
	statsCmd.Flags().Int("days", 7, "Number of days to include")
	statsCmd.Flags().Bool("week", false, "Show last 7 days")
	statsCmd.Flags().Bool("month", false, "Show last 30 days")
	rootCmd.AddCommand(statsCmd)
}
