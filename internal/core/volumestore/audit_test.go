package volumestore

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/audit"
)

func newAuditStore(t *testing.T) (*VolumeStore, string) {
	t.Helper()
	stateRoot := t.TempDir()
	root := filepath.Join(stateRoot, "volumes")
	return New(root), stateRoot
}

func readAuditLines(t *testing.T, stateRoot string) []audit.Event {
	t.Helper()
	f, err := os.Open(audit.Path(stateRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close() //nolint:errcheck
	var events []audit.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev audit.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("parse audit line: %v", err)
		}
		events = append(events, ev)
	}
	return events
}

func TestAudit_Rm(t *testing.T) {
	s, stateRoot := newAuditStore(t)
	ctx := audit.WithReason(context.Background(), "test-rm")
	mustCreateDir(t, s, "vol1")

	if err := s.Rm(ctx, "vol1"); err != nil {
		t.Fatalf("Rm: %v", err)
	}

	events := readAuditLines(t, stateRoot)
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event; got %d", len(events))
	}
	ev := events[0]
	if ev.Op != "volume.rm" {
		t.Errorf("op = %q; want volume.rm", ev.Op)
	}
	if len(ev.Volumes) != 1 || ev.Volumes[0] != "vol1" {
		t.Errorf("volumes = %v; want [vol1]", ev.Volumes)
	}
	if ev.Reason != "test-rm" {
		t.Errorf("reason = %q; want test-rm", ev.Reason)
	}
	if ev.Err != "" {
		t.Errorf("err = %q; want empty", ev.Err)
	}
}

func TestAudit_Trash(t *testing.T) {
	s, stateRoot := newAuditStore(t)
	ctx := audit.WithReason(context.Background(), "test-trash")
	mustCreateDir(t, s, "vol1")

	entry, err := s.Trash(ctx, "vol1")
	if err != nil {
		t.Fatalf("Trash: %v", err)
	}

	events := readAuditLines(t, stateRoot)
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event; got %d", len(events))
	}
	ev := events[0]
	if ev.Op != "volume.trash" {
		t.Errorf("op = %q; want volume.trash", ev.Op)
	}
	if len(ev.Volumes) != 2 || ev.Volumes[0] != "vol1" || ev.Volumes[1] != entry {
		t.Errorf("volumes = %v; want [vol1 %s]", ev.Volumes, entry)
	}
	if ev.Reason != "test-trash" {
		t.Errorf("reason = %q; want test-trash", ev.Reason)
	}
}

func TestAudit_Restore(t *testing.T) {
	s, stateRoot := newAuditStore(t)
	mustCreateDir(t, s, "vol1")
	entry, err := s.Trash(context.Background(), "vol1")
	if err != nil {
		t.Fatalf("Trash: %v", err)
	}

	ctx := audit.WithReason(context.Background(), "test-restore")
	if _, err := s.Restore(ctx, entry, ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	var restoreEvents []audit.Event
	for _, ev := range readAuditLines(t, stateRoot) {
		if ev.Op == "volume.restore" {
			restoreEvents = append(restoreEvents, ev)
		}
	}
	if len(restoreEvents) != 1 {
		t.Fatalf("expected 1 restore audit event; got %d", len(restoreEvents))
	}
	ev := restoreEvents[0]
	if len(ev.Volumes) != 2 || ev.Volumes[0] != entry || ev.Volumes[1] != "vol1" {
		t.Errorf("volumes = %v; want [%s vol1]", ev.Volumes, entry)
	}
	if ev.Reason != "test-restore" {
		t.Errorf("reason = %q; want test-restore", ev.Reason)
	}
}

func TestAudit_ExpireTrash(t *testing.T) {
	s, stateRoot := newAuditStore(t)
	mustCreateDir(t, s, "oldvol")

	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	trashNow = func() time.Time { return old }
	t.Cleanup(func() { trashNow = time.Now })

	entry, err := s.Trash(context.Background(), "oldvol")
	if err != nil {
		t.Fatalf("Trash: %v", err)
	}

	ctx := audit.WithReason(context.Background(), "test-expire")
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	deleted, err := s.ExpireTrash(ctx, TrashGrace, now)
	if err != nil {
		t.Fatalf("ExpireTrash: %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("expected 1 deleted; got %v", deleted)
	}

	var expireEvents []audit.Event
	for _, ev := range readAuditLines(t, stateRoot) {
		if ev.Op == "volume.trash_expire" {
			expireEvents = append(expireEvents, ev)
		}
	}
	if len(expireEvents) != 1 {
		t.Fatalf("expected 1 expire audit event; got %d", len(expireEvents))
	}
	ev := expireEvents[0]
	if len(ev.Volumes) != 1 || ev.Volumes[0] != entry {
		t.Errorf("volumes = %v; want [%s]", ev.Volumes, entry)
	}
	if ev.Reason != "test-expire" {
		t.Errorf("reason = %q; want test-expire", ev.Reason)
	}
}

func TestAudit_ExpireTrash_NoneDeleted_NoEvent(t *testing.T) {
	s, stateRoot := newAuditStore(t)
	mustCreateDir(t, s, "newvol")

	fresh := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	trashNow = func() time.Time { return fresh }
	t.Cleanup(func() { trashNow = time.Now })

	if _, err := s.Trash(context.Background(), "newvol"); err != nil {
		t.Fatalf("Trash: %v", err)
	}

	ctx := audit.WithReason(context.Background(), "test-no-expire")
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	deleted, err := s.ExpireTrash(ctx, TrashGrace, now)
	if err != nil {
		t.Fatalf("ExpireTrash: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("expected 0 deleted; got %v", deleted)
	}

	for _, ev := range readAuditLines(t, stateRoot) {
		if ev.Op == "volume.trash_expire" {
			t.Errorf("unexpected trash_expire audit event when nothing expired")
		}
	}
}
