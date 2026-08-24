package cli

// Tests for herdrSpacePruneFull (the testable core of __herdr-plugin space-prune),
// herdrSpacePruneSandboxExistsFn (the real constructor — mutation-proven), and
// herdrSpacePruneWorkspaceExistsFn (the real predicate, tested via the
// herdrExecCommandContext seam).
//
// All seams are injected — no real herdr binary or live service is touched.
//
// Cases covered:
//  1. Stale sandbox  → pruned under --apply
//  2. Stale workspace (sandbox alive, workspace gone) → pruned under --apply
//  3. Valid binding (both alive) → PRESERVED (most critical case)
//  4. Dry-run (no --apply) → nothing deleted regardless of staleness
//  5–8. herdrSpacePruneWorkspaceExistsFn: four payload shapes via seam
//  9–11. herdrSpacePruneSandboxExistsFn: not-found, transient error, live (mutation-proven)

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// noopRemoveSandbox is a no-op removeSandbox stub for existing prune tests
// that do not exercise the worktree-reap path.  Existing tests use non-worktree
// handles so this function is never called; its presence keeps the function
// signature consistent after the new parameter was added.
var noopRemoveSandbox = func(_ context.Context, _ string) error { return nil }

// ── fakeListLister ────────────────────────────────────────────────────────────

// fakeListLister implements herdrSpacePruneLister for constructor tests.
type fakeListLister struct {
	sbs []domain.Sandbox
	err error
}

func (f *fakeListLister) List(_ context.Context) ([]domain.Sandbox, error) {
	return f.sbs, f.err
}

// buildFakeCheckers returns sandboxExists and workspaceExists functions keyed by
// HerdrWorkspaceID for workspace and SandboxHandle for sandbox, so tests can
// say exactly which ones are alive.
func buildFakeCheckers(aliveSandboxes, aliveWorkspaces map[string]bool) (
	func(HerdrSpaceBinding) bool,
	func(HerdrSpaceBinding) bool,
) {
	sandboxExists := func(b HerdrSpaceBinding) bool { return aliveSandboxes[b.SandboxHandle] }
	workspaceExists := func(b HerdrSpaceBinding) bool { return aliveWorkspaces[b.HerdrWorkspaceID] }
	return sandboxExists, workspaceExists
}

// TestHerdrSpacePrune_StaleSandbox: a binding whose sandbox is gone is pruned.
func TestHerdrSpacePrune_StaleSandbox(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:gone-sandbox",
		HerdrWorkspaceID: "wSTALE",
		SandboxHandle:    "proj/gone",
		SandboxID:        "sb-gone",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists, workspaceExists := buildFakeCheckers(
		map[string]bool{}, // sandbox gone
		map[string]bool{"wSTALE": true},
	)
	var closedIDs []string
	closer := func(_ context.Context, id string) error {
		closedIDs = append(closedIDs, id)
		return nil
	}

	var buf bytes.Buffer
	if err := herdrSpacePruneFull(ctx, &buf, root, "", sandboxExists, workspaceExists, closer, noopRemoveSandbox, true); err != nil {
		t.Fatalf("herdrSpacePruneFull: %v", err)
	}

	// Binding must be deleted.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("stale-sandbox binding must be pruned; err=%v", err)
	}
	// Closer must have been called for the workspace.
	if len(closedIDs) == 0 {
		t.Error("closer not called for stale-sandbox binding")
	}
	if !strings.Contains(buf.String(), "1 stale") {
		t.Errorf("output missing stale count; got %q", buf.String())
	}
}

// TestHerdrSpacePrune_StaleWorkspace: a non-worktree-managed binding whose
// workspace is gone but sandbox is still alive has its HerdrWorkspaceID cleared
// (binding retained — the sandbox is running; deleting the binding would strand it).
//
// MUTATION TARGET: the `sbPresent && !wsPresent && !b.IsWorktreeManaged()` branch
// in herdrSpacePruneFull. Removing it causes the binding to be deleted instead
// of cleared → RED (binding missing after prune).
func TestHerdrSpacePrune_StaleWorkspace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:alive-sandbox-gone-ws",
		HerdrWorkspaceID: "wGONE",
		SandboxHandle:    "proj/alive",
		SandboxID:        "sb-alive",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists, workspaceExists := buildFakeCheckers(
		map[string]bool{"proj/alive": true}, // sandbox alive
		map[string]bool{},                    // workspace gone
	)
	closer := func(_ context.Context, _ string) error { return nil }

	var buf bytes.Buffer
	if err := herdrSpacePruneFull(ctx, &buf, root, "", sandboxExists, workspaceExists, closer, noopRemoveSandbox, true); err != nil {
		t.Fatalf("herdrSpacePruneFull: %v", err)
	}

	// Binding must be retained (sandbox still running).
	got, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel)
	if err != nil {
		t.Errorf("binding must be retained (sandbox running); err=%v", err)
	}
	// Workspace ID must be cleared so next space-create mints a fresh one.
	if got.HerdrWorkspaceID != "" {
		t.Errorf("HerdrWorkspaceID must be cleared after stale-workspace prune; got %q", got.HerdrWorkspaceID)
	}
	// closer must NOT have been called (workspace is already gone; no point closing).
	// The output should mention the clear action.
	if !strings.Contains(buf.String(), "CLEARED") {
		t.Errorf("prune output must mention CLEARED; got %q", buf.String())
	}
}

// TestHerdrSpacePrune_ValidBinding: a binding where both sandbox AND workspace
// are alive must NOT be deleted.  This is the most critical preservation case.
func TestHerdrSpacePrune_ValidBinding(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:keep-me",
		HerdrWorkspaceID: "wKEEP",
		SandboxHandle:    "proj/keep",
		SandboxID:        "sb-keep",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists, workspaceExists := buildFakeCheckers(
		map[string]bool{"proj/keep": true},
		map[string]bool{"wKEEP": true},
	)
	var closedIDs []string
	closer := func(_ context.Context, id string) error {
		closedIDs = append(closedIDs, id)
		return nil
	}

	var buf bytes.Buffer
	if err := herdrSpacePruneFull(ctx, &buf, root, "", sandboxExists, workspaceExists, closer, noopRemoveSandbox, true); err != nil {
		t.Fatalf("herdrSpacePruneFull: %v", err)
	}

	// Binding must still exist.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err != nil {
		t.Errorf("valid binding must be preserved; err=%v", err)
	}
	// Closer must NOT have been called.
	if len(closedIDs) != 0 {
		t.Errorf("closer must not be called for a live binding; got %v", closedIDs)
	}
}

// TestHerdrSpacePrune_DryRunPreservesAll: dry-run (apply=false) must not delete
// anything even when a binding is stale.
func TestHerdrSpacePrune_DryRunPreservesAll(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:dry-run-stale",
		HerdrWorkspaceID: "wDRY",
		SandboxHandle:    "proj/dry",
		SandboxID:        "sb-dry",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists, workspaceExists := buildFakeCheckers(
		map[string]bool{}, // sandbox gone
		map[string]bool{},
	)
	closer := func(_ context.Context, _ string) error { return nil }

	var buf bytes.Buffer
	// apply=false
	if err := herdrSpacePruneFull(ctx, &buf, root, "", sandboxExists, workspaceExists, closer, noopRemoveSandbox, false); err != nil {
		t.Fatalf("herdrSpacePruneFull (dry-run): %v", err)
	}

	// Binding must still be present (dry-run).
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err != nil {
		t.Errorf("dry-run must not delete binding; err=%v", err)
	}
	if !strings.Contains(buf.String(), "--apply") {
		t.Errorf("dry-run output should mention --apply; got %q", buf.String())
	}
}

// TestHerdrSpacePrune_MixedBindings: three bindings — stale sandbox (b1),
// stale workspace with running sandbox (b2), and valid (b3).
//
// 4-case reconciler behaviour:
//   b1: sandbox absent — workspace closed + binding deleted.
//   b2: sandbox present, workspace absent, non-worktree-managed — workspace-id cleared, binding retained.
//   b3: both present — unchanged.
//
// MUTATION TARGET: the `sbPresent && !wsPresent && !b.IsWorktreeManaged()` branch in
// herdrSpacePruneFull. Removing it causes b2 to be deleted instead of workspace-id-cleared
// (wrong: the sandbox is still running) → RED.
func TestHerdrSpacePrune_MixedBindings(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	b1 := HerdrSpaceBinding{SpaceLabel: "nexus3:a", HerdrWorkspaceID: "wA", SandboxHandle: "p/a", SandboxID: "sb-a"}
	b2 := HerdrSpaceBinding{SpaceLabel: "nexus3:b", HerdrWorkspaceID: "wB", SandboxHandle: "p/b", SandboxID: "sb-b"}
	b3 := HerdrSpaceBinding{SpaceLabel: "nexus3:c", HerdrWorkspaceID: "wC", SandboxHandle: "p/c", SandboxID: "sb-c"}
	for _, b := range []HerdrSpaceBinding{b1, b2, b3} {
		if err := HerdrSpacePut(ctx, root, b); err != nil {
			t.Fatalf("HerdrSpacePut %s: %v", b.SpaceLabel, err)
		}
	}

	sandboxExists, workspaceExists := buildFakeCheckers(
		map[string]bool{"p/b": true, "p/c": true}, // b, c sandboxes alive; a gone
		map[string]bool{"wA": true, "wC": true},   // a, c workspaces alive; b gone
	)
	var buf bytes.Buffer
	closer := func(_ context.Context, _ string) error { return nil }
	if err := herdrSpacePruneFull(ctx, &buf, root, "", sandboxExists, workspaceExists, closer, noopRemoveSandbox, true); err != nil {
		t.Fatalf("herdrSpacePruneFull: %v", err)
	}

	// b1: sandbox gone → binding deleted.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b1.SpaceLabel); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("b1 (sandbox absent) must be pruned; err=%v", err)
	}
	// b2: sandbox alive, workspace gone, non-wt/ → binding retained with HerdrWorkspaceID cleared.
	got2, err := HerdrSpaceGetByLabel(ctx, root, b2.SpaceLabel)
	if err != nil {
		t.Errorf("b2 (sandbox running, workspace gone) binding must be retained; err=%v", err)
	} else if got2.HerdrWorkspaceID != "" {
		t.Errorf("b2 HerdrWorkspaceID must be cleared; got %q", got2.HerdrWorkspaceID)
	}
	// b3: both alive → unchanged.
	got3, err := HerdrSpaceGetByLabel(ctx, root, b3.SpaceLabel)
	if err != nil {
		t.Errorf("b3 (valid) must be preserved; err=%v", err)
	} else if got3.HerdrWorkspaceID != "wC" {
		t.Errorf("b3 HerdrWorkspaceID must be unchanged; got %q", got3.HerdrWorkspaceID)
	}
}

// withFakeHerdr overrides herdrExecCommandContext for the duration of t and
// restores it in t.Cleanup.  The fake ignores name/args and outputs payload on
// stdout with exit 0.  It uses "cat" reading from stdin rather than a shell
// expansion so payload is safe to contain any characters.
func withFakeHerdr(t *testing.T, payload string) {
	t.Helper()
	orig := herdrExecCommandContext
	t.Cleanup(func() { herdrExecCommandContext = orig })
	herdrExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "cat")
		cmd.Stdin = strings.NewReader(payload)
		return cmd
	}
}

// TestHerdrSpacePruneWorkspaceExistsFn exercises the REAL predicate through
// the herdrExecCommandContext seam with all four payload shapes from the
// reviewer's table.  The five existing prune tests inject fake predicates and
// never touch this function; this test closes that gap.
func TestHerdrSpacePruneWorkspaceExistsFn(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		// wantW6 is the expected result for a binding with HerdrWorkspaceID="w6".
		wantW6 bool
		// wantW999 is the expected result for an ID not in the list.
		wantW999 bool
	}{
		{
			// Valid payload: w6 present in list → alive, w999 not present → stale.
			name:     "valid list with w6",
			payload:  `{"result":{"workspaces":[{"workspace_id":"w6"}]}}`,
			wantW6:   true,
			wantW999: false,
		},
		{
			// Empty result object: Workspaces field absent → zero-len → fail-safe.
			name:     "empty result object",
			payload:  `{"result":{}}`,
			wantW6:   true,
			wantW999: true,
		},
		{
			// Error envelope with exit 0: no "result.workspaces" key → zero-len → fail-safe.
			name:     "error envelope exit 0",
			payload:  `{"error":{"code":"busy"}}`,
			wantW6:   true,
			wantW999: true,
		},
		{
			// Field renamed from "workspaces" to "items": json.Unmarshal succeeds but
			// Workspaces stays nil → zero-len → fail-safe.
			name:     "field renamed to items",
			payload:  `{"result":{"items":[{"workspace_id":"w6"}]}}`,
			wantW6:   true,
			wantW999: true,
		},
		{
			// BREACH: outer field name is correct but inner key is "id" not
			// "workspace_id". All entries unmarshal with WorkspaceID="". The alive
			// map built from blank IDs would be {"":true}, making every binding
			// appear stale (C2 regression case). The guard must detect zero
			// non-empty IDs and treat all as alive.
			//
			// MUTATION TARGET: revert the non-empty-count guard to the old
			// len(resp.Result.Workspaces)==0 check; this case MUST turn RED.
			name:     "inner key renamed to id",
			payload:  `{"result":{"workspaces":[{"id":"w6"},{"id":"w999"}]}}`,
			wantW6:   true,
			wantW999: true,
		},
		{
			// BREACH: outer shape is correct but one entry has a blank workspace_id.
			// Blank entries must be skipped; only non-empty IDs populate the alive map.
			// w6 is alive (real id present), w999 is not in the list → stale.
			name:     "blank workspace_id entries skipped",
			payload:  `{"result":{"workspaces":[{"workspace_id":""},{"workspace_id":"w6"}]}}`,
			wantW6:   true,
			wantW999: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withFakeHerdr(t, tc.payload)

			pred := herdrSpacePruneWorkspaceExistsFn(context.Background(), "fake-herdr")

			b6 := HerdrSpaceBinding{HerdrWorkspaceID: "w6"}
			b999 := HerdrSpaceBinding{HerdrWorkspaceID: "w999"}

			if got := pred(b6); got != tc.wantW6 {
				t.Errorf("pred(w6): got %v, want %v", got, tc.wantW6)
			}
			if got := pred(b999); got != tc.wantW999 {
				t.Errorf("pred(w999): got %v, want %v", got, tc.wantW999)
			}
		})
	}
}

// ── herdrSpacePruneSandboxExistsFn constructor tests (mutation-proven) ────────
//
// Mutation proof — to confirm each test catches its target defect, the
// following mutations were applied one at a time and each made the named test
// RED while the rest stayed GREEN:
//
//   Mutation A (invert not-found result):
//     change `return alive[b.SandboxHandle]` → `return true`
//     → TestHerdrSpacePruneSandboxExistsFn_NotFound RED (got true, want false)
//
//   Mutation B (invert error result):
//     change error-path closure `return true` → `return false`
//     → TestHerdrSpacePruneSandboxExistsFn_TransientError RED (got false, want true)
//
// Both mutations compile cleanly and change exactly one test verdict.

// TestHerdrSpacePruneSandboxExistsFn_NotFound: a binding whose handle is not in
// the live sandbox list must be reported as gone (predicate = false).
func TestHerdrSpacePruneSandboxExistsFn_NotFound(t *testing.T) {
	lister := &fakeListLister{
		// List succeeds but contains no matching sandbox.
		sbs: []domain.Sandbox{
			{Project: "other", Name: "unrelated"},
		},
	}
	pred := herdrSpacePruneSandboxExistsFn(context.Background(), lister)

	b := HerdrSpaceBinding{SandboxHandle: "proj/gone"}
	if got := pred(b); got != false {
		t.Errorf("handle not in list: predicate = %v, want false (binding should be prunable)", got)
	}
}

// TestHerdrSpacePruneSandboxExistsFn_TransientError: when List returns an error
// every binding must be reported as alive (predicate = true) so no binding is
// destructively pruned due to a transient store error.
func TestHerdrSpacePruneSandboxExistsFn_TransientError(t *testing.T) {
	lister := &fakeListLister{
		err: errors.New("store: read dir: permission denied"),
	}
	pred := herdrSpacePruneSandboxExistsFn(context.Background(), lister)

	b := HerdrSpaceBinding{SandboxHandle: "proj/any"}
	if got := pred(b); got != true {
		t.Errorf("list error: predicate = %v, want true (all bindings must survive a transient error)", got)
	}
}

// TestHerdrSpacePruneSandboxExistsFn_LiveHandle: a binding whose handle is
// present in the live sandbox list must be reported as alive (predicate = true).
func TestHerdrSpacePruneSandboxExistsFn_LiveHandle(t *testing.T) {
	lister := &fakeListLister{
		sbs: []domain.Sandbox{
			{Project: "proj", Name: "live"},
			{Project: "other", Name: "unrelated"},
		},
	}
	pred := herdrSpacePruneSandboxExistsFn(context.Background(), lister)

	b := HerdrSpaceBinding{SandboxHandle: "proj/live"}
	if got := pred(b); got != true {
		t.Errorf("live handle: predicate = %v, want true (live binding must be preserved)", got)
	}
}

// TestHerdrSpacePruneWorkspaceExistsFn_Argv asserts that
// herdrSpacePruneWorkspaceExistsFn invokes herdr with exactly
// ["workspace", "list"] and NO extra flags (e.g. "--json" is invalid and
// causes herdr to exit 2, silently breaking the prune check in production).
//
// Mutation proof: re-adding "--json" as the third argument in
// herdrSpacePruneWorkspaceExistsFn makes this test RED:
//
//	want args: [workspace list]
//	got  args: [workspace list --json]
//
// That mutation compiles cleanly — it is the exact defect this test catches.
func TestHerdrSpacePruneWorkspaceExistsFn_Argv(t *testing.T) {
	var capturedArgs []string

	orig := herdrExecCommandContext
	t.Cleanup(func() { herdrExecCommandContext = orig })
	herdrExecCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		// Return a valid workspace-list payload so the function doesn't bail on error.
		payload := `{"result":{"workspaces":[{"workspace_id":"wX"}]}}`
		cmd := exec.CommandContext(ctx, "cat")
		cmd.Stdin = strings.NewReader(payload)
		return cmd
	}

	herdrSpacePruneWorkspaceExistsFn(context.Background(), "fake-herdr")

	want := []string{"workspace", "list"}
	if len(capturedArgs) != len(want) {
		t.Fatalf("herdr argv: got %v, want %v", capturedArgs, want)
	}
	for i, w := range want {
		if capturedArgs[i] != w {
			t.Errorf("argv[%d]: got %q, want %q (full: %v)", i, capturedArgs[i], w, capturedArgs)
		}
	}
}

// TestHerdrSpacePruneSandboxExistsFn_MalformedHandle: a binding with a
// malformed SandboxHandle (no slash) must be treated as alive — "cannot
// determine" must not trigger a destructive prune.
func TestHerdrSpacePruneSandboxExistsFn_MalformedHandle(t *testing.T) {
	lister := &fakeListLister{
		sbs: []domain.Sandbox{}, // empty list — would prune everything if not for guard
	}
	pred := herdrSpacePruneSandboxExistsFn(context.Background(), lister)

	b := HerdrSpaceBinding{SandboxHandle: "no-slash-handle"}
	if got := pred(b); got != true {
		t.Errorf("malformed handle: predicate = %v, want true (undeterminable handle must survive)", got)
	}
}

// TestHerdrSpacePrune_NoHerdr_ApplyRefused verifies that herdrPluginSpacePrune
// refuses --apply when herdrBin is empty (herdr not on PATH). The binding must
// survive: with herdr unavailable the workspace-exists predicate would exec-fail
// to all-alive, but a sandbox-gone binding would still be pruned leaving a live
// workspace orphaned and unverifiable.
//
// MUTATION TARGET (Fix 3): remove the `if *apply && herdrBin == ""` guard from
// herdrPluginSpacePrune.
// Expected RED: herdrPluginSpacePrune returns nil → no error reported → binding
// deleted despite herdr being unavailable.
func TestHerdrSpacePrune_NoHerdr_ApplyRefused(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:apply-guard",
		HerdrWorkspaceID: "wAPPLY",
		SandboxHandle:    "proj/apply-guard",
		SandboxID:        "sb-ag",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	var buf bytes.Buffer
	lister := &fakeListLister{sbs: nil} // would mark sandbox as gone
	// herdrBin == "" simulates herdr not found.
	err := herdrPluginSpacePrune(ctx, []string{"--apply"}, &buf, lister, root, "")
	if err == nil {
		t.Fatal("herdrPluginSpacePrune --apply with no herdr: got nil error, want UsageError")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}

	// Binding must be retained — prune must not have deleted anything.
	if _, err2 := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err2 != nil {
		t.Errorf("binding must survive refused --apply; HerdrSpaceGetByLabel: %v", err2)
	}
}

// TestHerdrSpacePrune_NoHerdr_DryRunAllowed verifies that --dry-run (no --apply)
// is allowed even when herdrBin is empty: it can report without deleting, and
// the workspace-exists predicate already fails safe to all-alive on exec error.
func TestHerdrSpacePrune_NoHerdr_DryRunAllowed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:dry-no-herdr",
		HerdrWorkspaceID: "wDNH",
		SandboxHandle:    "proj/dry-no-herdr",
		SandboxID:        "sb-dnh",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	var buf bytes.Buffer
	lister := &fakeListLister{sbs: nil}
	// No --apply: dry-run must succeed even without herdr.
	if err := herdrPluginSpacePrune(ctx, []string{}, &buf, lister, root, ""); err != nil {
		t.Errorf("herdrPluginSpacePrune dry-run without herdr: got %v, want nil", err)
	}
	// Binding must survive (dry-run never deletes).
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err != nil {
		t.Errorf("binding must survive dry-run; HerdrSpaceGetByLabel: %v", err)
	}
}

// ── Site B: close-failure retention in prune apply loop ──────────────────────

// TestHerdrSpacePruneFull_CloseFail_BindingRetained asserts that a stale binding
// whose workspace close fails is NOT deleted. The binding stays so the next
// prune run can retry.
//
// MUTATION TARGET: remove the `continue` after the close-error log in
// herdrSpacePruneFull so the code falls through to HerdrSpaceDelete.
// Expected RED: binding gone after call, but test asserts binding present.
func TestHerdrSpacePruneFull_CloseFail_BindingRetained(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:close-fail",
		HerdrWorkspaceID: "wFAIL",
		SandboxHandle:    "proj/close-fail",
		SandboxID:        "sb-cf",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists, workspaceExists := buildFakeCheckers(
		map[string]bool{},         // sandbox gone → stale
		map[string]bool{"wFAIL": true},
	)
	closeErr := errors.New("herdr: connection refused")
	closer := func(_ context.Context, _ string) error { return closeErr }

	var buf bytes.Buffer
	if err := herdrSpacePruneFull(ctx, &buf, root, "", sandboxExists, workspaceExists, closer, noopRemoveSandbox, true); err != nil {
		t.Fatalf("herdrSpacePruneFull: %v", err)
	}

	// Binding must still exist — close failed, retention required.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err != nil {
		t.Errorf("binding must be retained after close failure; HerdrSpaceGetByLabel: %v", err)
	}
	// Deleted count must be 0 (nothing successfully removed).
	if !strings.Contains(buf.String(), "Deleted 0") {
		t.Errorf("output should report 0 deleted; got %q", buf.String())
	}
}

// TestHerdrSpacePruneFull_CloseSucceeds_BindingDeleted asserts that a stale
// binding whose workspace close succeeds IS deleted.
//
// MUTATION TARGET: add `continue` before HerdrSpaceDelete in the prune loop so
// the binding is never deleted even on close success.
// Expected RED: binding present after call, but test asserts binding absent.
func TestHerdrSpacePruneFull_CloseSucceeds_BindingDeleted(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:close-ok",
		HerdrWorkspaceID: "wOK",
		SandboxHandle:    "proj/close-ok",
		SandboxID:        "sb-ok",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists, workspaceExists := buildFakeCheckers(
		map[string]bool{},       // sandbox gone → stale
		map[string]bool{"wOK": true},
	)
	closer := func(_ context.Context, _ string) error { return nil } // close succeeds

	var buf bytes.Buffer
	if err := herdrSpacePruneFull(ctx, &buf, root, "", sandboxExists, workspaceExists, closer, noopRemoveSandbox, true); err != nil {
		t.Fatalf("herdrSpacePruneFull: %v", err)
	}

	// Binding must be gone — close succeeded, delete authorised.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("binding must be deleted after close success; got err=%v", err)
	}
	// Deleted count must be 1.
	if !strings.Contains(buf.String(), "Deleted 1") {
		t.Errorf("output should report 1 deleted; got %q", buf.String())
	}
}

// ── adopted binding guard (Finding 1) ─────────────────────────────────────────

// TestHerdrSpacePruneWorkspaceExistsFn_AdoptedBinding verifies that an adopted
// binding — one with an empty HerdrWorkspaceID, created by herdrSpaceAdopt —
// is treated as alive by the workspace predicate even when the workspace list
// from herdr is healthy and non-empty.
//
// Scenario: live sandbox + adopted binding (HerdrWorkspaceID="") + valid
// workspace list containing a different workspace.  The binding must SURVIVE
// --apply.  Without the empty-ID guard the predicate returns alive[""]=false
// and the binding is incorrectly deleted.
//
// MUTATION TARGET: remove the `if b.HerdrWorkspaceID == ""` guard from
// herdrSpacePruneWorkspaceExistsFn's returned closure.
// Expected RED: binding gone after --apply, but test asserts binding present.
func TestHerdrSpacePruneWorkspaceExistsFn_AdoptedBinding(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// Adopted binding: HerdrWorkspaceID intentionally empty (see herdrSpaceAdopt).
	adopted := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:adopted-live",
		SandboxHandle:    "proj/adopted-live",
		SandboxID:        "sb-adopted-live",
		HerdrWorkspaceID: "", // empty by design
	}
	if err := HerdrSpacePut(ctx, root, adopted); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	// Workspace list returns one real workspace — not the adopted binding.
	// The list is non-empty so the len(alive)==0 all-alive guard does NOT fire;
	// only the empty-ID guard can save this binding.
	withFakeHerdr(t, `{"result":{"workspaces":[{"workspace_id":"wOTHER"}]}}`)

	workspaceExists := herdrSpacePruneWorkspaceExistsFn(ctx, "fake-herdr")

	// Sandbox is alive — only the workspace predicate is under test here.
	sandboxExists := func(b HerdrSpaceBinding) bool { return true }

	closer := func(_ context.Context, id string) error {
		t.Errorf("closer must not be called for an adopted binding (workspace_id=%q)", id)
		return nil
	}

	var buf bytes.Buffer
	if err := herdrSpacePruneFull(ctx, &buf, root, "", sandboxExists, workspaceExists, closer, noopRemoveSandbox, true); err != nil {
		t.Fatalf("herdrSpacePruneFull: %v", err)
	}

	// Adopted binding must still exist — cannot prune what has no workspace ID.
	if _, err := HerdrSpaceGetByLabel(ctx, root, adopted.SpaceLabel); err != nil {
		t.Errorf("adopted binding must survive --apply; HerdrSpaceGetByLabel: %v", err)
	}
	// Nothing should have been deleted.
	if strings.Contains(buf.String(), "STALE") {
		t.Errorf("adopted binding must not be reported STALE; output: %q", buf.String())
	}
}

// ── Site C: HerdrSpaceDelete failure in prune apply loop ─────────────────────

// TestHerdrSpacePruneFull_DeleteFail_BindingRetained asserts that when close
// succeeds but HerdrSpaceDelete itself fails, the deleted counter is NOT
// incremented and the binding remains on disk.
//
// MUTATION TARGET (M14): remove the `continue` after the delete-error log so
// that deleted++ fires even when HerdrSpaceDelete errored (counting attempts
// instead of actual deletions).
// Expected RED: output contains "Deleted 1" but the test asserts "Deleted 0".
func TestHerdrSpacePruneFull_DeleteFail_BindingRetained(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod-based injection is ineffective as root")
	}
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:delete-fail",
		HerdrWorkspaceID: "wDELFAIL",
		SandboxHandle:    "proj/delete-fail",
		SandboxID:        "sb-df",
	}
	// Write binding (also creates the lock file as a side effect of Put's lock
	// acquisition, so that OpenLock can succeed with the directory unwritable).
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	// Make the store directory unwritable so that herdrSpaceWriteAll's
	// os.CreateTemp fails. Read+execute are kept so OpenLock can open the
	// existing lock file by name, and ReadFile can read the existing bindings.
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatalf("chmod storeRoot: %v", err)
	}
	// Restore write permission before TempDir cleanup runs.
	defer func() { _ = os.Chmod(root, 0700) }()

	// Verify that the chmod actually causes HerdrSpaceDelete to fail.
	// If it does not, the test would silently pass for the wrong reason.
	probeErr := HerdrSpaceDelete(ctx, root, b.SpaceLabel)
	if probeErr == nil {
		t.Fatal("chmod did not make HerdrSpaceDelete fail — injection point is ineffective on this filesystem/uid; test cannot proceed")
	}

	// Re-write the binding (the failed delete left it intact; confirm via API).
	_, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel)
	if err != nil {
		t.Fatalf("binding should still exist after failed probe delete: %v", err)
	}

	sandboxExists, workspaceExists := buildFakeCheckers(
		map[string]bool{},                  // sandbox gone → stale
		map[string]bool{"wDELFAIL": true},
	)
	closer := func(_ context.Context, _ string) error { return nil } // close succeeds

	var buf bytes.Buffer
	if err := herdrSpacePruneFull(ctx, &buf, root, "", sandboxExists, workspaceExists, closer, noopRemoveSandbox, true); err != nil {
		t.Fatalf("herdrSpacePruneFull: %v", err)
	}

	// Binding must still exist — delete failed, so the record was not removed.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err != nil {
		t.Errorf("binding must be retained after delete failure; HerdrSpaceGetByLabel: %v", err)
	}
	// Counter must reflect actual deletions (zero), not attempts.
	if !strings.Contains(buf.String(), "Deleted 0") {
		t.Errorf("output should report 0 deleted; got %q", buf.String())
	}
}
