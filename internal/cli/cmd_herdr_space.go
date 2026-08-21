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
	"log/slog"
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
	// GuestPaneID is the opaque pane ID herdr assigned to the guest-shell pane
	// last opened for this space, e.g. "w1V:p2" — see
	// result.plugin_pane.pane.pane_id in the `herdr plugin pane open`
	// response. It is what `herdr agent start --pane <ID>` needs, so a later
	// invocation can start an agent in an already-open space without
	// reopening a pane.
	//
	// Empty on bindings written before this field existed, and on any binding
	// where the pane ID could not be parsed out of herdr's response (JSON
	// mode failed, e.g.) — encoding/json leaves missing fields at their zero
	// value on decode, so an old binding on disk loads without error; there is
	// nothing else to migrate.
	GuestPaneID string `json:"guest_pane_id,omitempty"`
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

// herdrSpacePruneLister is the subset of *service.Service needed for the prune
// sandbox-alive check. Injected so tests avoid spinning up a real service.
type herdrSpacePruneLister interface {
	List(ctx context.Context) ([]domain.Sandbox, error)
}

// herdrSpacePruneSandboxExistsFn returns a predicate that reports whether the
// sandbox recorded in a binding still exists in the store. The sandbox list is
// fetched ONCE before the closure is returned; on list failure every sandbox is
// considered alive so no binding is pruned due to a transient store error.
//
// Inside the closure, a binding whose SandboxHandle cannot be parsed as a valid
// "<project>/<name>" handle is treated as alive — "cannot determine" must not
// trigger a destructive prune.
func herdrSpacePruneSandboxExistsFn(ctx context.Context, svc herdrSpacePruneLister) func(HerdrSpaceBinding) bool {
	sbs, err := svc.List(ctx)
	if err != nil {
		slog.Warn("space-prune: sandbox list; treating all as alive", "err", err)
		return func(HerdrSpaceBinding) bool { return true }
	}
	alive := make(map[string]bool, len(sbs))
	for _, sb := range sbs {
		if sb.Project != "" && sb.Name != "" {
			alive[sb.Project+"/"+sb.Name] = true
		}
	}
	return func(b HerdrSpaceBinding) bool {
		if _, _, err := domain.ParseHandle(b.SandboxHandle); err != nil {
			// Malformed handle — existence cannot be determined; treat as alive.
			return true
		}
		return alive[b.SandboxHandle]
	}
}

// herdrSpacePruneWorkspaceExistsFn returns a predicate that reports whether
// the herdr workspace recorded in a binding still exists in herdr. The list
// is fetched once; on fetch or parse failure every workspace is considered
// alive so no binding is pruned due to herdr being unreachable.  An empty
// response or a response where no entry carries a non-empty workspace_id is
// treated as "response not understood" → all bindings alive, so a malformed
// response never causes mass deletion.
func herdrSpacePruneWorkspaceExistsFn(ctx context.Context, herdrBin string) func(HerdrSpaceBinding) bool {
	out, err := herdrExecCommandContext(ctx, herdrBin, "workspace", "list").Output()
	if err != nil {
		slog.Warn("space-prune: workspace list", "err", err)
		return func(HerdrSpaceBinding) bool { return true }
	}
	var resp struct {
		Result struct {
			Workspaces []struct {
				WorkspaceID string `json:"workspace_id"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if jsonErr := json.Unmarshal(out, &resp); jsonErr != nil {
		slog.Warn("space-prune: parse workspace list; treating all as alive", "err", jsonErr)
		return func(HerdrSpaceBinding) bool { return true }
	}
	// Count entries with a non-empty workspace_id. A list where every entry has
	// a blank or absent workspace_id is indistinguishable from a malformed
	// response; treat it as all-alive to avoid destroying every binding.
	alive := make(map[string]bool, len(resp.Result.Workspaces))
	for _, ws := range resp.Result.Workspaces {
		if ws.WorkspaceID != "" {
			alive[ws.WorkspaceID] = true
		}
	}
	if len(alive) == 0 {
		slog.Warn("space-prune: workspace list returned no entries with a non-empty workspace_id; treating all as alive (likely unexpected response shape)")
		return func(HerdrSpaceBinding) bool { return true }
	}
	// Stated assumption (F5): herdr's workspace list API is unpaginated — a
	// single response contains ALL workspaces for the account. If herdr ever
	// adds pagination, a partial response would be indistinguishable from a
	// complete one and bindings absent from the page would be incorrectly
	// pruned (blast radius: binding record only; prune does not close a live
	// workspace in this path). If herdr gains pagination, adopt option (b):
	// block --apply when the returned workspace count is implausibly below
	// the known binding count. Verified from herdr's workspace list command
	// source: no next-page token is present; the list is returned in one call.
	return func(b HerdrSpaceBinding) bool {
		if b.HerdrWorkspaceID == "" {
			// Empty workspace ID — cannot determine existence; treat as alive.
			// Adopted bindings (herdrSpaceAdopt) intentionally omit the workspace
			// ID, so this guard prevents adopted sandboxes from being pruned.
			return true
		}
		return alive[b.HerdrWorkspaceID]
	}
}

// herdrSpaceTeardownOnRm tears down the herdr workspace binding for the sandbox
// identified by handle, closing the workspace via closeWorkspace. Called from
// sandbox rm after the sandbox itself has been removed. Non-fatal: errors are
// logged but do not prevent the sandbox removal from reporting success.
//
// When closeWorkspace fails the binding is intentionally retained so that
// space-prune can recover the orphaned workspace on its next run. A transient
// herdr outage must not permanently discard the live workspace record.
func herdrSpaceTeardownOnRm(ctx context.Context, storeRoot string, closeWorkspace func(context.Context, string) error, handle string) {
	b, err := HerdrSpaceGetByHandle(ctx, storeRoot, handle)
	if errors.Is(err, ErrHerdrSpaceNotFound) {
		return // no binding — nothing to tear down
	}
	if err != nil {
		slog.Warn("space teardown on rm: get binding", "handle", handle, "err", err)
		return
	}
	if closeErr := closeWorkspace(ctx, b.HerdrWorkspaceID); closeErr != nil {
		// Retain the binding so space-prune can recover the live workspace later.
		slog.Warn("space teardown on rm: close workspace", "workspace_id", b.HerdrWorkspaceID, "err", closeErr)
		return
	}
	if delErr := HerdrSpaceDelete(ctx, storeRoot, b.SpaceLabel); delErr != nil && !errors.Is(delErr, ErrHerdrSpaceNotFound) {
		slog.Warn("space teardown on rm: delete binding", "label", b.SpaceLabel, "err", delErr)
	}
}
