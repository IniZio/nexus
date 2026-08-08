package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

func init() {
	Register(Command{
		Name:    "snapshot",
		Summary: "Manage snapshots (create|list|rm)",
		Run:     runSnapshot,
	})
}

// ── JSON data types ──────────────────────────────────────────────────────────

type snapshotInfoJSON struct {
	ID        string `json:"id"`
	SandboxID string `json:"sandbox_id"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
}

type snapshotCreatedDataJSON struct {
	Snapshot snapshotInfoJSON `json:"snapshot"`
}

type snapshotListDataJSON struct {
	Snapshots []snapshotInfoJSON `json:"snapshots"`
}

type snapshotRemovedDataJSON struct {
	ID string `json:"id"`
}

// ── dispatcher ───────────────────────────────────────────────────────────────

func runSnapshot(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "snapshot: missing subcommand; usage: snapshot <create|list|rm>"}
	}
	verb := args[0]
	verbArgs := args[1:]
	switch verb {
	case "create":
		return runSnapshotCreate(ctx, verbArgs, out)
	case "list":
		return runSnapshotList(ctx, verbArgs, out)
	case "rm":
		return runSnapshotRm(ctx, verbArgs, out)
	default:
		return &UsageError{Msg: fmt.Sprintf("snapshot: unknown subcommand %q; usage: snapshot <create|list|rm>", verb)}
	}
}

// ── snapshot create <ref> ────────────────────────────────────────────────────

func runSnapshotCreate(ctx context.Context, args []string, out *Output, svcs ...*service.Service) error {
	if len(args) != 1 {
		return &UsageError{Msg: "snapshot create: usage: snapshot create <ref>"}
	}
	ref := args[0]

	var svc *service.Service
	if len(svcs) > 0 && svcs[0] != nil {
		svc = svcs[0]
	} else {
		var err error
		svc, err = newSnapshotService()
		if err != nil {
			return errSandbox("snapshot create", err)
		}
	}

	snap, err := svc.Snapshot(ctx, ref)
	if err != nil {
		return errSandbox("snapshot create", err)
	}

	out.EmitSuccess("snapshot.created", snapshotCreatedDataJSON{
		Snapshot: snapshotInfoJSON{
			ID:        string(snap.ID),
			SandboxID: snap.SandboxID.String(),
			Kind:      string(snap.Kind),
			Size:      snap.Size,
		},
	}, fmt.Sprintf("snapshot %s created from sandbox %s", snap.ID, snap.SandboxID))
	return nil
}

// ── snapshot list ────────────────────────────────────────────────────────────

func runSnapshotList(ctx context.Context, args []string, out *Output, svcs ...*service.Service) error {
	if len(args) != 0 {
		return &UsageError{Msg: "snapshot list: usage: snapshot list"}
	}

	var svc *service.Service
	if len(svcs) > 0 && svcs[0] != nil {
		svc = svcs[0]
	} else {
		var err error
		svc, err = newSnapshotService()
		if err != nil {
			return errSandbox("snapshot list", err)
		}
	}

	snaps, err := svc.SnapshotList()
	if err != nil {
		return errSandbox("snapshot list", err)
	}

	infos := make([]snapshotInfoJSON, 0, len(snaps))
	for _, s := range snaps {
		infos = append(infos, snapshotInfoJSON{
			ID:        string(s.ID),
			SandboxID: s.SandboxID.String(),
			Kind:      string(s.Kind),
			Size:      s.Size,
		})
	}

	out.EmitSuccess("snapshot.list", snapshotListDataJSON{Snapshots: infos},
		fmt.Sprintf("%d snapshot(s)", len(infos)))
	return nil
}

// ── snapshot rm <id> ─────────────────────────────────────────────────────────

func runSnapshotRm(ctx context.Context, args []string, out *Output, svcs ...*service.Service) error {
	if len(args) != 1 {
		return &UsageError{Msg: "snapshot rm: usage: snapshot rm <id>"}
	}
	id := artifact.SnapshotID(args[0])

	var svc *service.Service
	if len(svcs) > 0 && svcs[0] != nil {
		svc = svcs[0]
	} else {
		var err error
		svc, err = newSnapshotService()
		if err != nil {
			return errSandbox("snapshot rm", err)
		}
	}

	if err := svc.SnapshotRemove(ctx, id); err != nil {
		return errSandbox("snapshot rm", err)
	}

	out.EmitSuccess("snapshot.removed", snapshotRemovedDataJSON{ID: string(id)},
		fmt.Sprintf("snapshot %s removed", id))
	return nil
}

// ── service construction ─────────────────────────────────────────────────────

// newSnapshotService builds a Service wired with an artifact store for
// snapshot persistence. The store root follows XDG_STATE_HOME (via
// store.DefaultRoot); snapshots are persisted under <root>/snapshots.
func newSnapshotService() (*service.Service, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, fmt.Errorf("snapshot: resolve state directory: %w", err)
	}
	aStore, err := artifact.NewStore(filepath.Join(root, "snapshots"))
	if err != nil {
		return nil, fmt.Errorf("snapshot: open artifact store: %w", err)
	}
	svc, err := newSandboxService()
	if err != nil {
		return nil, err
	}
	return svc.WithArtifacts(aStore), nil
}
