package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// TestNewSandboxService_RemoveDetachesNamedVolume proves that a Service built by
// newSandboxService() (the SOLE CLI constructor for rm and herdr teardown) has
// its volume store wired, so Service.Remove clears the volume's attachment lease.
//
// Regression: newSandboxService previously never called WithVolumes — only the
// create path did, and only when --mount-named was present. So `nexus3 rm` (and
// herdr worktree teardown) ran Remove with Service.volumes == nil, which silently
// SKIPS the detach loop. The attachment stayed in the volume's meta.json, the
// volume was stuck "in use: attached to <dead-sandbox>", and the next create that
// reused the name (e.g. re-creating a worktree sandbox — its docker disk is named
// from the stable handle) could not attach it.
//
// MUTATION PROOF: delete the WithVolumes(...) line in newSandboxService and this
// test goes RED (attachment survives the Remove).
//
// The cli package's TestMain points XDG_STATE_HOME at a per-run temp dir, so
// store.DefaultRoot() here resolves to the SAME root newSandboxService() opens.
// Unique names keep this isolated from other tests sharing that root.
func TestNewSandboxService_RemoveDetachesNamedVolume(t *testing.T) {
	ctx := context.Background()

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	vs := volumestore.New(filepath.Join(root, "volumes"))

	// kind=dir so the test needs no mke2fs; the detach path is kind-agnostic.
	const volName = "rm-detach-regression-vol"
	if _, err := vs.Create(ctx, volName, volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("vs.Create: %v", err)
	}

	sbID := domain.NewSandboxID()
	sb := domain.Sandbox{
		ID:      sbID,
		Name:    "rm-detach-regression-sb",
		Project: "test",
		State:   domain.Created,
		MountedVolumes: []domain.VolumeAttachment{
			{Name: volName, GuestPath: "/var/lib/docker", Kind: string(volumestore.KindDir)},
		},
	}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("st.Create sandbox record: %v", err)
	}
	if err := vs.Attach(ctx, volName, sbID.String()); err != nil {
		t.Fatalf("vs.Attach: %v", err)
	}

	// Sanity: the volume is attached before removal.
	if rec, err := vs.Get(volName); err != nil {
		t.Fatalf("vs.Get (pre): %v", err)
	} else if len(rec.Attachments) != 1 {
		t.Fatalf("pre-condition: want 1 attachment, got %d", len(rec.Attachments))
	}

	svc, err := newSandboxService()
	if err != nil {
		t.Fatalf("newSandboxService: %v", err)
	}
	if err := svc.Remove(ctx, sbID.String()); err != nil {
		t.Fatalf("svc.Remove: %v", err)
	}

	// The attachment lease must be released so the volume is reusable.
	rec, err := vs.Get(volName)
	if err != nil {
		t.Fatalf("vs.Get (post): %v", err)
	}
	if len(rec.Attachments) != 0 {
		t.Errorf("volume still attached after rm — detach was skipped (Service.volumes not wired): %+v", rec.Attachments)
	}
}
