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
		var workspaceNames []string
		if daemonRunning {
			count, err := countWorkspaces(cfg)
			if err == nil {
				workspaceCount = count
			}
			names, err := listWorkspaceNames(cfg)
			if err == nil {
				workspaceNames = names
			}
		}

		activeWorkspace, _ := getActiveWorkspace()

		boulderState, _ := loadBoulderState()

		if jsonOutput {
			fmt.Printf(`{"daemon_running":%t,"workspace_count":%d,"active_workspace":"%s","boulder_status":"%s"}`,
				daemonRunning, workspaceCount, activeWorkspace, boulderState.Status)
		} else {
			fmt.Println("Nexus Status")
			fmt.Println("───────────")
			fmt.Printf("Daemon: %s\n", boolToStatus(daemonRunning))
			fmt.Printf("Active Workspace: %s\n", boolToString(activeWorkspace != "", activeWorkspace, "none"))
			fmt.Printf("Workspaces: %d\n", workspaceCount)
			if len(workspaceNames) > 0 {
				fmt.Printf("Available: %s\n", joinStrings(workspaceNames, ", "))
			}
			fmt.Printf("Boulder: %s\n", boulderState.Status)
			if activeWorkspace == "" {
				fmt.Printf("\nHint: Run 'nexus workspace use <name>' to switch to a workspace\n")
			}
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

func boolToString(cond bool, trueVal, falseVal string) string {
	if cond {
		return trueVal
	}
	return falseVal
}

func joinStrings(items []string, sep string) string {
	result := ""
	for i, item := range items {
		if i > 0 {
			result += sep
		}
		result += item
	}
	return result
}

func listWorkspaceNames(cfg *config.Config) ([]string, error) {
	client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")
	result, err := client.ListWorkspaces()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(result.Workspaces))
	for _, ws := range result.Workspaces {
		names = append(names, ws.Name)
	}
	return names, nil
}
