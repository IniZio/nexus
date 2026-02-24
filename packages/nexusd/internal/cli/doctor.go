package cli

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"

	"github.com/nexus/nexus/packages/nexusd/internal/config"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check nexus setup and diagnose issues",
	Long: `Check nexus setup and diagnose common issues.

This command verifies:
  - CLI version is set
  - Config directory exists
  - Docker is available
  - Daemon is running and reachable

Example:
  nexus doctor`,
	Run: func(cmd *cobra.Command, args []string) {
		checks := []struct {
			name  string
			check func() error
		}{
			{"CLI version", checkVersion},
			{"Config directory", checkConfigDir},
			{"Docker availability", checkDocker},
			{"Daemon connectivity", checkDaemon},
		}

		allPassed := true
		for _, c := range checks {
			fmt.Printf("Checking %s... ", c.name)
			if err := c.check(); err != nil {
				fmt.Printf("\033[31mFAIL\033[0m: %v\n", err)
				allPassed = false
			} else {
				fmt.Printf("\033[32mOK\033[0m\n")
			}
		}

		if !allPassed {
			fmt.Println("\nSome checks failed. Please fix the issues above.")
			os.Exit(1)
		}

		fmt.Println("\nAll checks passed!")
	},
}

func checkVersion() error {
	if version == "" {
		return fmt.Errorf("version not set")
	}
	return nil
}

func checkConfigDir() error {
	dir := config.DirPath()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("config directory does not exist: %s", dir)
	}
	return nil
}

func checkDocker() error {
	conn, err := net.Dial("tcp", "localhost:2375")
	if err == nil {
		conn.Close()
		return nil
	}

	conn, err = net.Dial("tcp", "localhost:2376")
	if err == nil {
		conn.Close()
		return nil
	}

	output, err := exec.Command("docker", "ps").Output()
	if err != nil {
		return fmt.Errorf("docker not available")
	}
	if len(output) == 0 {
		return fmt.Errorf("docker returned empty output")
	}
	return nil
}

func checkDaemon() error {
	cfg := getConfig()
	addr := fmt.Sprintf("%s:%d", cfg.Daemon.Host, cfg.Daemon.Port)

	conn, err := net.Dial("tcp", addr)
	if err == nil {
		conn.Close()
		return nil
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/health", addr))
	if err != nil {
		return fmt.Errorf("daemon not running at %s", addr)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}

	return nil
}
