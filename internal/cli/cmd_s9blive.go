//go:build s9blive

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/IniZio/nexus/internal/core/artifact"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/supervisor"
)

// Test-only: the retired snapshot/restore/fork verbs, for scripts/s9b-regression.sh.
func init() {
	Register(Command{
		Name:    "s9b-live",
		Summary: "test-only: snapshot|restore|fork driver (build tag s9blive)",
		Hidden:  true,
		Run:     runS9bLive,
	})
}

func runS9bLive(ctx context.Context, args []string, out *Output) error {
	if len(args) < 2 {
		return &UsageError{Msg: "s9b-live: usage: snapshot <ref> | snaprm <snap-id> | restore <snap-id> [count] | fork <ref> [count]"}
	}
	count := 1
	if len(args) > 2 {
		n, err := strconv.Atoi(args[2])
		if err != nil || n < 1 {
			return &UsageError{Msg: "s9b-live: count must be a positive integer"}
		}
		count = n
	}
	root, err := store.DefaultRoot()
	if err != nil {
		return err
	}
	aStore, err := artifact.NewStore(filepath.Join(root, "snapshots"))
	if err != nil {
		return err
	}
	svc, err := newSandboxService()
	if err != nil {
		return err
	}
	svc.WithArtifacts(aStore)

	var kids []domain.Sandbox
	switch args[0] {
	case "snapshot":
		snap, err := svc.Snapshot(ctx, args[1])
		if err != nil {
			return errSandbox("snapshot", err)
		}
		fmt.Fprintf(os.Stdout, "snapshot %s size=%d\n", snap.ID, snap.Size)
		return nil
	case "snaprm":
		if err := svc.SnapshotRemove(ctx, artifact.SnapshotID(args[1])); err != nil {
			return errSandbox("snaprm", err)
		}
		fmt.Fprintf(os.Stdout, "snapshot %s removed\n", args[1])
		return nil
	case "restore":
		kids, err = svc.RestoreFromSnapshot(ctx, artifact.SnapshotID(args[1]), count)
	case "fork":
		kids, err = svc.Fork(ctx, args[1], count)
	default:
		return &UsageError{Msg: "s9b-live: unknown verb " + args[0]}
	}
	if err != nil {
		return errSandbox(args[0], err)
	}
	for _, k := range kids {
		if err := spawnChildSupervisor(ctx, svc, root, k); err != nil {
			return errSandbox(args[0], fmt.Errorf("child %s: %w", k.ID, err))
		}
		fmt.Fprintf(os.Stdout, "child %s %s\n", k.ID, k.Handle())
	}
	return nil
}

// spawnChildSupervisor starts the reacquire supervisor the retired verbs spawned.
func spawnChildSupervisor(ctx context.Context, svc *service.Service, root string, child domain.Sandbox) error {
	if child.NetnsChildPID == 0 || child.Provenance == nil {
		return nil
	}
	cfg, err := supervisor.ReadSpawnSpec(supervisor.DefaultStateDir(root, child.Provenance.ParentID))
	if err != nil {
		return err
	}
	if cfg.DiskPath != "" {
		cfg.DiskPath = filepath.Join(filepath.Dir(cfg.DiskPath), child.ID.String()+".raw")
	}
	extras := make([]string, len(cfg.ExtraDisks))
	for i, p := range cfg.ExtraDisks {
		extras[i] = cloudhypervisor.ChildExtraDiskPath(child.ID, p)
	}
	cfg.ExtraDisks = extras
	cfg.SandboxRef = child.ID.String()
	cfg.StateDir = supervisor.DefaultStateDir(root, child.ID)
	if err := supervisor.WriteSpawnSpec(cfg.StateDir, cfg); err != nil {
		return err
	}
	return spawnPersistedSupervisorReacquire(ctx, svc, child.ID, cfg.StateDir)
}
