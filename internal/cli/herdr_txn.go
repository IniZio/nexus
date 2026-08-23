package cli

// herdr_txn.go — atomic transaction primitives for the herdr-space ↔ nexus3-sandbox lifecycle.
//
// Design contract (fail-safe philosophy):
//   - A pre-existing/running sandbox is NEVER removed by a pane-open hiccup.
//     ensureSandbox returns createdByUs; svcRemove compensation fires ONLY when
//     createdByUs=true.
//   - Teardown is forward-only + idempotent: a workspace close that did NOT
//     succeed must NOT authorise deleting the binding.
//   - SandboxID guard (commit 8395389 / w4B): teardown refuses if
//     expectedSandboxID is set and binding.SandboxID does not match.
//   - failOpen: non-close teardown errors are swallowed when true, so teardown
//     never freezes a new pane (SIGHUP reap / sandbox-rm cascade paths).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// ── Types ─────────────────────────────────────────────────────────────────────

// createSpec carries parameters for herdrSpaceCreateTxn.
type createSpec struct {
	// ref is the sandbox ref (handle, e.g. "orca/demo-01").
	ref string
	// label is the herdr workspace label derived from ref (e.g. "nexus3:orca/demo-01").
	label string
	// hostCwd is the cwd hint passed to herdr workspace create.
	hostCwd string
	// focus controls whether herdr should focus the new pane.
	focus bool
}

// teardownOpts controls the behaviour of herdrSpaceTeardown.
type teardownOpts struct {
	// expectedSandboxID, when non-empty, causes teardown to refuse if
	// binding.SandboxID does not match (SandboxID guard, commit 8395389 / w4B).
	expectedSandboxID string
	// sandboxAlreadyRemoved, when true, skips the svcRemove step.
	// Pass true when the caller has already deleted the sandbox VM.
	sandboxAlreadyRemoved bool
	// failOpen, when true, logs non-close errors and returns nil.
	// Required for paths (SIGHUP reap, sandbox-rm cascade) where an error must
	// never freeze a new pane.
	// Close-failure ALWAYS retains the binding and returns nil regardless of
	// this flag.
	failOpen bool
}

// txnDeps carries injected seams for herdrSpaceCreateTxn,
// herdrSpaceEnsureWorkspaceTxn, and herdrSpaceTeardown.
type txnDeps struct {
	// ensureSandbox starts/resolves the sandbox. createdByUs is true when
	// THIS call created the sandbox (rollback candidate), false for pre-existing.
	// Returns the sandbox handle and stable ID as strings.
	ensureSandbox func(ctx context.Context, ref string) (handle, sandboxID string, createdByUs bool, err error)

	// workspaceCreate creates a new herdr workspace; returns (workspaceID, rootPaneID).
	workspaceCreate func(ctx context.Context, herdrBin, label, cwd string) (workspaceID, rootPaneID string, err error)

	// workspaceClose closes the herdr workspace.
	// Implementations must treat "workspace_not_found" as success.
	// workspaceClose(ctx, "") must return nil (no-op for absent IDs).
	workspaceClose func(ctx context.Context, workspaceID string) error

	// bindingPut persists the binding (upsert via HerdrSpacePut semantics).
	bindingPut func(ctx context.Context, b HerdrSpaceBinding) error

	// bindingDelete removes the binding for the given SpaceLabel.
	// Implementations must tolerate ErrHerdrSpaceNotFound.
	bindingDelete func(ctx context.Context, label string) error

	// svcRemove removes the sandbox VM.
	// Implementations must tolerate store.ErrNotFound / "not found".
	svcRemove func(ctx context.Context, ref string) error

	// openPane opens a guest-shell pane in the workspace and returns its pane ID.
	// Only required for herdrSpaceCreateTxn; may be nil for other functions.
	openPane func(ctx context.Context, herdrBin, ref, workspaceID, rootPaneID string, focus bool) (paneID string, err error)
}

// ── Transaction bodies ────────────────────────────────────────────────────────

// herdrSpaceCreateTxn atomically creates a herdr space.
//
// Transaction order (commit = bindingPut):
//
//  1. ensureSandbox  — captures (handle, sandboxID, createdByUs)
//  2. workspaceCreate
//  3. bindingPut     ← commit point; LIFO compensation fires on failure
//  4. openPane       — post-commit; failure downgrades to warning, binding retained
//  5. patch GuestPaneID — post-commit; failure downgrades to warning
//
// Pre-commit LIFO compensation: workspaceClose → svcRemove iff createdByUs.
// Pre-existing sandboxes (createdByUs=false) are NEVER removed.
func herdrSpaceCreateTxn(ctx context.Context, spec createSpec, deps txnDeps, herdrBin, storeRoot string) error {
	// Step 1: ensure sandbox.
	handle, sandboxID, createdByUs, err := deps.ensureSandbox(ctx, spec.ref)
	if err != nil {
		return fmt.Errorf("herdr-txn create: ensure sandbox %q: %w", spec.ref, err)
	}

	// Step 2: create workspace.
	workspaceID, rootPaneID, err := deps.workspaceCreate(ctx, herdrBin, spec.label, spec.hostCwd)
	if err != nil {
		// Compensate: remove sandbox only if we created it.
		if createdByUs {
			if rmErr := deps.svcRemove(ctx, handle); rmErr != nil {
				slog.Warn("herdr-txn create: pre-commit rollback: svcRemove failed",
					"handle", handle, "err", rmErr)
			}
		}
		return fmt.Errorf("herdr-txn create: workspace create %q: %w", spec.label, err)
	}

	// Step 3: commit (bindingPut). LIFO compensation on failure.
	b := HerdrSpaceBinding{
		SpaceLabel:       spec.label,
		HerdrWorkspaceID: workspaceID,
		SandboxHandle:    handle,
		SandboxID:        sandboxID,
	}
	if err := deps.bindingPut(ctx, b); err != nil {
		// LIFO compensation: close workspace first, then sandbox iff ours.
		if closeErr := deps.workspaceClose(ctx, workspaceID); closeErr != nil {
			slog.Warn("herdr-txn create: pre-commit rollback: workspaceClose failed",
				"workspace_id", workspaceID, "err", closeErr)
		}
		if createdByUs {
			if rmErr := deps.svcRemove(ctx, handle); rmErr != nil {
				slog.Warn("herdr-txn create: pre-commit rollback: svcRemove failed after close",
					"handle", handle, "err", rmErr)
			}
		}
		return fmt.Errorf("herdr-txn create: store binding %q: %w", spec.label, err)
	}

	// Post-commit — failures are warnings only; binding is retained.
	if deps.openPane == nil {
		return nil
	}
	paneID, err := deps.openPane(ctx, herdrBin, handle, workspaceID, rootPaneID, spec.focus)
	if err != nil {
		slog.Warn("herdr-txn create: post-commit: open pane failed (binding retained)",
			"label", spec.label, "err", err)
		return nil
	}

	b.GuestPaneID = paneID
	if err := deps.bindingPut(ctx, b); err != nil {
		slog.Warn("herdr-txn create: post-commit: patch GuestPaneID failed (binding retained)",
			"label", spec.label, "pane_id", paneID, "err", err)
	}
	return nil
}

// herdrSpaceEnsureWorkspaceTxn returns b with a real HerdrWorkspaceID.
//
// If b.HerdrWorkspaceID is already set the binding is returned unchanged (no-op).
// Otherwise a workspace is created, the binding is updated, and both are returned.
//
// TBD-SHL-7 rollback: if bindingPut fails after workspaceCreate, the orphaned
// workspace is immediately closed so it does not become permanently unreachable.
func herdrSpaceEnsureWorkspaceTxn(ctx context.Context, b HerdrSpaceBinding, hostCwd string, deps txnDeps, herdrBin string) (HerdrSpaceBinding, string, error) {
	if b.HerdrWorkspaceID != "" {
		return b, "", nil
	}
	if herdrBin == "" {
		return b, "", errors.New("herdr-txn ensure-workspace: herdr binary not available (HERDR_BIN_PATH unset and herdr not on PATH)")
	}

	workspaceID, rootPaneID, err := deps.workspaceCreate(ctx, herdrBin, b.SpaceLabel, hostCwd)
	if err != nil {
		return b, "", fmt.Errorf("herdr-txn ensure-workspace: create workspace %q: %w", b.SpaceLabel, err)
	}

	b.HerdrWorkspaceID = workspaceID
	if putErr := deps.bindingPut(ctx, b); putErr != nil {
		// TBD-SHL-7: close the workspace so it is not permanently orphaned.
		if closeErr := deps.workspaceClose(ctx, workspaceID); closeErr != nil {
			slog.Warn("herdr-txn ensure-workspace: TBD-SHL-7 rollback: workspaceClose failed (workspace orphaned)",
				"workspace_id", workspaceID, "label", b.SpaceLabel, "err", closeErr)
		}
		return b, "", fmt.Errorf("herdr-txn ensure-workspace: store binding %q: %w", b.SpaceLabel, putErr)
	}
	return b, rootPaneID, nil
}

// herdrSpaceTeardown is the unified teardown for sandbox VM + herdr workspace + binding.
//
// Teardown order: svcRemove → workspaceClose → bindingDelete.
//
// Invariants:
//   - not-found at any step is tolerated (idempotent; safe to re-run).
//   - close-FAILURE always retains the binding and returns nil — a workspace
//     that was not closed must not have its record deleted.
//   - expectedSandboxID mismatch refuses teardown (commits 8395389 / w4B).
//   - sandboxAlreadyRemoved skips svcRemove.
//   - failOpen swallows non-close errors: log + return nil.
func herdrSpaceTeardown(ctx context.Context, storeRoot, handle string, deps txnDeps, opts teardownOpts) error {
	b, err := HerdrSpaceGetByHandle(ctx, storeRoot, handle)
	if errors.Is(err, ErrHerdrSpaceNotFound) {
		return nil // no binding — already torn down or never created
	}
	if err != nil {
		return herdrTxnMaybeErr(opts.failOpen, err, "herdr-txn teardown: get binding by handle", "handle", handle)
	}

	// SandboxID guard (w4B data-loss fix, commit 8395389).
	if opts.expectedSandboxID != "" && b.SandboxID != "" && b.SandboxID != opts.expectedSandboxID {
		slog.Info("herdr-txn teardown: binding belongs to a different sandbox; refusing",
			"handle", handle, "expected_id", opts.expectedSandboxID, "binding_id", b.SandboxID)
		refErr := fmt.Errorf("herdr-txn teardown: SandboxID mismatch (expected %s, binding has %s)",
			opts.expectedSandboxID, b.SandboxID)
		return herdrTxnMaybeErr(opts.failOpen, refErr, "herdr-txn teardown: SandboxID guard")
	}

	// Remove sandbox VM (unless already gone).
	if !opts.sandboxAlreadyRemoved {
		if err := deps.svcRemove(ctx, handle); err != nil && !herdrTxnIsNotFound(err) {
			return herdrTxnMaybeErr(opts.failOpen, err, "herdr-txn teardown: svcRemove", "handle", handle)
		}
	}

	// Close herdr workspace. On failure: retain binding, return nil (always).
	if err := deps.workspaceClose(ctx, b.HerdrWorkspaceID); err != nil {
		slog.Warn("herdr-txn teardown: workspace close failed; binding retained for space-prune recovery",
			"workspace_id", b.HerdrWorkspaceID, "handle", handle, "err", err)
		return nil
	}

	// Delete binding. Double-teardown safe: not-found tolerated.
	if err := deps.bindingDelete(ctx, b.SpaceLabel); err != nil && !errors.Is(err, ErrHerdrSpaceNotFound) {
		return herdrTxnMaybeErr(opts.failOpen, err, "herdr-txn teardown: delete binding", "label", b.SpaceLabel)
	}
	return nil
}

// ── Private helpers ───────────────────────────────────────────────────────────

// herdrTxnMaybeErr logs err and returns nil when failOpen; otherwise returns err.
func herdrTxnMaybeErr(failOpen bool, err error, msg string, fields ...any) error {
	if err == nil {
		return nil
	}
	args := append(fields, "err", err) //nolint:gocritic
	slog.Warn(msg, args...)
	if failOpen {
		return nil
	}
	return err
}

// herdrTxnIsNotFound reports whether err represents a sandbox-not-found condition.
// Uses string matching so callers do not need to import the store package.
func herdrTxnIsNotFound(err error) bool {
	return strings.Contains(err.Error(), "not found")
}
