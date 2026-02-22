package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var boulderCmd = &cobra.Command{
	Use:   "boulder",
	Short: "Manage boulder enforcement",
}

var boulderStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check boulder enforcement status",
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		err := client.Health()
		if err != nil {
			fmt.Println("Boulder: Not running")
			return
		}
		fmt.Println("Boulder: Active")
	},
}

var boulderPauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Pause boulder enforcement",
	Run: func(cmd *cobra.Command, args []string) {
		resp, err := http.Post(apiURL+"/api/v1/boulder/pause", "application/json", nil)
		if err != nil {
			fmt.Println("Failed to pause boulder:", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			fmt.Println("Boulder enforcement paused")
		} else {
			fmt.Printf("Failed to pause boulder (status %d)\n", resp.StatusCode)
		}
	},
}

var boulderResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume boulder enforcement",
	Run: func(cmd *cobra.Command, args []string) {
		resp, err := http.Post(apiURL+"/api/v1/boulder/resume", "application/json", nil)
		if err != nil {
			fmt.Println("Failed to resume boulder:", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			fmt.Println("Boulder enforcement resumed")
		} else {
			fmt.Printf("Failed to resume boulder (status %d)\n", resp.StatusCode)
		}
	},
}
