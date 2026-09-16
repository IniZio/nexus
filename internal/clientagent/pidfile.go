package clientagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func DefaultPidPath() string {
	if d := os.Getenv("HERDR_PLUGIN_STATE_DIR"); d != "" {
		return filepath.Join(d, "local-agent.pid")
	}
	return filepath.Join(StateDir(), "local-agent.pid")
}

func WritePidfile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pidfile mkdir: %w", err)
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

func isAgentProcess(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return false
	}
	argv := strings.TrimSpace(string(out))
	return strings.Contains(argv, "nexus3-client") &&
		strings.Contains(argv, "herdr") &&
		strings.Contains(argv, "local-agent-startup")
}

func ReapPrevious(_ context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return nil
	}
	if !isAgentProcess(pid) {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "nexus3-client: reaping previous instance pid %d\n", pid)
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "nexus3-client: SIGTERM pid %d: %v\n", pid, err)
		return nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if !isAgentProcess(pid) {
			fmt.Fprintf(os.Stderr, "nexus3-client: previous instance pid %d exited\n", pid)
			return nil
		}
	}
	fmt.Fprintf(os.Stderr, "nexus3-client: SIGKILL pid %d (did not exit within 5s)\n", pid)
	_ = proc.Signal(syscall.SIGKILL)
	return nil
}
