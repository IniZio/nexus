package cli

import (
	"fmt"
	"net"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/nexus/nexus/packages/nexusd/internal/config"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show system status",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := getConfig()
		daemonRunning := checkDaemonRunning(cfg)
		
		workspaceCount := 0
		if daemonRunning {
			count, err := countWorkspaces(cfg)
			if err == nil {
				workspaceCount = count
			}
		}

		boulderState, _ := loadBoulderState()

		if jsonOutput {
			fmt.Printf(`{"daemon_running":%t,"workspace_count":%d,"boulder_status":"%s"}`,
				daemonRunning, workspaceCount, boulderState.Status)
		} else {
			fmt.Println("Nexus Status")
			fmt.Println("───────────")
			fmt.Printf("Daemon: %s\n", boolToStatus(daemonRunning))
			fmt.Printf("Workspaces: %d\n", workspaceCount)
			fmt.Printf("Boulder: %s\n", boulderState.Status)
		}
	},
}

func checkDaemonRunning(cfg *config.Config) bool {
	addr := fmt.Sprintf("%s:%d", cfg.Daemon.Host, cfg.Daemon.Port)
	
	conn, err := net.Dial("tcp", addr)
	if err == nil {
		conn.Close()
		return true
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/health", addr))
	if err == nil {
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}

	return false
}

func countWorkspaces(cfg *config.Config) (int, error) {
	client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")
	result, err := client.ListWorkspaces()
	if err != nil {
		return 0, err
	}
	return result.Total, nil
}

func boolToStatus(b bool) string {
	if b {
		return "\033[32mrunning\033[0m"
	}
	return "\033[31mstopped\033[0m"
}
