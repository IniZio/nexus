package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// seedMotiveSandbox creates a sandbox record in svc's store associated with
// motiveID (via Labels["motive"]) and returns it.
func seedMotiveSandbox(t *testing.T, ctx context.Context, svc *Service, motiveID string, state domain.State) domain.Sandbox {
	t.Helper()
	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    fmt.Sprintf("ms-test-%s", motiveID),
		Project: "test",
		Labels:  map[string]string{"motive": motiveID},
		State:   state,
	}
	if err := svc.store.Create(ctx, sb); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	return sb
}

// makeSparseRaw creates a sparse .raw disk file for the given sandbox ID in
// stateRoot/disks/. The file is sparse (os.Truncate), so its apparent size
// is size bytes but its allocated size is 0 (or one block).
func makeSparseRaw(t *testing.T, stateRoot string, id domain.SandboxID, size int64) string {
	t.Helper()
	disksDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(disksDir, 0o755); err != nil {
		t.Fatalf("mkdir disks: %v", err)
	}
	path := filepath.Join(disksDir, id.String()+".raw")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create raw: %v", err)
	}
	f.Close()
	if err := os.Truncate(path, size); err != nil {
		t.Fatalf("truncate raw: %v", err)
	}
	return path
}

// msNoopProbe is a sandboxProbeFn that always returns -1/nil (unknown uptime,
// no error). Used in tests that do not exercise the probe path.
// Named with "ms" prefix to avoid colliding with the msNoopProbe variable in
// create_test.go (which has type ProbeFunc, a different function signature).
func msNoopProbe(_ domain.Sandbox) (int64, error) { return -1, nil }

// msErrorProbeFor returns a sandboxProbeFn that injects a failure for the
// listed sandbox IDs. All other sandboxes receive msNoopProbe results.
// Used to test the partial-failure (unreachable) degradation path.
func msErrorProbeFor(ids ...domain.SandboxID) sandboxProbeFn {
	set := make(map[domain.SandboxID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(sb domain.Sandbox) (int64, error) {
		if set[sb.ID] {
			return -1, errors.New("injected probe failure: sandbox unreachable")
		}
		return -1, nil
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestMotiveStatus_Empty verifies that a motive with no sandboxes produces an
// empty report with zero totals and exits without error (M1-AC2).
func TestMotiveStatus_Empty(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())
	tmpRoot := t.TempDir()
	idx := NewResourceIndex(IndexConfig{StateRoot: tmpRoot, SocketDir: tmpRoot})

	report, err := svc.labelStatus(ctx, "motive","no-such-motive", idx, msNoopProbe)
	if err != nil {
		t.Fatalf("motiveStatus returned error: %v", err)
	}
	if len(report.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(report.Rows))
	}
	if report.TotalAllocBytes != 0 {
		t.Errorf("expected 0 total bytes, got %d", report.TotalAllocBytes)
	}
	if report.LeakedCount != 0 {
		t.Errorf("expected 0 leaked, got %d", report.LeakedCount)
	}
}

// TestMotiveStatus_Degradation verifies M1-AC2: when one sandbox's probe
// fails (unreachable), the other rows still render and the overall call
// returns nil error. The error is confined to the failing row.
func TestMotiveStatus_Degradation(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())
	tmpRoot := t.TempDir()
	idx := NewResourceIndex(IndexConfig{StateRoot: tmpRoot, SocketDir: tmpRoot})

	const motive = "degradation-motive"
	sb1 := seedMotiveSandbox(t, ctx, svc, motive, domain.Running)
	sb2 := seedMotiveSandbox(t, ctx, svc, motive, domain.Running)

	// sb1 is unreachable; sb2 is fine.
	probe := msErrorProbeFor(sb1.ID)

	report, err := svc.labelStatus(ctx, "motive",motive, idx, probe)
	if err != nil {
		t.Fatalf("motiveStatus returned unexpected error: %v", err)
	}

	if len(report.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(report.Rows))
	}

	// Find which row is which.
	rowFor := make(map[domain.SandboxID]SandboxStatusRow, 2)
	for _, r := range report.Rows {
		rowFor[r.Sandbox.ID] = r
	}

	row1, ok := rowFor[sb1.ID]
	if !ok {
		t.Fatal("row for sb1 missing from report")
	}
	if row1.Err == nil {
		t.Error("sb1 row: expected non-nil Err for unreachable sandbox, got nil")
	}

	row2, ok := rowFor[sb2.ID]
	if !ok {
		t.Fatal("row for sb2 missing from report")
	}
	if row2.Err != nil {
		t.Errorf("sb2 row: unexpected error: %v", row2.Err)
	}
}

// TestMotiveStatus_AllocatedBytes verifies that the allocated bytes are
// sourced from stat(2).Blocks*512 (not apparent size).
//
// Sparse ext4 images can be 100 GiB apparent but only a few MiB allocated.
// The test creates a sparse file (os.Truncate to 1 GiB) and checks that the
// reported size is much smaller than 1 GiB.
func TestMotiveStatus_AllocatedBytes(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())
	stateRoot := t.TempDir()
	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot, SocketDir: stateRoot})

	const motive = "alloc-bytes-motive"
	sb := seedMotiveSandbox(t, ctx, svc, motive, domain.Stopped)

	// Create a sparse 1 GiB raw disk file in the ResourceIndex's disks dir.
	const apparentSize = 1 << 30 // 1 GiB apparent
	rawPath := makeSparseRaw(t, stateRoot, sb.ID, apparentSize)
	_ = rawPath

	report, err := svc.labelStatus(ctx, "motive",motive, idx, msNoopProbe)
	if err != nil {
		t.Fatalf("motiveStatus: %v", err)
	}
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(report.Rows))
	}
	row := report.Rows[0]
	// Allocated bytes must be much less than apparent size for a sparse file.
	// On Linux a zero-filled sparse file has Blocks == 0 or a very small
	// number (metadata overhead). The apparent size is 1 GiB; allocated must
	// be < 1 MiB to pass.
	const maxAcceptableAllocBytes = 1 << 20 // 1 MiB
	if row.AllocatedBytes >= maxAcceptableAllocBytes {
		t.Errorf("allocated bytes = %d (>= 1 MiB): looks like apparent size was used instead of stat.Blocks*512", row.AllocatedBytes)
	}
	// The total must match the row.
	if report.TotalAllocBytes != row.AllocatedBytes {
		t.Errorf("TotalAllocBytes %d != row.AllocatedBytes %d", report.TotalAllocBytes, row.AllocatedBytes)
	}
}

// TestMotiveStatus_LeakedCount verifies that resources whose OwnerID has no
// store record are counted as leaked.
func TestMotiveStatus_LeakedCount(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())
	stateRoot := t.TempDir()
	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot, SocketDir: stateRoot})

	const motive = "leak-motive"
	// One known sandbox in the motive.
	sb := seedMotiveSandbox(t, ctx, svc, motive, domain.Stopped)
	makeSparseRaw(t, stateRoot, sb.ID, 0) // owned disk

	// Two orphan disk files (no store record).
	orphan1 := domain.NewSandboxID()
	orphan2 := domain.NewSandboxID()
	makeSparseRaw(t, stateRoot, orphan1, 0)
	makeSparseRaw(t, stateRoot, orphan2, 0)

	report, err := svc.labelStatus(ctx, "motive",motive, idx, msNoopProbe)
	if err != nil {
		t.Fatalf("motiveStatus: %v", err)
	}
	if report.LeakedCount != 2 {
		t.Errorf("LeakedCount = %d, want 2", report.LeakedCount)
	}
}

// TestMotiveStatus_MultiMotiveIsolation verifies that sandboxes from a
// different motive do not appear in the report.
func TestMotiveStatus_MultiMotiveIsolation(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())
	tmpRoot := t.TempDir()
	idx := NewResourceIndex(IndexConfig{StateRoot: tmpRoot, SocketDir: tmpRoot})

	seedMotiveSandbox(t, ctx, svc, "motive-A", domain.Running)
	seedMotiveSandbox(t, ctx, svc, "motive-A", domain.Running)
	seedMotiveSandbox(t, ctx, svc, "motive-B", domain.Stopped)

	report, err := svc.labelStatus(ctx, "motive","motive-A", idx, msNoopProbe)
	if err != nil {
		t.Fatalf("motiveStatus: %v", err)
	}
	if len(report.Rows) != 2 {
		t.Errorf("expected 2 rows for motive-A, got %d", len(report.Rows))
	}
	for _, r := range report.Rows {
		if r.Sandbox.Labels["motive"] != "motive-A" {
			t.Errorf("unexpected sandbox from motive %q in motive-A report", r.Sandbox.Labels["motive"])
		}
	}
}

// TestFormatUptime verifies the human-readable uptime formatting.
func TestFormatUptime(t *testing.T) {
	tests := []struct {
		seconds int64
		want    string
	}{
		{-1, "-"},
		{0, "0s"},
		{45, "45s"},
		{60, "1m0s"},
		{90, "1m30s"},
		{3600, "1h0m"},
		{3661, "1h1m"},
		{7323, "2h2m"},
	}
	for _, tt := range tests {
		got := FormatUptime(tt.seconds)
		if got != tt.want {
			t.Errorf("FormatUptime(%d) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

// TestFormatBytes verifies the human-readable byte-count formatting.
func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{1 << 30, "1.0 GiB"},
		{int64(1.5 * (1 << 30)), "1.5 GiB"},
	}
	for _, tt := range tests {
		got := FormatBytes(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

// TestPidUptimeSeconds_Self verifies that pidUptimeSeconds returns a
// positive value for the current process (which is definitely alive).
func TestPidUptimeSeconds_Self(t *testing.T) {
	u, err := pidUptimeSeconds(os.Getpid())
	if err != nil {
		t.Fatalf("pidUptimeSeconds(self): %v", err)
	}
	if u < 0 {
		t.Errorf("pidUptimeSeconds(self) = %d, want >= 0", u)
	}
}
