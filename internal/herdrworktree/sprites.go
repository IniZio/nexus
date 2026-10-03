package herdrworktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/herdrout"
)

// SpriteSyncer is the slice of the sprites driver Teardown needs to bring a
// sprite's work back before the sprite is destroyed.
type SpriteSyncer interface {
	ExportWorktree(ctx context.Context, id domain.SandboxID, guestDir, hostRepoDir, branch string) (string, error)
	GuestStatus(ctx context.Context, id domain.SandboxID, guestDir string) (string, error)
	GuestAtSeed(ctx context.Context, id domain.SandboxID, guestDir string) (bool, error)
	SyncMode(id domain.SandboxID) (string, error)
	GuestUnpushed(ctx context.Context, id domain.SandboxID, guestDir string) (string, error)
}

// guardPush is the push-mode teardown guard: the sprite must be clean and its
// branch pushed; nothing is imported to the host.
func guardPush(ctx context.Context, s SpriteSyncer, id domain.SandboxID, handle string, force bool) error {
	if force {
		return nil
	}
	status, err := s.GuestStatus(ctx, id, sprites.CloneDir)
	if err != nil {
		return &SpriteUnsyncedError{Handle: handle, Detail: "cannot read sprite git status: " + err.Error()}
	}
	if strings.TrimSpace(status) != "" {
		return &SpriteUnsyncedError{Handle: handle, Detail: "uncommitted changes in sprite:\n" + strings.TrimSpace(status)}
	}
	un, err := s.GuestUnpushed(ctx, id, sprites.CloneDir)
	if err != nil {
		return &SpriteUnsyncedError{Handle: handle, Detail: "cannot check pushed state: " + err.Error()}
	}
	if un != "" {
		return &SpriteUnsyncedError{Handle: handle, Detail: "push mode: " + un + "; push the branch"}
	}
	return nil
}

func isPushMode(s SpriteSyncer, id domain.SandboxID) bool {
	m, err := s.SyncMode(id)
	return err == nil && m == sprites.SyncPush
}

// SpriteSyncFor resolves the syncer for a sandbox; ok is false when the
// sandbox is not sprites-backed.
type SpriteSyncFor func(ctx context.Context, sandboxID string) (s SpriteSyncer, id domain.SandboxID, ok bool, err error)

func defaultSpriteSync(_ context.Context, sandboxID string) (SpriteSyncer, domain.SandboxID, bool, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, domain.SandboxID{}, false, err
	}
	id, err := domain.ParseSandboxID(sandboxID)
	if err != nil {
		// list output may truncate the id: accept a unique state-dir prefix
		m, _ := filepath.Glob(filepath.Join(root, registry.Sprites, sandboxID+"*"))
		if !strings.HasPrefix(sandboxID, "sb-") || len(m) != 1 {
			return nil, id, false, nil
		}
		if id, err = domain.ParseSandboxID(filepath.Base(m[0])); err != nil {
			return nil, id, false, nil
		}
	}
	if _, err := os.Stat(filepath.Join(root, registry.Sprites, id.String(), "spec.json")); err != nil {
		return nil, id, false, nil
	}
	drv, err := registry.New(registry.Sprites, sprites.Config{StateDir: root})
	if err != nil {
		return nil, id, true, err
	}
	s, ok := drv.(SpriteSyncer)
	if !ok {
		return nil, id, true, fmt.Errorf("sprites driver cannot sync worktrees")
	}
	return s, id, true, nil
}

// SpriteUnsyncedError reports that tearing down would lose work in the sprite.
type SpriteUnsyncedError struct {
	Handle string
	Detail string
}

func (e *SpriteUnsyncedError) Error() string {
	return fmt.Sprintf("sprite sandbox %s has work that is not synced: %s\nResolve it, or retry with force to discard it.", e.Handle, e.Detail)
}

// guardSprite exports a sprite's commits to the host branch (ff-only) and
// refuses when that fails or the sprite has uncommitted changes, unless force.
func guardSprite(ctx context.Context, r Runners, herdrBin, ws, handle, sandboxID string, force bool) error {
	sel := r.SpriteSync
	if sel == nil {
		sel = defaultSpriteSync
	}
	syncer, id, ok, err := sel(ctx, sandboxID)
	if err != nil {
		if force {
			return nil
		}
		return fmt.Errorf("sprite sandbox %s: cannot verify sync state: %w", handle, err)
	}
	if !ok {
		return nil
	}
	if isPushMode(syncer, id) {
		return guardPush(ctx, syncer, id, handle, force)
	}
	wtOut, err := r.Herdr(ctx, herdrBin, "worktree", "list", "--workspace", ws, "--json")
	wtPath := ""
	if err == nil {
		wtPath = herdrout.WorktreePathByWorkspaceID(wtOut, ws)
	}
	if wtPath == "" {
		if force {
			return nil
		}
		return &SpriteUnsyncedError{Handle: handle, Detail: "host worktree path unknown, cannot export commits"}
	}
	status, err := syncer.GuestStatus(ctx, id, sprites.CloneDir)
	if err != nil {
		if force {
			return nil
		}
		return &SpriteUnsyncedError{Handle: handle, Detail: "cannot read sprite git status: " + err.Error()}
	}
	if strings.TrimSpace(status) != "" && !force {
		return &SpriteUnsyncedError{Handle: handle, Detail: "uncommitted changes in sprite:\n" + strings.TrimSpace(status)}
	}
	branch, err := r.git(ctx, "-C", wtPath, "symbolic-ref", "--short", "-q", "HEAD")
	branch = strings.TrimSpace(branch)
	if err != nil || branch == "" {
		if force {
			return nil
		}
		return &SpriteUnsyncedError{Handle: handle, Detail: "host worktree is on a detached HEAD, cannot export commits"}
	}
	if _, err := syncer.ExportWorktree(ctx, id, sprites.CloneDir, wtPath, branch); err != nil {
		if force {
			return nil
		}
		return &SpriteUnsyncedError{Handle: handle, Detail: err.Error()}
	}
	return nil
}

// guardUnboundSprite protects `sandbox rm <ref>` when no herdr workspace is
// bound: the host worktree is unknown so nothing can be exported, so the
// sprite must be clean and still at its seed commit.
func guardUnboundSprite(ctx context.Context, r Runners, ref string, force bool) error {
	if force {
		return nil
	}
	if r.SpriteSync == nil && !anySpriteState() {
		return nil
	}
	sbID := ""
	if strings.HasPrefix(ref, "sb-") {
		sbID = ref
	} else if out, err := r.Host(ctx, "sandbox", "list"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			hit := false
			for _, f := range fields {
				if f == ref {
					hit = true
				}
			}
			if !hit {
				continue
			}
			for _, f := range fields {
				if strings.HasPrefix(f, "sb-") {
					sbID = f
				}
			}
			break
		}
	}
	if sbID == "" {
		return nil
	}
	sel := r.SpriteSync
	if sel == nil {
		sel = defaultSpriteSync
	}
	syncer, id, ok, err := sel(ctx, sbID)
	if err != nil {
		return fmt.Errorf("sprite sandbox %s: cannot verify sync state: %w", ref, err)
	}
	if !ok {
		return nil
	}
	if isPushMode(syncer, id) {
		return guardPush(ctx, syncer, id, ref, false)
	}
	hint := "no herdr workspace is bound so commits cannot be exported; re-bind it or retry with force to discard"
	status, err := syncer.GuestStatus(ctx, id, sprites.CloneDir)
	if err != nil {
		return &SpriteUnsyncedError{Handle: ref, Detail: "cannot read sprite git status: " + err.Error() + "; " + hint}
	}
	if strings.TrimSpace(status) != "" {
		return &SpriteUnsyncedError{Handle: ref, Detail: "uncommitted changes in sprite:\n" + strings.TrimSpace(status) + "\n" + hint}
	}
	at, err := syncer.GuestAtSeed(ctx, id, sprites.CloneDir)
	if err != nil || !at {
		return &SpriteUnsyncedError{Handle: ref, Detail: "sprite has commits since the seed; " + hint}
	}
	return nil
}

func anySpriteState() bool {
	root, err := store.DefaultRoot()
	if err != nil {
		return false
	}
	ents, err := os.ReadDir(filepath.Join(root, registry.Sprites))
	return err == nil && len(ents) > 0
}
