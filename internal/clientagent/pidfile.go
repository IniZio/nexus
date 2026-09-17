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

type procEntry struct {
	Pid  int
	Argv string
}

var listProcesses = func(_ context.Context) ([]procEntry, error) {
	out, err := exec.Command("ps", "-axo", "pid=,args=").Output()
	if err != nil {
		return nil, err
	}
	var entries []procEntry
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, ' ')
		if idx < 0 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line[:idx]))
		if err != nil || pid <= 0 {
			continue
		}
		entries = append(entries, procEntry{Pid: pid, Argv: line[idx+1:]})
	}
	return entries, nil
}

// matchesAgentArgv is the single identity gate; removing it must break tests.
func matchesAgentArgv(argv string) bool {
	return strings.Contains(argv, "nexus3-client") &&
		strings.Contains(argv, "herdr") &&
		strings.Contains(argv, "local-agent-startup")
}

func isAgentProcess(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return false
	}
	return matchesAgentArgv(strings.TrimSpace(string(out)))
}

func reapOnePid(pid int) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "nexus3-client: reaping previous instance pid %d\n", pid)
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "nexus3-client: SIGTERM pid %d: %v\n", pid, err)
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if !isAgentProcess(pid) {
			fmt.Fprintf(os.Stderr, "nexus3-client: previous instance pid %d exited\n", pid)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "nexus3-client: SIGKILL pid %d (did not exit within 5s)\n", pid)
	_ = proc.Signal(syscall.SIGKILL)
}

func scanAndReapOrphans(ctx context.Context, skip map[int]bool) {
	entries, err := listProcesses(ctx)
	if err != nil {
		return
	}
	self := os.Getpid()
	parent := os.Getppid()
	for _, e := range entries {
		if e.Pid == self || e.Pid == parent || skip[e.Pid] {
			continue
		}
		if !matchesAgentArgv(e.Argv) {
			continue
		}
		reapOnePid(e.Pid)
	}
}

func ReapPrevious(_ context.Context, path string) error {
	skip := map[int]bool{}

	data, err := os.ReadFile(path)
	if err == nil {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && pid > 0 && pid != os.Getpid() {
			if isAgentProcess(pid) {
				reapOnePid(pid)
			}
			skip[pid] = true
		}
	}

	scanAndReapOrphans(context.Background(), skip)
	return nil
}
