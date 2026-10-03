//go:build linux

package hubclient

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func fakeProc(t *testing.T, procs map[int][2]string) {
	t.Helper()
	root := t.TempDir()
	for pid, v := range procs {
		d := filepath.Join(root, fmt.Sprint(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		stat := fmt.Sprintf("%d (%s) S %s 0 0\n", pid, v[0], v[1])
		if err := os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "comm"), []byte(v[0]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := ProcRoot
	ProcRoot = root
	t.Cleanup(func() { ProcRoot = old })
}

func TestFindAgentAncestorMatch(t *testing.T) {
	fakeProc(t, map[int][2]string{
		1:   {"init", "0"},
		100: {"claude", "1"},
		200: {"bash", "100"},
		300: {"hook", "200"},
	})
	pid, comm, err := FindAgentAncestor(300, nil)
	if err != nil || pid != 100 || comm != "claude" {
		t.Fatalf("got %d %q %v", pid, comm, err)
	}
}

func TestFindAgentAncestorSkipsOwnPid(t *testing.T) {
	fakeProc(t, map[int][2]string{
		1:   {"init", "0"},
		100: {"codex", "1"},
		300: {"claude", "100"},
	})
	pid, _, err := FindAgentAncestor(300, nil)
	if err != nil || pid != 100 {
		t.Fatalf("got %d %v", pid, err)
	}
}

func TestFindAgentAncestorNotFound(t *testing.T) {
	fakeProc(t, map[int][2]string{
		1:   {"init", "0"},
		200: {"bash", "1"},
		300: {"hook", "200"},
	})
	if _, _, err := FindAgentAncestor(300, nil); err == nil {
		t.Fatal("want error")
	}
}

func TestFindAgentAncestorCycle(t *testing.T) {
	fakeProc(t, map[int][2]string{
		200: {"bash", "300"},
		300: {"hook", "200"},
	})
	if _, _, err := FindAgentAncestor(300, nil); err == nil {
		t.Fatal("want cycle error")
	}
}

func TestFindAgentAncestorCustomComms(t *testing.T) {
	fakeProc(t, map[int][2]string{
		1:   {"init", "0"},
		100: {"my agent) x", "1"},
		200: {"claude", "100"},
		300: {"hook", "200"},
	})
	pid, comm, err := FindAgentAncestor(300, []string{"my agent) x"})
	if err != nil || pid != 100 || comm != "my agent) x" {
		t.Fatalf("got %d %q %v", pid, comm, err)
	}
}
