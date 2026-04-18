package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/daemonclient"
	"github.com/spf13/cobra"
)

func init() {
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the Nexus workspace daemon",
	}

	daemonCmd.AddCommand(
		daemonStartCmd(),
		daemonStopCmd(),
		daemonRestartCmd(),
		daemonStatusCmd(),
		daemonLogsCmd(),
	)

	rootCmd.AddCommand(daemonCmd)
}

// ── start ─────────────────────────────────────────────────────────────────────

func daemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon (no-op if already running)",
		RunE: func(cmd *cobra.Command, args []string) error {
			port := daemonPort()
			worktreeRoot, _ := daemonclient.ProcessWorktreeRoot(".")
			if daemonclient.IsRunning(port) {
				pid := daemonReadPID(port)
				fmt.Fprintf(cmd.OutOrStdout(), "daemon already running  port=:%d  pid=%s\n", port, pid)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "starting daemon on port :%d…\n", port)
			if err := daemonclient.EnsureRunningForWorktree(port, "", "", worktreeRoot); err != nil {
				return fmt.Errorf("daemon start: %w", err)
			}
			pid := daemonReadPID(port)
			fmt.Fprintf(cmd.OutOrStdout(), "daemon ready  port=:%d  pid=%s\n", port, pid)
			return nil
		},
	}
}

// ── stop ──────────────────────────────────────────────────────────────────────

func daemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			port := daemonPort()
			if !daemonclient.IsRunning(port) {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon is not running")
				return nil
			}
			pid := daemonReadPID(port)
			fmt.Fprintf(cmd.OutOrStdout(), "stopping daemon  pid=%s…\n", pid)
			if err := daemonclient.Stop(port); err != nil {
				return fmt.Errorf("daemon stop: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			return nil
		},
	}
}

// ── restart ───────────────────────────────────────────────────────────────────

func daemonRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Stop (if running) then start the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			port := daemonPort()
			worktreeRoot, _ := daemonclient.ProcessWorktreeRoot(".")
			if daemonclient.IsRunning(port) {
				pid := daemonReadPID(port)
				fmt.Fprintf(cmd.OutOrStdout(), "stopping daemon  pid=%s…\n", pid)
				if err := daemonclient.Stop(port); err != nil {
					return fmt.Errorf("daemon restart (stop): %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "starting daemon on port :%d…\n", port)
			if err := daemonclient.EnsureRunningForWorktree(port, "", "", worktreeRoot); err != nil {
				return fmt.Errorf("daemon restart (start): %w", err)
			}
			pid := daemonReadPID(port)
			fmt.Fprintf(cmd.OutOrStdout(), "daemon ready  port=:%d  pid=%s\n", port, pid)
			return nil
		},
	}
}

// ── status ────────────────────────────────────────────────────────────────────

func daemonStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status (port, PID, version, uptime)",
		RunE: func(cmd *cobra.Command, args []string) error {
			port := daemonPort()
			out := cmd.OutOrStdout()

			running := daemonclient.IsRunning(port)
			pidStr := daemonReadPID(port)
			pid, _ := strconv.Atoi(pidStr)
			var versionJSON string
			if running {
				versionJSON = daemonFetchVersion(port) // raw JSON from /version
			}

			// Parse protocol from the raw /version JSON body.
			protocol := 0
			versionStr := ""
			if versionJSON != "" {
				var info struct {
					Version  string `json:"version"`
					Protocol int    `json:"protocol"`
				}
				if err := json.Unmarshal([]byte(versionJSON), &info); err == nil {
					protocol = info.Protocol
					versionStr = info.Version
				}
			}

			uptime := daemonUptime(pidStr)
			runDir, _ := daemonclient.RunDir()
			logPath := ""
			if runDir != "" {
				logPath = filepath.Join(runDir, "daemon.log")
			}

			if jsonOut {
				type statusOutput struct {
					Running  bool   `json:"running"`
					Port     int    `json:"port"`
					PID      int    `json:"pid,omitempty"`
					Version  string `json:"version,omitempty"`
					Protocol int    `json:"protocol,omitempty"`
					Uptime   string `json:"uptime,omitempty"`
					Log      string `json:"log,omitempty"`
				}
				st := statusOutput{
					Running:  running,
					Port:     port,
					PID:      pid,
					Version:  versionStr,
					Protocol: protocol,
					Uptime:   uptime,
					Log:      logPath,
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}

			// Human-readable output (preserve existing format)
			if !running {
				fmt.Fprintf(out, "status:   stopped\nport:     :%d\n", port)
				if logPath != "" {
					fmt.Fprintf(out, "log:      %s\n", logPath)
				}
				return nil
			}
			fmt.Fprintf(out, "status:   running\n")
			fmt.Fprintf(out, "port:     :%d\n", port)
			fmt.Fprintf(out, "pid:      %s\n", pidStr)
			if versionStr != "" {
				fmt.Fprintf(out, "version:  %s\n", versionStr)
			}
			if protocol > 0 {
				fmt.Fprintf(out, "protocol: %d\n", protocol)
			}
			if uptime != "" {
				fmt.Fprintf(out, "uptime:   %s\n", uptime)
			}
			if logPath != "" {
				fmt.Fprintf(out, "log:      %s\n", logPath)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "render machine-readable output")
	return cmd
}

// ── logs ──────────────────────────────────────────────────────────────────────

func daemonLogsCmd() *cobra.Command {
	var follow bool
	var lines int

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print (and optionally follow) the daemon log",
		RunE: func(cmd *cobra.Command, args []string) error {
			runDir, err := daemonclient.RunDir()
			if err != nil {
				return fmt.Errorf("daemon logs: run dir: %w", err)
			}
			logPath := filepath.Join(runDir, "daemon.log")

			f, err := os.Open(logPath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("daemon logs: log file not found at %s (has the daemon been started?)", logPath)
				}
				return fmt.Errorf("daemon logs: %w", err)
			}
			defer f.Close()

			out := cmd.OutOrStdout()

			if !follow {
				// Print the last `lines` lines then exit.
				if err := printLastN(f, out, lines); err != nil {
					return fmt.Errorf("daemon logs: %w", err)
				}
				return nil
			}

			// Seek to end minus a reasonable window, then stream.
			if _, err := f.Seek(0, io.SeekEnd); err == nil {
				// Start streaming from now; user can ctrl-c to stop.
			}
			fmt.Fprintf(out, "(following %s — press ctrl-c to stop)\n\n", logPath)
			scanner := bufio.NewScanner(f)
			for {
				for scanner.Scan() {
					fmt.Fprintln(out, scanner.Text())
				}
				time.Sleep(200 * time.Millisecond)
				// Re-init scanner so it picks up new bytes.
				scanner = bufio.NewScanner(f)
			}
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output (like tail -f)")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "Number of recent lines to print (without -f)")
	return cmd
}

// ── helpers ───────────────────────────────────────────────────────────────────

func daemonReadPID(port int) string {
	// Ask lsof for the live PID first — PID files may be stale.
	out, err := exec.Command("lsof", "-i", fmt.Sprintf(":%d", port), "-sTCP:LISTEN", "-t").Output()
	if err == nil {
		if pid := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]); pid != "" {
			return pid
		}
	}
	// Fall back to PID file.
	runDir, err := daemonclient.RunDir()
	if err != nil {
		return "unknown"
	}
	for _, name := range []string{
		fmt.Sprintf("daemon-%d.pid", port),
		"daemon.pid",
	} {
		data, err := os.ReadFile(filepath.Join(runDir, name))
		if err == nil {
			pid := strings.TrimSpace(string(data))
			if pid != "" && pidIsAlive(pid) {
				return pid
			}
		}
	}
	return "unknown"
}

// pidIsAlive returns true if the process with the given PID string is running.
func pidIsAlive(pidStr string) bool {
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// os.FindProcess always succeeds on Unix; send signal 0 to check liveness.
	err = proc.Signal(os.Signal(syscall.Signal(0)))
	return err == nil
}

func daemonFetchVersion(port int) string {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/version", port))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return strings.TrimSpace(string(raw))
}

func daemonUptime(pidStr string) string {
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return ""
	}
	startedAt, err := daemonclient.DaemonProcessStartedAt(pid)
	if err != nil {
		return ""
	}
	d := time.Since(startedAt).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// printLastN reads f from the start and writes the last n lines to w.
func printLastN(f *os.File, w io.Writer, n int) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	scanner := bufio.NewScanner(f)
	var buf []string
	for scanner.Scan() {
		buf = append(buf, scanner.Text())
		if len(buf) > n {
			buf = buf[len(buf)-n:]
		}
	}
	for _, line := range buf {
		fmt.Fprintln(w, line)
	}
	return scanner.Err()
}
