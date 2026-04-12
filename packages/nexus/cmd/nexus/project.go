package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
	"github.com/spf13/cobra"
)

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage projects",
}

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all projects",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		listProjects()
	},
}

var projectShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show project details and workspaces",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		showProject(strings.TrimSpace(args[0]))
	},
}

var projectRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a project and all its workspaces",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		removeProject(strings.TrimSpace(args[0]))
	},
}

func init() {
	projectCmd.AddCommand(projectListCmd, projectShowCmd, projectRemoveCmd)
	rootCmd.AddCommand(projectCmd)
}

func listProjects() {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus project list: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var result struct {
		Projects []projectmgr.Project `json:"projects"`
	}
	if err := daemonRPC(conn, "project.list", map[string]any{}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus project list: %v\n", err)
		os.Exit(1)
	}

	if len(result.Projects) == 0 {
		fmt.Println("no projects")
		return
	}

	fmt.Printf("%-24s  %-20s  %s\n", "ID", "NAME", "PRIMARY REPO")
	fmt.Printf("%-24s  %-20s  %s\n",
		"------------------------", "--------------------", "------------------------------")
	for _, p := range result.Projects {
		fmt.Printf("%-24s  %-20s  %s\n", p.ID, p.Name, p.PrimaryRepo)
	}
}

func showProject(id string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus project show: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var result struct {
		Project    projectmgr.Project       `json:"project"`
		Workspaces []workspacemgr.Workspace `json:"workspaces"`
	}
	if err := daemonRPC(conn, "project.get", map[string]any{"id": id}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus project show: %v\n", err)
		os.Exit(1)
	}

	p := result.Project
	fmt.Printf("ID:             %s\n", p.ID)
	fmt.Printf("Name:           %s\n", p.Name)
	fmt.Printf("Primary Repo:   %s\n", p.PrimaryRepo)
	fmt.Printf("Root Path:      %s\n", p.RootPath)
	fmt.Printf("Created:        %s\n", p.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("\nWorkspaces (%d):\n", len(result.Workspaces))

	if len(result.Workspaces) == 0 {
		fmt.Println("  (none)")
		return
	}

	fmt.Printf("  %-36s  %-20s  %-10s  %s\n", "ID", "NAME", "STATE", "REF")
	fmt.Printf("  %-36s  %-20s  %-10s  %s\n",
		"------------------------------------", "--------------------",
		"----------", "----------")
	for _, ws := range result.Workspaces {
		fmt.Printf("  %-36s  %-20s  %-10s  %s\n",
			ws.ID, ws.WorkspaceName, ws.State, ws.Ref)
	}
}

func removeProject(id string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus project remove: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var result struct {
		Removed bool `json:"removed"`
	}
	if err := daemonRPC(conn, "project.remove", map[string]any{"id": id}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus project remove: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("removed project %s\n", id)
}
