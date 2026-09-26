package service

// Tests for the audit seam on Service.Remove / auditRemove.
//
// Service.Remove requires a running VM harness; constructing one without a
// real VMM is impractical (the fake driver requires a disk on disk and a
// running lifecycle machine). The helper auditRemove is therefore tested

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/audit"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/store"
)

func newAuditTestService(t *testing.T, auditTmpDir string) *Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := New(st, fake.New(), lifecycle.New())
	svc.WithAuditRoot(func() (string, error) { return auditTmpDir, nil })
	return svc
}

// TestAuditRemove_WritesIntentLine verifies that auditRemove appends a line
// with op "sandbox.remove", the sandbox id, and the reason from context.
func TestAuditRemove_WritesIntentLine(t *testing.T) {
	tmpDir := t.TempDir()
	svc := newAuditTestService(t, tmpDir)

	sb := domain.Sandbox{
		ID:   domain.NewSandboxID(),
		Name: "audit-test",
		MountedVolumes: []domain.VolumeAttachment{
			{Name: "vol-a"},
			{Name: "vol-b"},
		},
	}

	ctx := audit.WithReason(context.Background(), "test reason")
	svc.auditRemove(ctx, sb, nil)

	logPath := audit.Path(tmpDir)
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var found bool
	for scanner.Scan() {
		var ev audit.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		if ev.Op != "sandbox.remove" {
			continue
		}
		found = true
		if ev.SandboxID != sb.ID.String() {
			t.Errorf("sandbox_id: got %q, want %q", ev.SandboxID, sb.ID.String())
		}
		if ev.Reason != "test reason" {
			t.Errorf("reason: got %q, want %q", ev.Reason, "test reason")
		}
		if len(ev.Volumes) != 2 {
			t.Errorf("volumes: got %v, want [vol-a vol-b]", ev.Volumes)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	if !found {
		t.Error("no sandbox.remove line found in audit log")
	}
}

// TestAuditRemove_UnwritableRootDoesNotFail verifies that an unwritable audit
// root (simulated by pointing auditRoot at a regular file) does not prevent
// auditRemove from returning. Remove must not fail because the audit path is
// broken.
func TestAuditRemove_UnwritableRootDoesNotFail(t *testing.T) {
	tmpDir := t.TempDir()
	badRoot := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(badRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc := newAuditTestService(t, badRoot)
	sb := domain.Sandbox{ID: domain.NewSandboxID(), Name: "bad-root"}

	svc.auditRemove(context.Background(), sb, errors.New("some prior error"))
}
