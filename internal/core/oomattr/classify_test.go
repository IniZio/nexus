package oomattr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/IniZio/nexus/internal/hubclient"
)

func TestClassify(t *testing.T) {
	base := Snapshot{BootID: "b", VmstatOOMKill: 1}
	kill := Signal{Signal: 9}
	cases := []struct {
		name  string
		after Snapshot
		sig   Signal
		want  hubclient.Cause
	}{
		{"none", base, kill, hubclient.CauseUnknown},
		{"host vmstat", Snapshot{BootID: "b", VmstatOOMKill: 2}, kill, hubclient.CauseHostOOM},
		{"scope kill without oom", Snapshot{BootID: "b", VmstatOOMKill: 1, ScopeOOMKill: 1}, kill, hubclient.CauseHostOOM},
		{"scope oom", Snapshot{BootID: "b", VmstatOOMKill: 2, ScopeOOM: 1, ScopeOOMKill: 1}, kill, hubclient.CauseSliceOOM},
		{"ancestor oom", Snapshot{BootID: "b", VmstatOOMKill: 2, AncestorOOM: 1}, kill, hubclient.CauseSliceOOM},
		{"ancestor kill only is not slice", Snapshot{BootID: "b", VmstatOOMKill: 1, AncestorOOMKill: 3}, kill, hubclient.CauseUnknown},
		{"sigterm", Snapshot{BootID: "b", VmstatOOMKill: 2, ScopeOOM: 1}, Signal{Signal: 15}, hubclient.CauseUnknown},
		{"exit code", Snapshot{BootID: "b", VmstatOOMKill: 2, ScopeOOM: 1}, Signal{Code: 1}, hubclient.CauseUnknown},
		{"adopted slice", Snapshot{BootID: "b", AncestorOOM: 1}, UnknownExit, hubclient.CauseSliceOOM},
		{"adopted host", Snapshot{BootID: "b", VmstatOOMKill: 2}, UnknownExit, hubclient.CauseHostOOM},
		{"adopted none", base, UnknownExit, hubclient.CauseUnknown},
		{"clean known exit with concurrent oom", Snapshot{BootID: "b", VmstatOOMKill: 2, ScopeOOM: 1}, Signal{}, hubclient.CauseUnknown},
	}
	for _, c := range cases {
		if got := classify(base, c.after, c.sig); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestBackfill(t *testing.T) {
	started := json.RawMessage(`{"boot_id":"b","oom_kill":3}`)
	if got := backfill(started, Snapshot{BootID: "b", VmstatOOMKill: 4}); got != hubclient.CauseHostOOM {
		t.Errorf("risen: %s", got)
	}
	if got := backfill(started, Snapshot{BootID: "b", VmstatOOMKill: 3}); got != hubclient.CauseUnknown {
		t.Errorf("flat: %s", got)
	}
	if got := backfill(started, Snapshot{BootID: "x", VmstatOOMKill: 9}); got != hubclient.CauseUnknown {
		t.Errorf("reboot: %s", got)
	}
	if got := backfill(json.RawMessage(`bad`), Snapshot{BootID: "b"}); got != hubclient.CauseUnknown {
		t.Errorf("bad: %s", got)
	}
}

func TestTakeFromFixture(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	root := t.TempDir()
	w := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("proc/sys/kernel/random/boot_id", "abc-123\n")
	w("proc/vmstat", "nr_free_pages 5\noom_kill 7\n")
	w("proc/self/cgroup", "0::/a/b/scope\n")
	w("sys/fs/cgroup/a/b/scope/memory.events", "low 0\noom 4\noom_kill 2\noom_group_kill 1\n")
	w("sys/fs/cgroup/a/b/memory.events", "oom 1\noom_kill 3\noom_group_kill 0\n")
	w("sys/fs/cgroup/a/memory.events", "oom_kill 5\n")
	got := takeFrom(root)
	want := Snapshot{BootID: "abc-123", VmstatOOMKill: 7, ScopeOOMKill: 3, AncestorOOMKill: 8, ScopeOOM: 4, AncestorOOM: 1}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if z := takeFrom(t.TempDir()); z != (Snapshot{}) {
		t.Fatalf("missing files: %+v", z)
	}
}
