package portfwd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func makeMgrWithSock(sock string, resps []runResp) (*Manager, *[][]string) {
	var calls [][]string
	fw := &Forwarder{
		ControlPath: sock,
		SSHHost:     testHost,
		Run:         seqRun(&calls, resps),
		ListenFunc:  noopListen,
	}
	return NewManager(fw), &calls
}

func TestManagerLoadsPersistedApplied(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.ctl")
	data := `{"entries":[{"sandbox_id":"abc","port":3000}]}`
	if err := os.WriteFile(sock+".applied.json", []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr, _ := makeMgrWithSock(sock, nil)
	entries := mgr.Applied()
	if len(entries) != 1 || entries[0].SandboxID != "abc" || entries[0].Port != 3000 {
		t.Fatalf("want [{abc 3000}], got %v", entries)
	}
}

func TestManagerPersistsOnApply(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.ctl")
	mgr, _ := makeMgrWithSock(sock, []runResp{
		{code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sock + ".applied.json")
	if err != nil {
		t.Fatalf("applied file not written: %v", err)
	}
	var pa persistedApplied
	if err := json.Unmarshal(raw, &pa); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pa.Entries) != 1 || pa.Entries[0].SandboxID != "abc" || pa.Entries[0].Port != 3000 {
		t.Fatalf("want [{abc 3000}], got %v", pa.Entries)
	}
	if pa.Entries[0].Kind != "local" {
		t.Fatalf("want kind=local in persisted entry, got %q", pa.Entries[0].Kind)
	}
}

func TestManagerRestartCancelsOrphans(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.ctl")
	data := `{"entries":[{"sandbox_id":"abc","port":3000}]}`
	if err := os.WriteFile(sock+".applied.json", []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr, calls := makeMgrWithSock(sock, []runResp{{code: 0}})
	if err := mgr.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	foundCancel := false
	for _, argv := range *calls {
		if len(argv) >= 3 && argv[1] == "-O" && argv[2] == "cancel" {
			foundCancel = true
		}
	}
	if !foundCancel {
		t.Fatalf("orphan port must be cancelled on restart; calls=%v", *calls)
	}
	if len(mgr.Applied()) != 0 {
		t.Fatalf("Applied must be empty after orphan cancel, got %v", mgr.Applied())
	}
}
