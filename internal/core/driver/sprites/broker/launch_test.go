package broker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

type fakeLauncher struct {
	calls int
	// onSpawn simulates the broker writing its own broker.json.
	onSpawn func(id string) error
}

func (f *fakeLauncher) Spawn(_ context.Context, id string) error {
	f.calls++
	if f.onSpawn != nil {
		return f.onSpawn(id)
	}
	return nil
}

// startSleep starts a harmless helper and returns its recorded State.
func startSleep(t *testing.T) (State, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	// Right after fork the child still shows the parent's argv; wait for exec.
	var argv []string
	for i := 0; i < 200; i++ {
		a, err := ProcessCmdline(cmd.Process.Pid)
		if err == nil && len(a) > 0 && filepath.Base(a[0]) == "sleep" {
			argv = a
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if argv == nil {
		t.Fatal("helper cmdline never became sleep")
	}
	return State{PID: cmd.Process.Pid, Cmdline: argv, Started: time.Now()}, cmd
}

func TestEnsureNoopWhenAlive(t *testing.T) {
	dir, id := t.TempDir(), domain.NewSandboxID().String()
	st, _ := startSleep(t)
	if err := WriteState(dir, id, st); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLauncher{}
	m := &Manager{StateDir: dir, Launcher: fl}
	if err := m.Ensure(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if fl.calls != 0 {
		t.Fatalf("spawned %d times for live broker", fl.calls)
	}
}

func TestEnsureSpawnsWhenStale(t *testing.T) {
	dir, id := t.TempDir(), domain.NewSandboxID().String()
	// Stale: dead pid recorded.
	if err := WriteState(dir, id, State{PID: 1 << 22, Cmdline: []string{"gone"}}); err != nil {
		t.Fatal(err)
	}
	live, _ := startSleep(t)
	fl := &fakeLauncher{onSpawn: func(id string) error { return WriteState(dir, id, live) }}
	m := &Manager{StateDir: dir, Launcher: fl, ReadyTimeout: 2 * time.Second}
	if err := m.Ensure(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if fl.calls != 1 {
		t.Fatalf("spawn calls = %d, want 1", fl.calls)
	}
}

func TestEnsureTimesOut(t *testing.T) {
	dir, id := t.TempDir(), domain.NewSandboxID().String()
	m := &Manager{StateDir: dir, Launcher: &fakeLauncher{}, ReadyTimeout: 100 * time.Millisecond}
	if err := m.Ensure(context.Background(), id); err == nil {
		t.Fatal("want timeout error")
	}
}

func TestStopRefusesMismatchedCmdline(t *testing.T) {
	dir, id := t.TempDir(), domain.NewSandboxID().String()
	st, cmd := startSleep(t)
	st.Cmdline = []string{"/usr/bin/nexus", "__sprites-broker", id} // recycled-PID scenario
	if err := WriteState(dir, id, st); err != nil {
		t.Fatal(err)
	}
	m := &Manager{StateDir: dir, StopGrace: 200 * time.Millisecond}
	if err := m.Stop(id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if !pidAlive(cmd.Process.Pid) || cmd.ProcessState != nil {
		t.Fatal("unrelated process was signalled")
	}
	if _, err := ReadState(dir, id); err == nil {
		t.Fatal("stale state not removed")
	}
}

func TestStopTerminatesVerifiedProcess(t *testing.T) {
	dir, id := t.TempDir(), domain.NewSandboxID().String()
	st, cmd := startSleep(t)
	if err := WriteState(dir, id, st); err != nil {
		t.Fatal(err)
	}
	m := &Manager{StateDir: dir, StopGrace: 3 * time.Second}
	if err := m.Stop(id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for pidAlive(cmd.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pidAlive(cmd.Process.Pid) {
		t.Fatal("verified process still alive")
	}
	if _, err := ReadState(dir, id); err == nil {
		t.Fatal("state not removed")
	}
}

func TestStopMissingStateIsNoop(t *testing.T) {
	m := &Manager{StateDir: t.TempDir()}
	if err := m.Stop(domain.NewSandboxID().String()); err != nil {
		t.Fatal(err)
	}
}

func TestExecLauncherRejectsBadID(t *testing.T) {
	dir := t.TempDir()
	l := ExecLauncher{Exe: "/bin/true", StateDir: dir}
	if err := l.Spawn(context.Background(), "../../x"); err == nil {
		t.Fatal("want error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("state dir touched: %v", entries)
	}
}

func TestExecLauncherSpawnsHelper(t *testing.T) {
	dir, id := t.TempDir(), domain.NewSandboxID().String()
	// /bin/true ignores args; harmless stand-in for the nexus binary.
	l := ExecLauncher{Exe: "/bin/true", StateDir: dir}
	if err := l.Spawn(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "sprites", id, "broker.log")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, dir) {
		t.Fatal("log outside state dir")
	}
}
