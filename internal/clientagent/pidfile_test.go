package clientagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeTempPid(t *testing.T, pid int) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "local-agent.pid")
	if err := os.WriteFile(f, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func waitVisible(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pid %d never appeared in ps", pid)
}

func startManaged(t *testing.T, name string, args ...string) (pid int, exited <-chan struct{}) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ch := make(chan struct{})
	go func() { cmd.Wait(); close(ch) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-ch })
	return cmd.Process.Pid, ch
}

func withProcs(t *testing.T, entries []procEntry) {
	t.Helper()
	orig := listProcesses
	listProcesses = func(_ context.Context) ([]procEntry, error) {
		return entries, nil
	}
	t.Cleanup(func() { listProcesses = orig })
}

func TestReapPrevious_KillsMatchingArgv(t *testing.T) {
	pid, exited := startManaged(t, "bash", "-c", "exec -a 'nexus-client herdr local-agent-startup' sleep 60")
	waitVisible(t, pid)
	withProcs(t, []procEntry{{Pid: pid, Argv: "nexus-client herdr local-agent-startup"}})

	if err := ReapPrevious(context.Background(), writeTempPid(t, pid)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-exited:
	case <-time.After(7 * time.Second):
		t.Fatal("process still alive after ReapPrevious")
	}
}

func TestReapPrevious_SkipsNonMatchingArgv(t *testing.T) {
	pid, exited := startManaged(t, "sleep", "60")
	waitVisible(t, pid)
	withProcs(t, []procEntry{{Pid: pid, Argv: "sleep 60"}})

	if err := ReapPrevious(context.Background(), writeTempPid(t, pid)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-exited:
		t.Fatal("sleep process was killed by ReapPrevious but argv did not match")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestReapPrevious_ToleratesMissingPidfile(t *testing.T) {
	withProcs(t, nil)
	if err := ReapPrevious(context.Background(), filepath.Join(t.TempDir(), "no-such.pid")); err != nil {
		t.Fatal(err)
	}
}

func TestReapPrevious_TolerateStalePid(t *testing.T) {
	withProcs(t, nil)
	if err := ReapPrevious(context.Background(), writeTempPid(t, 2000000)); err != nil {
		t.Fatal(err)
	}
}

func TestReapPrevious_ToleratesUnparseablePidfile(t *testing.T) {
	withProcs(t, nil)
	f := filepath.Join(t.TempDir(), "local-agent.pid")
	os.WriteFile(f, []byte("not-a-pid\n"), 0o644)
	if err := ReapPrevious(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

func TestReapPrevious_NoPidfile_KillsOrphanMatchingArgv(t *testing.T) {
	pid, exited := startManaged(t, "bash", "-c", "exec -a 'nexus-client herdr local-agent-startup' sleep 60")
	waitVisible(t, pid)
	withProcs(t, []procEntry{{Pid: pid, Argv: "nexus-client herdr local-agent-startup"}})

	missing := filepath.Join(t.TempDir(), "no-such.pid")
	if err := ReapPrevious(context.Background(), missing); err != nil {
		t.Fatal(err)
	}

	select {
	case <-exited:
	case <-time.After(7 * time.Second):
		t.Fatal("orphan process still alive after ReapPrevious with no pidfile")
	}
}

func TestReapPrevious_NoPidfile_SkipsForeignArgv(t *testing.T) {
	pid, exited := startManaged(t, "sleep", "60")
	waitVisible(t, pid)
	withProcs(t, []procEntry{{Pid: pid, Argv: "sleep 60"}})

	missing := filepath.Join(t.TempDir(), "no-such.pid")
	if err := ReapPrevious(context.Background(), missing); err != nil {
		t.Fatal(err)
	}

	select {
	case <-exited:
		t.Fatalf("foreign-argv process pid %d was killed by ReapPrevious", pid)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestReapPrevious_NeverScansRealTable(t *testing.T) {
	entries, err := listProcesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("default test lister must return nothing; got %d entries", len(entries))
	}
}

func TestWritePidfile_WritesCurrentPid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "local-agent.pid")
	if err := WritePidfile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || got != os.Getpid() {
		t.Fatalf("expected pid %d, got %q", os.Getpid(), data)
	}
}
