package cli

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check nexus setup and diagnose issues",
	Run: func(cmd *cobra.Command, args []string) {
		checks := []struct {
			name    string
			check   func() error
		}{
			{"CLI version", checkVersion},
			{"Go installation", checkGo},
			{"Daemon binary", checkDaemonBinary},
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

func checkGo() error {
	output, err := exec.Command("go", "version").Output()
	if err != nil {
		return fmt.Errorf("go not installed")
	}
	fmt.Printf("(%s) ", string(output[:len(output)-1]))
	return nil
}

func checkDaemonBinary() error {
	paths := []string{
		"./nexusd",
		"/usr/local/bin/nexusd",
		"/usr/bin/nexusd",
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
	}

	output, err := exec.Command("which", "nexusd").Output()
	if err == nil {
		return nil
	}

	return fmt.Errorf("nexusd binary not found")
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
	client := getClient()
	if err := client.Health(); err != nil {
		return fmt.Errorf("daemon not running at %s", apiURL)
	}

	resp, err := http.Get(apiURL + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}

	return nil
}
