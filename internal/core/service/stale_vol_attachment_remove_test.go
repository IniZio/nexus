package service_test

// TestRemove_ClearsStaleVolumeAttachment reproduces TBD-PD-22: a volume whose
// meta.json Attachments list names a sandbox id that is NOT in sb.MountedVolumes
// blocked "volume rm" with "volume in use: attached to <removed-sandbox-id>".
//
// Setup: create a volume, attach it directly (simulating a crash that wrote the
// attachment but did not commit the sandbox record's MountedVolumes), then create
// a sandbox whose MountedVolumes is EMPTY (the diverged state). Remove the
// sandbox; the stale attachment must be gone and vs.Rm must succeed.
//
// MUTATION: revert the TBD-PD-22 sweep in Service.Remove (the s.volumes.List()
// block) and this test goes RED with "volume in use: attached to <id>".

import (
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

func TestRemove_ClearsStaleVolumeAttachment(t *testing.T) {
	ctx := context.Background()

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	vs := volumestore.New(t.TempDir())
	svc := service.New(st, fake.New(), lifecycle.New()).WithVolumes(vs)

	// Create the named volume.
	volName := "stale-attach-vol"
	if _, err := vs.Create(ctx, volName, volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("vs.Create: %v", err)
	}

	// Create the sandbox with EMPTY MountedVolumes — this is the diverged state:
	// the attachment record exists in meta.json but the sandbox record does not
	// list the volume (crash between Attach write and sandbox record commit).
	sbID := domain.NewSandboxID()
	sb := domain.Sandbox{
		ID:             sbID,
		Name:           "stale-attach-sb",
		Project:        "test",
		State:          domain.Created,
		MountedVolumes: nil, // intentionally empty
	}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("st.Create: %v", err)
	}

	// Inject the stale attachment directly into the volume meta.json.
	if err := vs.Attach(ctx, volName, sbID.String()); err != nil {
		t.Fatalf("vs.Attach (inject stale record): %v", err)
	}

	// Sanity: volume reports it is attached, so Rm must refuse now.
	if rmErr := vs.Rm(ctx, volName); rmErr == nil {
		t.Fatal("expected vs.Rm to refuse while stale attachment present; got nil")
	}

	// Remove the sandbox. This should sweep the stale attachment.
	if err := svc.Remove(ctx, sbID.String()); err != nil {
		t.Fatalf("svc.Remove: %v", err)
	}

	// Criterion 1: no attachment naming the removed sandbox survives.
	recs, listErr := vs.List()
	if listErr != nil {
		t.Fatalf("vs.List: %v", listErr)
	}
	for _, rec := range recs {
		for _, att := range rec.Attachments {
			if att.SandboxID == sbID.String() {
				t.Errorf("stale attachment for sandbox %s still present on volume %s after Remove", sbID, rec.Name)
			}
		}
	}

	// Criterion 2: volume rm must now succeed without --include-detached.
	if err := vs.Rm(ctx, volName); err != nil {
		t.Errorf("vs.Rm after Remove: %v", err)
	}
}
