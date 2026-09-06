package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// reapFailureFixture builds a state root holding one orphan shadow disk in a
// directory that cannot be written to, so os.Remove fails with EACCES.
func reapFailureFixture(t *testing.T) (store.Store, *service.ResourceIndex, string) {
	t.Helper()
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(disksDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(disksDir, "stuck.shadow.node_modules.ext4")
	if err := os.WriteFile(path, []byte("fake-ext4-data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(disksDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(disksDir, 0o700) })

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	return st, idx, path
}

// TBD-PD-37, CLI half. `reap --apply` used to print "Deleted N resource(s)"
// and exit 0 whether or not every orphan was actually reclaimed, so a script
// could not distinguish a complete cleanup from a partial one. The failure
// must reach BOTH the human output and the exit code.
func TestRunReapFull_DeletionFailureExitsNonZero(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block unlink")
	}
	st, idx, path := reapFailureFixture(t)
	out, _, errBuf := newTestOutput(false)

	err := runReapFull(context.Background(), st, idx, true /*apply*/, out, service.ReapOptions{ProcDir: t.TempDir()})
	if err == nil {
		t.Fatal("reap --apply returned nil despite failing to delete an orphan")
	}
	var exitErr *ExitCodeError
	if !asExitCodeError(err, &exitErr) {
		t.Fatalf("error is %T, want *ExitCodeError so the shell sees a failure: %v", err, err)
	}
	if exitErr.Code == 0 {
		t.Error("exit code is 0; a partial reclamation must not look like success")
	}
	stderr := errBuf.String()
	if !strings.Contains(stderr, "FAILED to reclaim") {
		t.Errorf("stderr does not report the failure:\n%s", stderr)
	}
	if !strings.Contains(stderr, filepath.Base(path)) {
		t.Errorf("stderr does not name the path that failed:\n%s", stderr)
	}
}

// The JSON surface must carry the same fact, or a machine caller reading only
// the envelope would still conclude success.
func TestRunReapFull_JSONCarriesFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block unlink")
	}
	st, idx, path := reapFailureFixture(t)
	out, stdout, _ := newTestOutput(true)

	if err := runReapFull(context.Background(), st, idx, true /*apply*/, out, service.ReapOptions{ProcDir: t.TempDir()}); err == nil {
		t.Fatal("JSON mode returned nil despite a deletion failure")
	}

	var env struct {
		Data struct {
			Deleted []string `json:"deleted"`
			Failed  []struct {
				Path   string `json:"path"`
				Reason string `json:"reason"`
			} `json:"failed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\n%s", err, stdout.String())
	}
	if len(env.Data.Failed) != 1 {
		t.Fatalf("failed[] has %d entries, want 1:\n%s", len(env.Data.Failed), stdout.String())
	}
	if env.Data.Failed[0].Path != path {
		t.Errorf("failed[0].path = %q, want %q", env.Data.Failed[0].Path, path)
	}
	if env.Data.Failed[0].Reason == "" {
		t.Error("failed[0].reason is empty")
	}
	for _, d := range env.Data.Deleted {
		if d == path {
			t.Error("path appears in deleted[] despite failing")
		}
	}
}

// asExitCodeError is errors.As specialised, kept local so the test reads
// without importing errors for a single call.
func asExitCodeError(err error, target **ExitCodeError) bool {
	for err != nil {
		if e, ok := err.(*ExitCodeError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
