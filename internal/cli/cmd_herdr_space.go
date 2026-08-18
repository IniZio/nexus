package cli

// cmd_herdr_space.go — herdr space ↔ sandbox binding store.
//
// A "herdr space" is a named herdr workspace used as the UI shell for a nexus3
// sandbox. Convention: the herdr workspace label is "nexus3:<sandbox-handle>"
// (e.g. "nexus3:demo-orca-01").
//
// The binding store persists a flat JSON array of HerdrSpaceBinding records at:
//
//	<storeRoot>/herdr-space-bindings.json
//
// Concurrent CLI invocations are serialised via an adjacent lock file:
//
//	<storeRoot>/herdr-space-bindings.lock
//
// Put enforces the 1:1 invariant: both (SpaceLabel) and (SandboxHandle) are
// unique across all bindings. A Put that would violate uniqueness replaces the
// conflicting entry rather than erroring, so re-binding is idempotent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

// HerdrSpaceBinding records the 1:1 relationship between a herdr workspace and
// a nexus3 sandbox.
type HerdrSpaceBinding struct {
	// SpaceLabel is the herdr workspace label, e.g. "nexus3:demo-orca-01".
	SpaceLabel string `json:"space_label"`
	// HerdrWorkspaceID is the opaque ID returned by herdr when the workspace
	// was created, e.g. "wB".
	HerdrWorkspaceID string `json:"herdr_workspace_id"`
	// SandboxHandle is the human-readable ref for the sandbox,
	// e.g. "orca/demo-orca-01".
	SandboxHandle string `json:"sandbox_handle"`
	// SandboxID is the stable nexus3 sandbox ID, e.g. "sb-...".
	SandboxID string `json:"sandbox_id"`
}

// ErrHerdrSpaceNotFound is returned when no matching binding exists.
var ErrHerdrSpaceNotFound = errors.New("herdr-space: binding not found")

// herdrSpaceBindingsPath returns the path to the bindings JSON file.
func herdrSpaceBindingsPath(storeRoot string) string {
	return filepath.Join(storeRoot, "herdr-space-bindings.json")
}

// herdrSpaceLockPath returns the path to the bindings lock file.
func herdrSpaceLockPath(storeRoot string) string {
	return filepath.Join(storeRoot, "herdr-space-bindings.lock")
}

// herdrSpaceWithLock acquires an exclusive flock on the bindings lock file,
// calls fn, then releases the lock. The storeRoot directory must already exist.
func herdrSpaceWithLock(ctx context.Context, storeRoot string, fn func() error) error {
	lk, err := store.OpenLock(herdrSpaceLockPath(storeRoot))
	if err != nil {
		return fmt.Errorf("herdr-space: open lock: %w", err)
	}
	defer lk.Close()

	if err := lk.Exclusive(ctx); err != nil {
		return fmt.Errorf("herdr-space: acquire lock: %w", err)
	}
	defer lk.Unlock() //nolint:errcheck

	return fn()
}

// herdrSpaceReadAll reads all bindings from disk without locking.
// Returns an empty slice when the file does not exist yet.
func herdrSpaceReadAll(storeRoot string) ([]HerdrSpaceBinding, error) {
	data, err := os.ReadFile(herdrSpaceBindingsPath(storeRoot))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("herdr-space: read bindings: %w", err)
	}
	var bindings []HerdrSpaceBinding
	if err := json.Unmarshal(data, &bindings); err != nil {
		return nil, fmt.Errorf("herdr-space: unmarshal bindings: %w", err)
	}
	return bindings, nil
}

// herdrSpaceWriteAll persists bindings to disk atomically (temp+rename).
func herdrSpaceWriteAll(storeRoot string, bindings []HerdrSpaceBinding) error {
	data, err := json.MarshalIndent(bindings, "", "  ")
	if err != nil {
		return fmt.Errorf("herdr-space: marshal bindings: %w", err)
	}

	target := herdrSpaceBindingsPath(storeRoot)
	tmp, err := os.CreateTemp(storeRoot, ".herdr-space-bindings-*.json.tmp")
	if err != nil {
		return fmt.Errorf("herdr-space: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { os.Remove(tmpName) }() // no-op after successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("herdr-space: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("herdr-space: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("herdr-space: rename temp file: %w", err)
	}
	return nil
}

// HerdrSpacePut stores a binding, enforcing the 1:1 invariant.
//
// If an existing binding has the same SpaceLabel or the same SandboxHandle, it
// is removed before the new binding is inserted. This allows re-binding without
// error.
func HerdrSpacePut(ctx context.Context, storeRoot string, b HerdrSpaceBinding) error {
	if err := os.MkdirAll(storeRoot, 0700); err != nil {
		return fmt.Errorf("herdr-space: ensure store dir: %w", err)
	}
	return herdrSpaceWithLock(ctx, storeRoot, func() error {
		bindings, err := herdrSpaceReadAll(storeRoot)
		if err != nil {
			return err
		}
		// Remove any entry that conflicts on either key.
		filtered := bindings[:0:0]
		for _, existing := range bindings {
			if existing.SpaceLabel == b.SpaceLabel || existing.SandboxHandle == b.SandboxHandle {
				continue // drop conflicting entry
			}
			filtered = append(filtered, existing)
		}
		filtered = append(filtered, b)
		return herdrSpaceWriteAll(storeRoot, filtered)
	})
}

// HerdrSpaceGetByLabel returns the binding whose SpaceLabel matches label.
func HerdrSpaceGetByLabel(ctx context.Context, storeRoot string, label string) (HerdrSpaceBinding, error) {
	if err := ctx.Err(); err != nil {
		return HerdrSpaceBinding{}, err
	}
	bindings, err := herdrSpaceReadAll(storeRoot)
	if err != nil {
		return HerdrSpaceBinding{}, err
	}
	for _, b := range bindings {
		if b.SpaceLabel == label {
			return b, nil
		}
	}
	return HerdrSpaceBinding{}, ErrHerdrSpaceNotFound
}

// HerdrSpaceGetByHandle returns the binding whose SandboxHandle matches handle.
func HerdrSpaceGetByHandle(ctx context.Context, storeRoot string, handle string) (HerdrSpaceBinding, error) {
	if err := ctx.Err(); err != nil {
		return HerdrSpaceBinding{}, err
	}
	bindings, err := herdrSpaceReadAll(storeRoot)
	if err != nil {
		return HerdrSpaceBinding{}, err
	}
	for _, b := range bindings {
		if b.SandboxHandle == handle {
			return b, nil
		}
	}
	return HerdrSpaceBinding{}, ErrHerdrSpaceNotFound
}

// HerdrSpaceList returns all bindings in insertion order.
func HerdrSpaceList(ctx context.Context, storeRoot string) ([]HerdrSpaceBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return herdrSpaceReadAll(storeRoot)
}

// HerdrSpaceDelete removes the binding identified by SpaceLabel.
func HerdrSpaceDelete(ctx context.Context, storeRoot string, label string) error {
	return herdrSpaceWithLock(ctx, storeRoot, func() error {
		bindings, err := herdrSpaceReadAll(storeRoot)
		if err != nil {
			return err
		}
		n := len(bindings)
		filtered := bindings[:0:0]
		for _, b := range bindings {
			if b.SpaceLabel != label {
				filtered = append(filtered, b)
			}
		}
		if len(filtered) == n {
			return ErrHerdrSpaceNotFound
		}
		return herdrSpaceWriteAll(storeRoot, filtered)
	})
}

// ── space-keyed sandbox lifecycle ────────────────────────────────────────────

// HerdrSpaceSandboxService is the subset of *service.Service required by the
// herdr-space lifecycle helpers. *service.Service satisfies this interface
// automatically; tests supply a lightweight fake.
type HerdrSpaceSandboxService interface {
	Pause(ctx context.Context, ref string) (domain.Sandbox, error)
	Resume(ctx context.Context, ref string) (domain.Sandbox, error)
	Remove(ctx context.Context, ref string) error
}

// HerdrSpacePauseByLabel pauses the sandbox bound to label.
// Returns ErrHerdrSpaceNotFound when no binding exists for label.
func HerdrSpacePauseByLabel(ctx context.Context, svc HerdrSpaceSandboxService, storeRoot, label string) error {
	b, err := HerdrSpaceGetByLabel(ctx, storeRoot, label)
	if err != nil {
		return err
	}
	if _, err := svc.Pause(ctx, b.SandboxHandle); err != nil {
		return fmt.Errorf("herdr-space: pause sandbox %q: %w", b.SandboxHandle, err)
	}
	return nil
}

// HerdrSpaceResumeByLabel resumes the sandbox bound to label.
// Returns ErrHerdrSpaceNotFound when no binding exists for label.
func HerdrSpaceResumeByLabel(ctx context.Context, svc HerdrSpaceSandboxService, storeRoot, label string) error {
	b, err := HerdrSpaceGetByLabel(ctx, storeRoot, label)
	if err != nil {
		return err
	}
	if _, err := svc.Resume(ctx, b.SandboxHandle); err != nil {
		return fmt.Errorf("herdr-space: resume sandbox %q: %w", b.SandboxHandle, err)
	}
	return nil
}

// HerdrSpaceRemoveByLabel removes the sandbox bound to label and deletes the
// binding so no orphan mapping remains.
// Returns ErrHerdrSpaceNotFound when no binding exists for label.
func HerdrSpaceRemoveByLabel(ctx context.Context, svc HerdrSpaceSandboxService, storeRoot, label string) error {
	b, err := HerdrSpaceGetByLabel(ctx, storeRoot, label)
	if err != nil {
		return err
	}
	if err := svc.Remove(ctx, b.SandboxHandle); err != nil {
		return fmt.Errorf("herdr-space: remove sandbox %q: %w", b.SandboxHandle, err)
	}
	// Delete the binding after the sandbox is gone.
	if err := HerdrSpaceDelete(ctx, storeRoot, label); err != nil && !errors.Is(err, ErrHerdrSpaceNotFound) {
		return fmt.Errorf("herdr-space: delete binding %q: %w", label, err)
	}
	return nil
}
