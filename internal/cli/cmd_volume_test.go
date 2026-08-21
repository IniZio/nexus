package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// ── mock SandboxLister ────────────────────────────────────────────────────────

type mockVolSandboxLister struct {
	sandboxes []domain.Sandbox
}

func (m *mockVolSandboxLister) List(_ context.Context) ([]domain.Sandbox, error) {
	return m.sandboxes, nil
}

// ── helpers ────────────────────────────────────────────────────────────────────

func newVolTestOutput() (*Output, *bytes.Buffer) {
	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, false)
	return out, &buf
}

func newVolTestVolumeStore(t *testing.T) (*volumestore.VolumeStore, string) {
	t.Helper()
	root := t.TempDir()
	return volumestore.New(root), root
}

// writeVolMetaAndDisk creates a complete (non-stub) volume directory with both
// meta.json and a fake disk.ext4.
func writeVolMetaAndDisk(t *testing.T, root, name string, atts []volumestore.VolumeAttachment) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	rec := volumestore.VolumeRecord{
		Name:        name,
		Kind:        volumestore.KindDisk,
		SizeBytes:   1024,
		Attachments: atts,
		CreatedAt:   time.Now().UTC(),
	}
	data, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
		t.Fatalf("write meta.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "disk.ext4"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("write disk.ext4: %v", err)
	}
}

// ── CLI registration ──────────────────────────────────────────────────────────

// TestVolumeCreateDir_SuccessShape pins the kind=dir create success shape, and
// documents the scope exclusion identified by
// slice VOL-PREFLIGHT: nexus3 volume create intentionally does not run a
// disk-space preflight (TBD-PD-26 / commit 48d1b82).
//
// For kind=dir the allocation is a mkdir — zero disk blocks. For kind=disk,
// preallocateFile uses ftruncate (sparse — no blocks until guest writes) and
// formatExt4 (mke2fs) writes only filesystem metadata (~5% of sizeBytes).
// Neither cost is projectable from the CLI layer: there is no source artifact
// to measure. Applying service.CheckDiskSpaceBytes here would either
// over-charge sizeBytes (~10–30× the actual mke2fs footprint, blocking valid
// creates) or use a fixed metadata estimate that always passes. The TBD-PD-26
// preflight covers CreateAndBoot and Fork, where a real OCI artifact is
// copied — an immediate, measurable allocation. Volume create is outside that
// scope by design.
//
// What this test ACTUALLY asserts: that kind=dir create succeeds and emits the
// expected success shape. It does NOT assert the absence of a preflight — there
// is no observable signal for an absent call, so no test can. The rationale
// above is the record of that decision; this test is coverage for a CLI path
// that previously had none. kind=disk is omitted (requires mke2fs on PATH).
func TestVolumeCreateDir_SuccessShape(t *testing.T) {
	vs, _ := newVolTestVolumeStore(t)
	out, buf := newVolTestOutput()

	err := runVolumeCreateWith(context.Background(),
		[]string{"--kind=dir", "my-dir-vol"}, out, vs)
	if err != nil {
		t.Fatalf("runVolumeCreateWith: %v", err)
	}
	if !strings.Contains(buf.String(), "my-dir-vol") {
		t.Errorf("output should mention my-dir-vol; got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "created") {
		t.Errorf("output should contain 'created'; got: %s", buf.String())
	}
}

// TestVolumeCommandRegistered verifies that the volume noun is registered via
// the decentralised init() mechanism (acceptance criterion 4).
func TestVolumeCommandRegistered(t *testing.T) {
	cmd, ok := Lookup("volume")
	if !ok {
		t.Fatal("volume command not registered; init() must call Register")
	}
	if cmd.Name != "volume" {
		t.Errorf("cmd.Name = %q; want volume", cmd.Name)
	}
}

// ── runVolumePruneWith: 7.5(a) stub record ───────────────────────────────────

func TestCliPruneStubRecord(t *testing.T) {
	vs, root := newVolTestVolumeStore(t)

	// Write meta.json only (no backing file) = stub record.
	dir := filepath.Join(root, "stub-vol")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := volumestore.VolumeRecord{Name: "stub-vol", Kind: volumestore.KindDisk, SizeBytes: 1024, CreatedAt: time.Now()}
	data, _ := json.MarshalIndent(rec, "", "  ")
	os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644) //nolint:errcheck

	out, buf := newVolTestOutput()
	err := runVolumePruneWith(context.Background(), out, vs, &mockVolSandboxLister{}, volumestore.PruneOptions{Apply: true})
	if err != nil {
		t.Fatalf("runVolumePruneWith: %v", err)
	}

	if !strings.Contains(buf.String(), "stub-vol") {
		t.Errorf("output should mention stub-vol; got: %s", buf.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, "meta.json")); !os.IsNotExist(statErr) {
		t.Error("meta.json must be deleted after prune --apply")
	}
}

// ── runVolumePruneWith: 7.5(c) detached without --include-detached ────────────

// TestCliPruneDetachedSurvivestWithoutFlag verifies that a detached volume's
// backing file is NOT deleted when --include-detached is absent (acceptance
// criterion 3).
func TestCliPruneDetachedSurvivestWithoutFlag(t *testing.T) {
	vs, root := newVolTestVolumeStore(t)

	writeVolMetaAndDisk(t, root, "detached-vol", []volumestore.VolumeAttachment{
		{SandboxID: "dead-id", AttachedAt: time.Now()},
	})
	diskPath := filepath.Join(root, "detached-vol", "disk.ext4")

	out, buf := newVolTestOutput()
	// --apply but no --include-detached
	err := runVolumePruneWith(context.Background(), out, vs, &mockVolSandboxLister{}, volumestore.PruneOptions{Apply: true})
	if err != nil {
		t.Fatalf("runVolumePruneWith: %v", err)
	}

	// Must be reported as candidate.
	if !strings.Contains(buf.String(), "--include-detached") {
		t.Errorf("output should mention --include-detached; got: %s", buf.String())
	}
	// Backing file must survive.
	if _, statErr := os.Stat(diskPath); statErr != nil {
		t.Errorf("disk.ext4 must survive plain prune: %v", statErr)
	}
}

// ── runVolumePruneWith: dual-source records win ───────────────────────────────

// TestCliPruneDualSourceRecordsWin proves the dual-source authority rule:
// meta.json Attachments is empty (meta source says detached) but a live sandbox
// record's MountedVolumes includes the volume (records source says live).
// Records win — the volume must not be deleted (acceptance criterion 2).
func TestCliPruneDualSourceRecordsWin(t *testing.T) {
	vs, root := newVolTestVolumeStore(t)

	// meta.json: empty Attachments — meta source says detached.
	writeVolMetaAndDisk(t, root, "records-win-vol", nil)

	// Sandbox record with MountedVolumes including the volume — records say live.
	liveID := domain.NewSandboxID()
	lister := &mockVolSandboxLister{
		sandboxes: []domain.Sandbox{
			{
				ID: liveID,
				MountedVolumes: []domain.VolumeAttachment{
					{Name: "records-win-vol", GuestPath: "/mnt", Kind: "disk"},
				},
			},
		},
	}

	diskPath := filepath.Join(root, "records-win-vol", "disk.ext4")

	out, _ := newVolTestOutput()
	err := runVolumePruneWith(context.Background(), out, vs, lister, volumestore.PruneOptions{
		Apply:           true,
		IncludeDetached: true,
	})
	if err != nil {
		t.Fatalf("runVolumePruneWith: %v", err)
	}

	// Backing file must survive — records win over empty meta.json.
	if _, statErr := os.Stat(diskPath); statErr != nil {
		t.Errorf("records must block deletion: disk.ext4 must survive: %v", statErr)
	}
}

// ── JSON output shape ─────────────────────────────────────────────────────────

func TestCliPruneJSONOutput(t *testing.T) {
	vs, root := newVolTestVolumeStore(t)

	// One stub record.
	dir := filepath.Join(root, "json-vol")
	os.MkdirAll(dir, 0o755) //nolint:errcheck
	rec := volumestore.VolumeRecord{Name: "json-vol", Kind: volumestore.KindDisk, SizeBytes: 1024, CreatedAt: time.Now()}
	data, _ := json.MarshalIndent(rec, "", "  ")
	os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644) //nolint:errcheck

	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, true) // JSON mode

	err := runVolumePruneWith(context.Background(), out, vs, &mockVolSandboxLister{}, volumestore.PruneOptions{Apply: true})
	if err != nil {
		t.Fatalf("runVolumePruneWith: %v", err)
	}

	var envelope struct {
		Kind string `json:"kind"`
		Data struct {
			StubsDeleted []string `json:"stubs_deleted"`
		} `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("decode JSON: %v\nraw: %s", err, buf.String())
	}
	if envelope.Kind != "volume.prune" {
		t.Errorf("kind = %q; want volume.prune", envelope.Kind)
	}
	if len(envelope.Data.StubsDeleted) == 0 {
		t.Errorf("stubs_deleted should contain json-vol")
	}
}
