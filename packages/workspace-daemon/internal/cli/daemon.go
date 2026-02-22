package cli

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

func getDefaultWorkspaceDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/nexus-workspaces"
	}
	return filepath.Join(home, ".nexus", "workspaces")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the nexus daemon",
	Run: func(cmd *cobra.Command, args []string) {
		port, _ := cmd.Flags().GetInt("port")
		workspaceDir, _ := cmd.Flags().GetString("workspace-dir")
		token := daemonToken

		if token == "" {
			token = os.Getenv("NEXUS_TOKEN")
			if token == "" {
				fmt.Println("Error: --daemon-token is required (or set NEXUS_TOKEN env var)")
				os.Exit(1)
			}
		}

		daemonPath := findDaemonBinary()
		if daemonPath == "" {
			fmt.Println("Error: nexusd binary not found")
			os.Exit(1)
		}

		execCmd := exec.Command(daemonPath,
			"--port", fmt.Sprint(port),
			"--workspace-dir", workspaceDir,
			"--token", token,
		)
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr
		execCmd.Stdin = os.Stdin

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

		go func() {
			<-sigChan
			execCmd.Process.Kill()
		}()

		fmt.Printf("Starting daemon on port %d...\n", port)
		if err := execCmd.Run(); err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	serveCmd.Flags().IntP("port", "p", 8080, "Port to listen on")
	serveCmd.Flags().StringP("workspace-dir", "w", getDefaultWorkspaceDir(), "Workspace directory path")
}

func findDaemonBinary() string {
	paths := []string{
		"./nexusd",
		"/usr/local/bin/nexusd",
		"/usr/bin/nexusd",
		os.Getenv("HOME") + "/bin/nexusd",
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	output, err := exec.Command("which", "nexusd").Output()
	if err == nil {
		return string(output)
	}

	return ""
}
