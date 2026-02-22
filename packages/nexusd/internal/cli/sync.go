package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Manage file synchronization for workspaces",
	Long:  "Control Mutagen-based bidirectional file sync between host and workspaces",
}

var syncStatusCmd = &cobra.Command{
	Use:   "status [workspace]",
	Short: "Show sync status for workspace or all workspaces",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		if len(args) == 1 {
			workspace := args[0]
			status, err := client.GetSyncStatus(workspace)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
					os.Exit(3)
				}
				os.Exit(1)
			}

			if jsonOutput {
				printJSON(status)
			} else {
				fmt.Printf("Workspace: %s\n", workspace)
				fmt.Printf("Sync State: %s\n", status.State)
				if status.SessionID != "" {
					fmt.Printf("Session ID: %s\n", status.SessionID)
				}
				if !status.LastSync.IsZero() {
					fmt.Printf("Last Sync: %s\n", status.LastSync.Format("2006-01-02 15:04:05"))
				}
				if len(status.Conflicts) > 0 {
					fmt.Printf("\nConflicts:\n")
					for _, c := range status.Conflicts {
						fmt.Printf("  - %s\n", c.Path)
					}
				}
			}
			return
		}

		result, err := client.ListWorkspaces()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if len(result.Workspaces) == 0 {
			fmt.Println("No workspaces found")
			return
		}

		if jsonOutput {
			type syncInfo struct {
				Workspace string      `json:"workspace"`
				State     string      `json:"state"`
				LastSync  interface{} `json:"last_sync"`
			}
			infos := make([]syncInfo, 0)
			for _, ws := range result.Workspaces {
				status, err := client.GetSyncStatus(ws.Name)
				if err != nil {
					continue
				}
				infos = append(infos, syncInfo{
					Workspace: ws.Name,
					State:     status.State,
					LastSync:  status.LastSync,
				})
			}
			printJSON(infos)
			return
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "WORKSPACE\tSTATE\tLAST SYNC\n")
		for _, ws := range result.Workspaces {
			status, err := client.GetSyncStatus(ws.Name)
			if err != nil {
				continue
			}
			lastSync := "-"
			if !status.LastSync.IsZero() {
				lastSync = status.LastSync.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", ws.Name, status.State, lastSync)
		}
		w.Flush()
	},
}

var syncPauseCmd = &cobra.Command{
	Use:   "pause <workspace>",
	Short: "Pause file sync for workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		workspace := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		err := client.PauseSync(workspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"workspace":"%s","state":"paused"}`, workspace)
		} else {
			fmt.Printf("Paused sync for workspace %s\n", workspace)
		}
	},
}

var syncResumeCmd = &cobra.Command{
	Use:   "resume <workspace>",
	Short: "Resume file sync for workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		workspace := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		err := client.ResumeSync(workspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"workspace":"%s","state":"resumed"}`, workspace)
		} else {
			fmt.Printf("Resumed sync for workspace %s\n", workspace)
		}
	},
}

var syncFlushCmd = &cobra.Command{
	Use:   "flush <workspace>",
	Short: "Force sync (flush changes immediately)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		workspace := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		err := client.FlushSync(workspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Printf(`{"workspace":"%s","state":"flushed"}`, workspace)
		} else {
			fmt.Printf("Flushed sync for workspace %s\n", workspace)
		}
	},
}

var syncListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sync sessions",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		result, err := client.ListWorkspaces()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if len(result.Workspaces) == 0 {
			fmt.Println("No workspaces found")
			return
		}

		type syncInfo struct {
			Workspace string     `json:"workspace"`
			State     string     `json:"state"`
			SessionID string     `json:"session_id,omitempty"`
			LastSync  *time.Time `json:"last_sync,omitempty"`
		}
		infos := make([]syncInfo, 0)

		for _, ws := range result.Workspaces {
			status, err := client.GetSyncStatus(ws.Name)
			if err != nil {
				continue
			}
			infos = append(infos, syncInfo{
				Workspace: ws.Name,
				State:     status.State,
				SessionID: status.SessionID,
				LastSync:  &status.LastSync,
			})
		}

		if jsonOutput {
			printJSON(infos)
			return
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "WORKSPACE\tSTATE\tSESSION ID\n")
		for _, info := range infos {
			fmt.Fprintf(w, "%s\t%s\t%s\n", info.Workspace, info.State, info.SessionID)
		}
		w.Flush()
		fmt.Fprintf(os.Stderr, "\nTotal: %d sync session(s)\n", len(infos))
	},
}

func init() {
	syncCmd.AddCommand(syncStatusCmd)
	syncCmd.AddCommand(syncPauseCmd)
	syncCmd.AddCommand(syncResumeCmd)
	syncCmd.AddCommand(syncFlushCmd)
	syncCmd.AddCommand(syncListCmd)
}
