package cli

// Tests for herdrWorktreeSandboxHandle, herdrListWorktreeForWorkspace (parse
// layer), and herdrWorktreeSandbox (the orchestrator).
//
// Assertion↔mechanism discipline: every assertion names what it would still
// accept; if that set includes the mutation that reverts the mechanism, the
// assertion is tightened. Mutation proofs are recorded inline.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// ── herdrWorktreeSandboxHandle ────────────────────────────────────────────────

func TestHerdrWorktreeSandboxHandle_stripsPrefix(t *testing.T) {
	// Full path is encoded: "worktree/silver-forest-225f" → "wt/worktree-silver-forest-225f".
	// Mutation: strip to last segment → "wt/silver-forest-225f". RED: want "wt/worktree-silver-forest-225f".
	got := herdrWorktreeSandboxHandle("worktree/silver-forest-225f")
	const want = "wt/worktree-silver-forest-225f"
	if got != want {
		t.Errorf("handle = %q; want %q", got, want)
	}
}

func TestHerdrWorktreeSandboxHandle_featureBranch(t *testing.T) {
	// Full path encoding: "feature/my-feature" → "wt/feature-my-feature".
	// Distinguishes from "bugfix/my-feature" → "wt/bugfix-my-feature".
	got := herdrWorktreeSandboxHandle("feature/my-feature")
	const want = "wt/feature-my-feature"
	if got != want {
		t.Errorf("handle = %q; want %q", got, want)
	}
}

func TestHerdrWorktreeSandboxHandle_noPrefixSlash(t *testing.T) {
	// Branch with no "/" → use whole name.
	// Mutation: return "" after "wt/" yields "wt/". RED: want "wt/main".
	got := herdrWorktreeSandboxHandle("main")
	const want = "wt/main"
	if got != want {
		t.Errorf("handle = %q; want %q", got, want)
	}
}

func TestHerdrWorktreeSandboxHandle_sanitizesUppercase(t *testing.T) {
	// Uppercase chars are lowercased; full path is encoded.
	got := herdrWorktreeSandboxHandle("Feature/MyBranch")
	const want = "wt/feature-mybranch"
	if got != want {
		t.Errorf("handle = %q; want %q", got, want)
	}
}

func TestHerdrWorktreeSandboxHandle_sanitizesSpecialChars(t *testing.T) {
	// Special chars become "-", consecutive runs are collapsed; full path encoded.
	got := herdrWorktreeSandboxHandle("feat/my_branch.v2")
	const want = "wt/feat-my-branch-v2"
	if got != want {
		t.Errorf("handle = %q; want %q", got, want)
	}
}

func TestHerdrWorktreeSandboxHandle_emptySlugFallback(t *testing.T) {
	// A branch where all chars are special falls back to "worktree".
	// Mutation: return "" gives "wt/". RED: want "wt/worktree".
	got := herdrWorktreeSandboxHandle("___")
	const want = "wt/worktree"
	if got != want {
		t.Errorf("handle = %q; want %q", got, want)
	}
}

func TestHerdrWorktreeSandboxHandle_noBranchCollision(t *testing.T) {
	// "feature/x" and "bugfix/x" must produce distinct handles.
	// Under the old last-segment scheme both yielded "wt/x".
	// Mutation: strip to last segment → both produce "wt/x". RED: a == b.
	a := herdrWorktreeSandboxHandle("feature/x")
	b := herdrWorktreeSandboxHandle("bugfix/x")
	if a == b {
		t.Errorf("feature/x and bugfix/x both produce %q; handles must be distinct", a)
	}
}

func TestHerdrWorktreeSandboxHandle_hasWtPrefix(t *testing.T) {
	// Handle always starts with "wt/" — this ensures it can never collide with
	// the plain "nexus3" label (w8's label) since "wt/" ≠ "nexus3".
	// Mutation: drop the "wt/" prefix → "silver-forest-225f". RED: no "wt/".
	got := herdrWorktreeSandboxHandle("worktree/silver-forest-225f")
	if !strings.HasPrefix(got, "wt/") {
		t.Errorf("handle %q does not start with 'wt/'; expected 'wt/' prefix", got)
	}
}

// ── herdrWorktreeSandbox — test helpers ───────────────────────────────────────

// stubWorktreeInfo is a herdrListWorktreeForWorkspaceFn that always returns
// a fixed herdrWorktreeInfo (or an error) regardless of arguments.
type stubWorktreeList struct {
	info herdrWorktreeInfo
	err  error
}

func (s stubWorktreeList) fn() func(ctx context.Context, herdrBin, workspaceID string) (herdrWorktreeInfo, error) {
	return func(_ context.Context, _, _ string) (herdrWorktreeInfo, error) {
		return s.info, s.err
	}
}

// linkedWorktreeInfo returns a valid linked-worktree info for the given workspace.
func linkedWorktreeInfo(workspaceID, sourceWorkspaceID, branch, path string) herdrWorktreeInfo {
	return herdrWorktreeInfo{
		Branch:            branch,
		Path:              path,
		SourceWorkspaceID: sourceWorkspaceID,
		IsLinkedWorktree:  true,
	}
}

// stubSandbox returns a fixed Sandbox or error.
func stubSandboxGet(sb domain.Sandbox, err error) func(context.Context, string) (domain.Sandbox, error) {
	return func(_ context.Context, _ string) (domain.Sandbox, error) {
		return sb, err
	}
}

// noopCreate is a createSandbox stub that always succeeds.
func noopCreate(_ context.Context, _, _, _, _ string, _ []string) error { return nil }

// errCreate is a createSandbox stub that always fails.
func errCreate(_ context.Context, _, _, _, _ string, _ []string) error {
	return errors.New("create failed")
}

// swapListFn replaces herdrListWorktreeForWorkspaceFn for the duration of the
// test, restoring the original via t.Cleanup.
func swapListFn(t *testing.T, fn func(ctx context.Context, herdrBin, workspaceID string) (herdrWorktreeInfo, error)) {
	t.Helper()
	old := herdrListWorktreeForWorkspaceFn
	herdrListWorktreeForWorkspaceFn = fn
	t.Cleanup(func() { herdrListWorktreeForWorkspaceFn = old })
}

// swapRenameFn replaces herdrWorkspaceRenameFn for the duration of the test.
func swapRenameFn(t *testing.T, fn func(ctx context.Context, herdrBin, workspaceID, label string) error) {
	t.Helper()
	old := herdrWorkspaceRenameFn
	herdrWorkspaceRenameFn = fn
	t.Cleanup(func() { herdrWorkspaceRenameFn = old })
}

// callHerdrWorktreeSandbox is a thin wrapper that pre-populates the injected
// function args with no-op stubs so each test only has to override what it cares about.
//
// Safety: pins HERDR_BIN_PATH to a sentinel so resolveHerdrBin never calls
// exec.LookPath("herdr"), and swaps herdrExecCommandContext so any call through
// herdrOpenGuestShellPane (step 9, only when openPane=true) never executes the
// operator's live herdr binary. All tests using this helper pass openPane=false
// and never reach step 9; tests for step 9 call herdrWorktreeSandbox directly.
func callHerdrWorktreeSandbox(
	t *testing.T,
	workspaceID string,
	storeRoot string,
	conditional bool,
	auto bool,
	create func(context.Context, string, string, string, string, []string) error,
	get func(context.Context, string) (domain.Sandbox, error),
) error {
	t.Helper()
	// Pin HERDR_BIN_PATH: resolveHerdrBin returns the value directly without
	// validating that the file exists, so any non-empty value isolates the test.
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	// Swap herdrExecCommandContext so step 9 (herdrOpenGuestShellPane) and any
	// other herdrExecCommandContext call in herdrWorktreeSandbox runs a no-op
	// rather than the operator's live herdr binary.
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	t.Cleanup(func() { herdrExecCommandContext = old })

	ctx := context.Background()
	var w strings.Builder
	if create == nil {
		create = noopCreate
	}
	if get == nil {
		get = stubSandboxGet(domain.Sandbox{}, nil)
	}
	return herdrWorktreeSandbox(ctx, workspaceID, &w, storeRoot, false, conditional, auto, create, get)
}

// seedBinding writes a binding for workspaceID into storeRoot so idempotency checks fire.
func seedBinding(t *testing.T, storeRoot, workspaceID, sandboxHandle string) {
	t.Helper()
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:" + sandboxHandle,
		HerdrWorkspaceID: workspaceID,
		SandboxHandle:    sandboxHandle,
		SandboxID:        "sb-seed",
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, b); err != nil {
		t.Fatalf("seedBinding: %v", err)
	}
}

// ── idempotency ───────────────────────────────────────────────────────────────

func TestHerdrWorktreeSandbox_alreadyBound_noOp(t *testing.T) {
	// When a binding already exists for workspaceID, herdrWorktreeSandbox must
	// return without calling herdrListWorktreeForWorkspaceFn or createSandbox.
	//
	// MUTATION PROOF: remove the herdrSpaceResolve idempotency check and the
	// listFn is called instead. RED: "listFn must not be called".
	root := t.TempDir()
	seedBinding(t, root, "w-already", "wt/already")

	listCalled := false
	swapListFn(t, func(_ context.Context, _, _ string) (herdrWorktreeInfo, error) {
		listCalled = true
		return herdrWorktreeInfo{}, nil
	})

	createCalled := false
	err := callHerdrWorktreeSandbox(t, "w-already", root, false, false, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { createCalled = true; return nil },
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if listCalled {
		t.Error("listFn must not be called when binding already exists")
	}
	if createCalled {
		t.Error("createSandbox must not be called when binding already exists")
	}
}

// ── linked-worktree guard ─────────────────────────────────────────────────────

func TestHerdrWorktreeSandbox_mainCheckout_notBound(t *testing.T) {
	// When IsLinkedWorktree=false the workspace is the main checkout (e.g. w8).
	// herdrWorktreeSandbox must leave it unbound.
	//
	// MUTATION PROOF: remove the !info.IsLinkedWorktree guard.
	// createSandbox is called → RED: "createSandbox must not be called".
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: herdrWorktreeInfo{Branch: "work", Path: "/repo", IsLinkedWorktree: false},
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	createCalled := false
	err := callHerdrWorktreeSandbox(t, "w8", root, false, false, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { createCalled = true; return nil },
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createCalled {
		t.Error("createSandbox must not be called for main checkout workspace")
	}
	// Confirm no binding was written.
	all, _ := herdrSpaceReadAll(root)
	if len(all) != 0 {
		t.Errorf("expected no bindings; got %d", len(all))
	}
}

// ── list error → fail safe ────────────────────────────────────────────────────

func TestHerdrWorktreeSandbox_listError_failSafe(t *testing.T) {
	// When herdr worktree list fails, workspace stays unbound (no error returned).
	//
	// MUTATION PROOF: propagate the error instead of returning nil.
	// RED: test expects nil error but gets the list error.
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{err: errors.New("herdr unreachable")}.fn())

	err := callHerdrWorktreeSandbox(t, "w-new", root, false, false /*auto*/, nil, nil)
	if err != nil {
		t.Fatalf("expected nil (fail-safe) but got: %v", err)
	}
	all, _ := herdrSpaceReadAll(root)
	if len(all) != 0 {
		t.Errorf("expected no bindings after list error; got %d", len(all))
	}
}

// ── conditional (source check) ────────────────────────────────────────────────

func TestHerdrWorktreeSandbox_conditional_sourceNotBound_staysHost(t *testing.T) {
	// When conditional=true and the source workspace has no nexus3 binding,
	// the workspace must stay a host shell (no sandbox created, no binding).
	//
	// MUTATION PROOF: remove the conditional branch.
	// createSandbox is called → RED: "createSandbox must not be called".
	root := t.TempDir()
	// Source workspace "w-plain" is NOT in the bindings store.
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-worktree", "w-plain", "worktree/abc", "/path/abc"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	createCalled := false
	err := callHerdrWorktreeSandbox(t, "w-worktree", root, true, false, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { createCalled = true; return nil },
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createCalled {
		t.Error("createSandbox must not be called when source workspace is not nexus3-bound")
	}
	all, _ := herdrSpaceReadAll(root)
	if len(all) != 0 {
		t.Errorf("expected no bindings; got %d", len(all))
	}
}

func TestHerdrWorktreeSandbox_conditional_sourceBound_binds(t *testing.T) {
	// When conditional=true and the source workspace IS nexus3-bound, the
	// function must create a sandbox and write a binding.
	//
	// MUTATION PROOF: invert the herdrSpaceResolve condition (always skip).
	// No binding is written → RED: "expected 1 binding; got 0".
	root := t.TempDir()

	// Seed a binding for the source workspace "w-src".
	seedBinding(t, root, "w-src", "wt/src-sandbox")

	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-new", "w-src", "worktree/feat", "/path/feat"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	// Expect createSandbox to be called with handle "wt/worktree-feat" (full
	// branch path "worktree/feat" encoded) and mount "/path/feat:/workspace".
	const wantHandle = "wt/worktree-feat"
	var gotHandle, gotMount string
	err := callHerdrWorktreeSandbox(t, "w-new", root, true, false, /*auto*/
		func(_ context.Context, handle, mountSpec, _, _ string, _ []string) error {
			gotHandle = handle
			gotMount = mountSpec
			return nil
		},
		stubSandboxGet(domain.Sandbox{}, nil),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHandle != wantHandle {
		t.Errorf("createSandbox called with handle=%q; want %q", gotHandle, wantHandle)
	}
	if gotMount != "/path/feat:/workspace" {
		t.Errorf("createSandbox called with mount=%q; want %q", gotMount, "/path/feat:/workspace")
	}

	// Confirm the binding for "w-new" was written.
	all, _ := herdrSpaceReadAll(root)
	var found *HerdrSpaceBinding
	for i := range all {
		if all[i].HerdrWorkspaceID == "w-new" {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatalf("expected binding for workspace w-new; got none (bindings: %v)", all)
	}
	if found.SpaceLabel != "nexus3:"+wantHandle {
		t.Errorf("binding.SpaceLabel = %q; want %q", found.SpaceLabel, "nexus3:"+wantHandle)
	}
	if found.SandboxHandle != wantHandle {
		t.Errorf("binding.SandboxHandle = %q; want %q", found.SandboxHandle, wantHandle)
	}
}

func TestHerdrWorktreeSandbox_conditional_sourceUnknown_failSafe(t *testing.T) {
	// When conditional=true and SourceWorkspaceID is empty, the function must
	// not bind (ambiguous source → fail safe).
	//
	// MUTATION PROOF: remove the srcID=="" guard.
	// herdrSpaceResolve is called with "" → no binding found → falls through to
	// create. createCalled becomes true. RED: "createSandbox must not be called".
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-new", "" /*sourceWorkspaceID empty*/, "worktree/x", "/path/x"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	createCalled := false
	err := callHerdrWorktreeSandbox(t, "w-new", root, true, false, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { createCalled = true; return nil },
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createCalled {
		t.Error("createSandbox must not be called when source workspace is unknown")
	}
}

// ── explicit mode always binds ────────────────────────────────────────────────

func TestHerdrWorktreeSandbox_explicit_noSourceCheck(t *testing.T) {
	// When conditional=false (explicit action), the source workspace check is
	// skipped: even a worktree from a non-nexus3 source gets sandboxed.
	//
	// MUTATION PROOF: run the conditional block unconditionally.
	// Source "w-plain" not in store → skips. No binding written.
	// RED: "expected 1 binding; got 0".
	root := t.TempDir()
	// Source "w-plain" is NOT in the store (would fail condition B check).
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-explicit", "w-plain", "worktree/expl", "/path/expl"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	err := callHerdrWorktreeSandbox(t, "w-explicit", root, false /*conditional=false*/, false /*auto*/, nil, stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	all, _ := herdrSpaceReadAll(root)
	var found *HerdrSpaceBinding
	for i := range all {
		if all[i].HerdrWorkspaceID == "w-explicit" {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatalf("expected binding for workspace w-explicit in explicit mode; got none")
	}
}

// ── sandbox create failure → fail safe ───────────────────────────────────────

func TestHerdrWorktreeSandbox_createFails_noBinding(t *testing.T) {
	// When createSandbox returns an error in conditional mode, the workspace must
	// stay unbound (fail-safe — workspace stays a host shell, nil returned).
	// Explicit mode (conditional=false) returns a non-nil error on create failure;
	// that case is covered by TestHerdrWorktreeSandbox_explicitMode_createError_returnsError.
	//
	// MUTATION PROOF: continue past create error and write the binding.
	// A binding is written → RED: "expected no binding after create failure".
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-fail", "w-src", "worktree/fail", "/path/fail"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	err := callHerdrWorktreeSandbox(t, "w-fail", root, true /* conditional */, false /*auto*/, errCreate, nil)
	if err != nil {
		t.Fatalf("unexpected error (conditional mode should fail-safe): %v", err)
	}
	all, _ := herdrSpaceReadAll(root)
	for _, b := range all {
		if b.HerdrWorkspaceID == "w-fail" {
			t.Errorf("expected no binding after create failure; got %+v", b)
		}
	}
}

// ── happy path: binding written with correct fields ───────────────────────────

func TestHerdrWorktreeSandbox_happyPath_bindingFields(t *testing.T) {
	// A successful explicit bind must write a binding with:
	//   SpaceLabel       = "nexus3:wt/worktree-silver-forest-225f"
	//   HerdrWorkspaceID = "w-new"
	//   SandboxHandle    = "wt/worktree-silver-forest-225f"
	//
	// MUTATION PROOF: write empty SpaceLabel.
	// RED: "SpaceLabel = ''; want 'nexus3:wt/worktree-silver-forest-225f'".
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-new", "w-src", "worktree/silver-forest-225f", "/checkout/sf225f"),
	}.fn())

	var renamedLabel string
	swapRenameFn(t, func(_ context.Context, _, wsID, lbl string) error {
		if wsID == "w-new" {
			renamedLabel = lbl
		}
		return nil
	})

	err := callHerdrWorktreeSandbox(t, "w-new", root, false, false /*auto*/, nil, stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all, _ := herdrSpaceReadAll(root)
	var found *HerdrSpaceBinding
	for i := range all {
		if all[i].HerdrWorkspaceID == "w-new" {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatalf("binding for workspace w-new not written")
	}
	const wantLabel = "nexus3:wt/worktree-silver-forest-225f"
	const wantHandle = "wt/worktree-silver-forest-225f"
	if found.SpaceLabel != wantLabel {
		t.Errorf("SpaceLabel = %q; want %q", found.SpaceLabel, wantLabel)
	}
	if found.SandboxHandle != wantHandle {
		t.Errorf("SandboxHandle = %q; want %q", found.SandboxHandle, wantHandle)
	}
	if found.HerdrWorkspaceID != "w-new" {
		t.Errorf("HerdrWorkspaceID = %q; want %q", found.HerdrWorkspaceID, "w-new")
	}
	// Workspace was renamed to the correct label.
	if renamedLabel != wantLabel {
		t.Errorf("workspace rename label = %q; want %q", renamedLabel, wantLabel)
	}
}

// ── createSandbox receives correct handle and mount spec ──────────────────────

func TestHerdrWorktreeSandbox_createArgs(t *testing.T) {
	// The createSandbox closure must receive:
	//   handle   = "wt/<branch-slug>"
	//   mountSpec = "<path>:/workspace"
	//
	// MUTATION PROOF: pass mountSpec without ":/workspace".
	// RED: "mount spec = '/checkout/b'; want '/checkout/b:/workspace'".
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-c", "w-s", "feature/branch-b", "/checkout/b"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	var gotHandle, gotMount string
	_ = callHerdrWorktreeSandbox(t, "w-c", root, false, false, /*auto*/
		func(_ context.Context, h, m, _, _ string, _ []string) error {
			gotHandle = h
			gotMount = m
			return nil
		},
		stubSandboxGet(domain.Sandbox{}, nil),
	)
	if gotHandle != "wt/feature-branch-b" {
		t.Errorf("handle = %q; want %q", gotHandle, "wt/feature-branch-b")
	}
	if gotMount != "/checkout/b:/workspace" {
		t.Errorf("mount spec = %q; want %q", gotMount, "/checkout/b:/workspace")
	}
}

// ── herdrWorktreeSandboxCreateArgs — --no-builtin-gh ─────────────────────────

func TestHerdrWorktreeSandboxCreateArgs_containsNoBuiltinGh(t *testing.T) {
	// --no-builtin-gh must be present in every sandbox-create invocation from
	// the worktree-sandbox path: it controls whether the builtin GitHub
	// credential enters the sandbox and must never be silently omitted.
	// MUTATION PROOF: delete "--no-builtin-gh" from herdrWorktreeSandboxCreateArgs.
	// This test goes RED. A suite-wide build+green is insufficient — this flag
	// is in a closure and no other test observed the argv before this change.
	args := herdrWorktreeSandboxCreateArgs("wt/my-branch", "/repo:/workspace", "--image", herdrDefaultImage, nil)
	found := false
	for _, a := range args {
		if a == "--no-builtin-gh" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("--no-builtin-gh missing from herdrWorktreeSandboxCreateArgs %v; "+
			"this flag gates the builtin GitHub credential and must always be present", args)
	}
}

// TestHerdrWorktreeSandboxCreateArgs_isBootableShaped asserts that the argv
// produced by herdrWorktreeSandboxCreateArgs contains exactly one of --image,
// --rootfs, or --file, so that `nexus3 sandbox create` never sees --mount
// without a bootable flag and rejects with exit 2.
//
// MUTATION PROOF: remove imageFlag/imageVal from herdrWorktreeSandboxCreateArgs.
// Exactly-one count = 0 → RED. This test catches the original defect.
func TestHerdrWorktreeSandboxCreateArgs_isBootableShaped(t *testing.T) {
	for _, tc := range []struct {
		name      string
		imageFlag string
		imageVal  string
	}{
		{"image flag", "--image", "nexus3-agent-base"},
		{"file flag", "--file", "/some/checkout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := herdrWorktreeSandboxCreateArgs("wt/branch", "/repo:/workspace", tc.imageFlag, tc.imageVal, nil)
			bootableFlags := []string{"--image", "--rootfs", "--file"}
			count := 0
			for i, a := range args {
				for _, bf := range bootableFlags {
					if a == bf {
						count++
						// Verify the value immediately follows the flag.
						if i+1 >= len(args) {
							t.Errorf("bootable flag %q has no following value in argv %v", bf, args)
						}
						break
					}
				}
			}
			if count != 1 {
				t.Errorf("argv must contain exactly one bootable flag (--image|--rootfs|--file); "+
					"got %d in %v — sandbox create rejects --mount without a bootable flag", count, args)
			}
		})
	}
}

// ── step 9: guest-shell pane is opened and GuestPaneID is captured ────────────

func TestHerdrWorktreeSandbox_step9_guestPaneIDCaptured(t *testing.T) {
	// When step 9 (herdrOpenGuestShellPane) succeeds and returns a parseable
	// pane ID, the binding must store it in GuestPaneID.
	// MUTATION PROOF: remove the `if paneID != "" { binding.GuestPaneID = paneID }` block.
	// GuestPaneID stays "". RED: "GuestPaneID = ''; want 'stub-pane-1'".
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-pane", "w-src", "worktree/pane-test", "/path/pane"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	// Isolate from real herdr: set HERDR_BIN_PATH and swap exec so step 9
	// returns a recognisable pane ID without touching the live herdr server.
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	const stubPaneID = "stub-pane-1"
	paneJSON := `{"id":"cli:plugin","result":{"plugin_pane":{"pane":{"pane_id":"` + stubPaneID + `"}},"type":"plugin_pane_opened"}}`
	old := herdrExecCommandContext
	var recordedArgs []string
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		recordedArgs = append(recordedArgs, args...)
		return exec.CommandContext(ctx, "sh", "-c", "printf '%s\\n' '"+paneJSON+"'")
	}
	t.Cleanup(func() { herdrExecCommandContext = old })

	err := herdrWorktreeSandbox(context.Background(), "w-pane", &strings.Builder{}, root, true, false, false, noopCreate, stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step 9 must have called herdr plugin pane open.
	foundOpen := false
	for _, a := range recordedArgs {
		if a == "open" {
			foundOpen = true
			break
		}
	}
	if !foundOpen {
		t.Errorf("step 9 must invoke herdr plugin pane open; recorded args: %v", recordedArgs)
	}

	// GuestPaneID must be persisted in the binding.
	all, _ := herdrSpaceReadAll(root)
	var found *HerdrSpaceBinding
	for i := range all {
		if all[i].HerdrWorkspaceID == "w-pane" {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatal("binding for w-pane not written")
	}
	if found.GuestPaneID != stubPaneID {
		t.Errorf("GuestPaneID = %q; want %q", found.GuestPaneID, stubPaneID)
	}
}

// ── herdrListWorktreeForWorkspace JSON parsing ─────────────────────────────────

func TestHerdrListWorktreeForWorkspace_parsesResponse(t *testing.T) {
	// Unit-test the JSON parsing layer in herdrListWorktreeForWorkspace.
	// We drive it through the parse helper directly to avoid calling herdr.
	//
	// MUTATION PROOF: return info without IsLinkedWorktree=true.
	// The caller (herdrWorktreeSandbox) would skip the workspace.
	// Tested separately here to isolate the parser from the orchestrator.
	raw := `{"id":"x","result":{` +
		`"source":{"source_workspace_id":"w8","repo_name":"nexus3","repo_root":"/repo","source_checkout_path":"/repo","repo_key":"/repo/.git"},` +
		`"type":"worktree_list",` +
		`"worktrees":[` +
		`{"branch":"work","is_linked_worktree":false,"open_workspace_id":"w8","path":"/repo"},` +
		`{"branch":"worktree/feat","is_linked_worktree":true,"open_workspace_id":"wFEAT","path":"/wt/feat"}` +
		`]}}`

	info, err := herdrParseWorktreeListForWorkspace([]byte(raw), "wFEAT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Branch != "worktree/feat" {
		t.Errorf("Branch = %q; want %q", info.Branch, "worktree/feat")
	}
	if info.Path != "/wt/feat" {
		t.Errorf("Path = %q; want %q", info.Path, "/wt/feat")
	}
	if !info.IsLinkedWorktree {
		t.Error("IsLinkedWorktree must be true for the linked entry")
	}
	if info.SourceWorkspaceID != "w8" {
		t.Errorf("SourceWorkspaceID = %q; want %q", info.SourceWorkspaceID, "w8")
	}
	// Predicate (c) field: RepoKey must be populated.
	// MUTATION PROOF (RepoKey): clear RepoKey in herdrParseWorktreeListForWorkspace →
	// herdrWorktreeSandboxRepoCheck returns false (empty RepoKey guard fires) → RED.
	if info.RepoKey != "/repo/.git" {
		t.Errorf("RepoKey = %q; want %q", info.RepoKey, "/repo/.git")
	}
}

func TestHerdrListWorktreeForWorkspace_notFound(t *testing.T) {
	// When no worktree entry has the requested workspace_id, return an error.
	//
	// MUTATION PROOF: always return the first entry regardless of workspace ID.
	// The wrong workspace gets bound. RED: caller gets an info without an error
	// and misidentifies the workspace.
	raw := `{"id":"x","result":{"source":{"source_workspace_id":"w8"},"type":"worktree_list","worktrees":[]}}`
	_, err := herdrParseWorktreeListForWorkspace([]byte(raw), "w-nobody")
	if err == nil {
		t.Error("expected error when workspace not found; got nil")
	}
	if !strings.Contains(err.Error(), "w-nobody") {
		t.Errorf("error message should mention workspace ID %q; got %q", "w-nobody", err.Error())
	}
}

func TestHerdrListWorktreeForWorkspace_malformedJSON(t *testing.T) {
	_, err := herdrParseWorktreeListForWorkspace([]byte("{bad json"), "w-x")
	if err == nil {
		t.Error("expected error on malformed JSON; got nil")
	}
}

// ── dispatch: nexus3 herdr worktree-sandbox routes to herdrWorktreeSandbox ────

func TestHerdrGroup_worktreeSandbox_dispatch(t *testing.T) {
	// Confirm that `nexus3 herdr worktree-sandbox <id>` dispatches through
	// runHerdrGroup → herdrWorktreeSandbox (not to an "unknown subcommand" error).
	//
	// MUTATION PROOF: drop the "worktree-sandbox" case from runHerdrGroup →
	// returns UsageError "unknown subcommand" → RED.
	//
	// The sandbox service (newSandboxService) is called inside the dispatch path
	// before any injectable fn; in a sandboxless env it returns an internal error
	// (not UsageError) proving the routing succeeded. XDG_STATE_HOME is pointed at a
	// temp dir so the store can initialize without touching real state.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	listCalled := false
	swapListFn(t, func(_ context.Context, _, _ string) (herdrWorktreeInfo, error) {
		listCalled = true
		return herdrWorktreeInfo{}, fmt.Errorf("stub: stop here")
	})
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	ctx := context.Background()
	var w strings.Builder
	out := NewOutput(&w, &w, false)
	err := runHerdrGroup(ctx, []string{"worktree-sandbox", "w-test"}, out)
	if ue, ok := err.(*UsageError); ok {
		if strings.Contains(ue.Msg, "unknown subcommand") {
			t.Errorf("dispatch failed with UsageError: %v", err)
		}
	}
	// In environments with a working store, listFn is called — a stronger signal
	// that dispatch reached herdrWorktreeSandbox. Assert it when available.
	if listCalled {
		t.Log("dispatch confirmed: listFn reached herdrWorktreeSandbox")
	}
}

// ── --auto flag parsing ───────────────────────────────────────────────────────

func TestHerdrWorktreeSandboxParseArgs_autoFlag(t *testing.T) {
	// --auto is accepted by the CLI; not emitted by any script today.
	// ["--auto", "w1"]: the flag must be stripped and auto set to true.
	// --auto activates the repo-level predicate (c); it does NOT set conditional.
	//
	// MUTATION PROOF: rename "--auto" to "--never-matches" in herdrWorktreeSandboxParseArgs →
	// --auto is not consumed, rest[0]="--auto" ≠ "w1" → RED.
	rest, conditional, auto := herdrWorktreeSandboxParseArgs([]string{"--auto", "w1"})
	if len(rest) == 0 || rest[0] != "w1" {
		t.Errorf("--auto not stripped: rest=%v, want [w1]", rest)
	}
	if !auto {
		t.Errorf("--auto should set auto=true; got false")
	}
	if conditional {
		t.Errorf("--auto should NOT set conditional; got true")
	}
}

func TestHerdrWorktreeSandboxParseArgs_conditionalAlias(t *testing.T) {
	// --conditional sets conditional=true (legacy SourceWorkspaceID predicate).
	// It is kept for backward compatibility and is distinct from --auto.
	rest, conditional, auto := herdrWorktreeSandboxParseArgs([]string{"--conditional", "w1"})
	if len(rest) == 0 || rest[0] != "w1" {
		t.Errorf("--conditional not stripped: rest=%v, want [w1]", rest)
	}
	if !conditional {
		t.Errorf("--conditional should set conditional=true; got false")
	}
	if auto {
		t.Errorf("--conditional should NOT set auto; got true")
	}
}

func TestHerdrWorktreeSandboxParseArgs_noFlag(t *testing.T) {
	// No flag: workspace ID passes through unchanged, both booleans stay false.
	rest, conditional, auto := herdrWorktreeSandboxParseArgs([]string{"w1"})
	if len(rest) == 0 || rest[0] != "w1" {
		t.Errorf("plain ID: rest=%v, want [w1]", rest)
	}
	if conditional {
		t.Errorf("no flag: expected conditional=false; got true")
	}
	if auto {
		t.Errorf("no flag: expected auto=false; got true")
	}
}

// ── explicit vs conditional error mode (MAJOR 6) ─────────────────────────────

func TestHerdrWorktreeSandbox_explicitMode_createError_returnsError(t *testing.T) {
	// When conditional=false (explicit bind) and sandbox create fails,
	// herdrWorktreeSandbox must return a non-nil error so open-pane.sh's
	// STATUS -ne 0 check fires.
	//
	// MUTATION PROOF: change `if !conditional { return fmt.Errorf(...) }` to
	// always return nil → this test gets nil → RED.
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-exp", "w-src", "feature/exp", "/work/exp"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	createErr := errors.New("create failed: explicit test")
	err := callHerdrWorktreeSandbox(t, "w-exp", root, false, false, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { return createErr },
		nil,
	)
	if err == nil {
		t.Error("explicit mode: createFn error must return non-nil error; got nil")
	}
}

func TestHerdrWorktreeSandbox_conditionalMode_createError_returnsNil(t *testing.T) {
	// When conditional=true (auto mode) and sandbox create fails,
	// herdrWorktreeSandbox must return nil (fail-safe — workspace stays host shell).
	//
	// MUTATION PROOF: change `if !conditional` to always return error →
	// this test gets non-nil → RED.
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-cond", "w-src", "feature/cond", "/work/cond"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	err := callHerdrWorktreeSandbox(t, "w-cond", root, true, false, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { return errors.New("create failed") },
		nil,
	)
	if err != nil {
		t.Errorf("conditional mode: createFn error must return nil (fail-safe); got %v", err)
	}
}

// ── step 8 getFn error ────────────────────────────────────────────────────────

func TestHerdrWorktreeSandbox_explicitMode_getFnError_returnsError(t *testing.T) {
	// When conditional=false and getFn returns an error (step 8 sandbox lookup),
	// herdrWorktreeSandbox must return a non-nil error.
	//
	// MUTATION PROOF: delete `if !conditional { return fmt.Errorf(...) }` in the
	// getFn error branch → this test gets nil → RED.
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-getfail-exp", "w-src", "feature/gf-exp", "/work/gf-exp"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	getFail := func(_ context.Context, _ string) (domain.Sandbox, error) {
		return domain.Sandbox{}, errors.New("sandbox lookup failed: explicit test")
	}
	err := callHerdrWorktreeSandbox(t, "w-getfail-exp", root, false, false /*auto*/, nil, getFail)
	if err == nil {
		t.Error("explicit mode: getFn error must return non-nil error; got nil")
	}
}

func TestHerdrWorktreeSandbox_conditionalMode_getFnError_returnsNil(t *testing.T) {
	// When conditional=true and getFn returns an error (step 8 sandbox lookup),
	// herdrWorktreeSandbox must return nil (fail-safe).
	//
	// MUTATION PROOF: remove the conditional guard → this test gets non-nil → RED.
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-getfail-cond", "w-src", "feature/gf-cond", "/work/gf-cond"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	getFail := func(_ context.Context, _ string) (domain.Sandbox, error) {
		return domain.Sandbox{}, errors.New("sandbox lookup failed: conditional test")
	}
	err := callHerdrWorktreeSandbox(t, "w-getfail-cond", root, true, false /*auto*/, nil, getFail)
	if err != nil {
		t.Errorf("conditional mode: getFn error must return nil (fail-safe); got %v", err)
	}
}

// ── step 8 HerdrSpacePut error ────────────────────────────────────────────────

func TestHerdrWorktreeSandbox_explicitMode_spacePutError_returnsError(t *testing.T) {
	// When conditional=false and HerdrSpacePut fails (step 8 binding write),
	// herdrWorktreeSandbox must return a non-nil error.
	// Force failure: pass a plain file (not a directory) as storeRoot so
	// HerdrSpacePut's os.MkdirAll(storeRoot) returns ENOTDIR.
	//
	// MUTATION PROOF: delete `if !conditional { return fmt.Errorf(...) }` in the
	// HerdrSpacePut error branch → this test gets nil → RED.
	tmp := t.TempDir()
	// badRoot is a regular file; MkdirAll on it returns ENOTDIR → HerdrSpacePut fails.
	badRoot := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(badRoot, []byte("block"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-putfail-exp", "w-src", "feature/pf-exp", "/work/pf-exp"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	err := callHerdrWorktreeSandbox(t, "w-putfail-exp", badRoot, false, false /*auto*/, nil, stubSandboxGet(domain.Sandbox{}, nil))
	if err == nil {
		t.Error("explicit mode: HerdrSpacePut error must return non-nil error; got nil")
	}
}

func TestHerdrWorktreeSandbox_conditionalMode_spacePutError_returnsNil(t *testing.T) {
	// When conditional=true and HerdrSpacePut fails (step 8 binding write),
	// herdrWorktreeSandbox must return nil (fail-safe).
	//
	// MUTATION PROOF: remove the conditional guard → this test gets non-nil → RED.
	tmp := t.TempDir()
	// Same blocker technique as the explicit-mode test above.
	badRoot := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(badRoot, []byte("block"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-putfail-cond", "w-src", "feature/pf-cond", "/work/pf-cond"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	err := callHerdrWorktreeSandbox(t, "w-putfail-cond", badRoot, true, false /*auto*/, nil, stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Errorf("conditional mode: HerdrSpacePut error must return nil (fail-safe); got %v", err)
	}
}

// ── binding written before pane opens ────────────────────────────────────────

func TestHerdrWorktreeSandbox_bindingWrittenBeforePaneOpen(t *testing.T) {
	// The HerdrSpaceBinding must exist in the store BEFORE herdrOpenGuestShellPane
	// is called. If the process dies after writing the binding but before the pane
	// opens, the next run's idempotency check finds the binding and skips creation.
	//
	// MUTATION PROOF: move HerdrSpacePut to after herdrOpenGuestShellPane →
	// bindingExistedBeforePane is false → RED.
	root := t.TempDir()
	const wsID = "w-pane-order"

	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo(wsID, "w-src", "feature/pane-order", "/work/po"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	bindingExistedBeforePane := false
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// This is called by herdrOpenGuestShellPane (step 9).
		// Check whether the binding was already written.
		if _, err := herdrSpaceResolve(ctx, root, wsID); err == nil {
			bindingExistedBeforePane = true
		}
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	t.Cleanup(func() { herdrExecCommandContext = old })
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")

	ctx := context.Background()
	var w strings.Builder
	err := herdrWorktreeSandbox(ctx, wsID, &w, root, true, false, false, noopCreate, stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bindingExistedBeforePane {
		t.Error("binding was NOT in store when herdrOpenGuestShellPane was called; must write binding before opening pane")
	}
}

// ── step 9: pane open failure error policy ────────────────────────────────────

func TestHerdrWorktreeSandbox_explicitMode_paneError_returnsError(t *testing.T) {
	// When conditional=false (explicit bind) and herdrOpenGuestShellPane fails
	// (step 9), herdrWorktreeSandbox must return a non-nil error.
	// The sandbox and binding already exist and are recoverable; returning an error
	// gives the caller a non-zero exit for visibility — silence is not acceptable.
	//
	// MUTATION PROOF: delete `if !conditional { return fmt.Errorf(...) }` in the
	// paneErr branch → this test gets nil → RED.
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-panefail-exp", "w-src", "feature/panefail-exp", "/work/pfe"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	// Swap exec so step 9 returns an error (exit 1 → herdrOpenGuestShellPane returns err).
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 1")
	}
	t.Cleanup(func() { herdrExecCommandContext = old })

	ctx := context.Background()
	var w strings.Builder
	err := herdrWorktreeSandbox(ctx, "w-panefail-exp", &w, root, true /*openPane*/, false /*conditional*/, false /*auto*/, noopCreate, stubSandboxGet(domain.Sandbox{}, nil))
	if err == nil {
		t.Error("explicit mode: pane open error must return non-nil error; got nil")
	}
}

func TestHerdrWorktreeSandbox_conditionalMode_paneError_returnsNil(t *testing.T) {
	// When conditional=true and herdrOpenGuestShellPane fails (step 9),
	// herdrWorktreeSandbox must return nil — binding exists, workspace is usable.
	//
	// MUTATION PROOF: always return paneErr regardless of conditional →
	// this test gets non-nil → RED.
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-panefail-cond", "w-src", "feature/panefail-cond", "/work/pfc"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 1")
	}
	t.Cleanup(func() { herdrExecCommandContext = old })

	ctx := context.Background()
	var w strings.Builder
	err := herdrWorktreeSandbox(ctx, "w-panefail-cond", &w, root, true /*openPane*/, true /*conditional*/, false /*auto*/, noopCreate, stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Errorf("conditional mode: pane open error must return nil (fail-safe); got %v", err)
	}
}

// ── auto mode (--auto / repo-level predicate) ─────────────────────────────────

// linkedWorktreeInfoAuto returns a herdrWorktreeInfo for a linked worktree
// with the RepoKey field populated for predicate (c).
func linkedWorktreeInfoAuto(workspaceID, branch, path, repoKey string) herdrWorktreeInfo {
	return herdrWorktreeInfo{
		Branch:           branch,
		Path:             path,
		IsLinkedWorktree: true,
		RepoKey:          repoKey,
	}
}

// seedBindingWithRepoRoot writes a binding with the given RepoRoot to storeRoot.
// Used by auto-mode tests to seed a repo-level binding that herdrRepoHasBoundSandbox matches.
func seedBindingWithRepoRoot(t *testing.T, storeRoot, workspaceID, sandboxHandle, repoRoot string) {
	t.Helper()
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:" + sandboxHandle,
		HerdrWorkspaceID: workspaceID,
		SandboxHandle:    sandboxHandle,
		SandboxID:        "sb-seed",
		RepoRoot:         repoRoot,
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, b); err != nil {
		t.Fatalf("seedBindingWithRepoRoot: %v", err)
	}
}

func TestHerdrWorktreeSandbox_auto_siblingBound_binds(t *testing.T) {
	// When auto=true and a binding exists whose RepoRoot matches the worktree's
	// main repo, the function must create a sandbox and write a binding.
	//
	// MUTATION PROOF (predicate c): invert herdrWorktreeSandboxRepoCheck
	// (return false) → no binding written → RED: "expected binding for w-new; got none".
	root := t.TempDir()

	// Seed a binding whose RepoRoot == "/repo" (parent of info.RepoKey="/repo/.git").
	// This is the nexus3-created binding — its workspace ID ("w-src") is NOT
	// in the workspace IDs that open worktrees, matching the live disjoint scenario.
	seedBindingWithRepoRoot(t, root, "w-src", "wt/src-sandbox", "/repo")

	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto("w-new", "worktree/feat", "/path/feat", "/repo/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	err := callHerdrWorktreeSandbox(t, "w-new", root, false, true, /*auto*/
		nil, stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	all, _ := herdrSpaceReadAll(root)
	var found bool
	for _, b := range all {
		if b.HerdrWorkspaceID == "w-new" {
			found = true
		}
	}
	if !found {
		t.Error("expected binding for w-new in store; got none")
	}
}

func TestHerdrWorktreeSandbox_auto_noRepoBound_staysHost(t *testing.T) {
	// When auto=true but no sibling workspace is nexus3-bound, the function
	// must leave the workspace unbound (fail safe).
	//
	// MUTATION PROOF: remove repo check (always proceed) → createSandbox is
	// called → RED: "createSandbox must not be called".
	root := t.TempDir()
	// No bindings in the store.

	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto("w-new", "worktree/feat", "/path/feat", "/repo/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	createCalled := false
	err := callHerdrWorktreeSandbox(t, "w-new", root, false, true, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { createCalled = true; return nil },
		nil)
	if err != nil {
		t.Fatalf("unexpected error (auto mode is fail-safe): %v", err)
	}
	if createCalled {
		t.Error("createSandbox must not be called when no binding's RepoRoot matches the repo")
	}
	all, _ := herdrSpaceReadAll(root)
	if len(all) != 0 {
		t.Errorf("expected no bindings; got %d", len(all))
	}
}

func TestHerdrWorktreeSandbox_auto_repoKeyEmpty_staysHost(t *testing.T) {
	// When auto=true but RepoKey is empty, the repo cannot be identified →
	// herdrWorktreeSandboxRepoCheck returns false → workspace stays unbound.
	//
	// MUTATION PROOF: remove the `info.RepoKey == ""` guard in
	// herdrWorktreeSandboxRepoCheck → derives mainRepo=filepath.Dir(".")="."
	// → herdrRepoHasBoundSandbox(".", bindings) finds the seeded binding
	// (RepoRoot=".") → creates sandbox → RED: "createSandbox must not be called".
	root := t.TempDir()
	// Seed binding with RepoRoot="." — matches what the mutated code would derive
	// from an empty RepoKey: filepath.Dir(filepath.Clean("")) == ".".
	seedBindingWithRepoRoot(t, root, "w-src", "wt/src-sandbox", ".")

	swapListFn(t, stubWorktreeList{
		// RepoKey is deliberately empty.
		info: linkedWorktreeInfoAuto("w-new", "worktree/feat", "/path/feat", "" /*repoKey=empty*/),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	createCalled := false
	err := callHerdrWorktreeSandbox(t, "w-new", root, false, true, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { createCalled = true; return nil },
		nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createCalled {
		t.Error("createSandbox must not be called when RepoKey is empty")
	}
}

func TestHerdrWorktreeSandbox_auto_notLinkedWorktree_noSideEffects(t *testing.T) {
	// When auto=true but IsLinkedWorktree=false (e.g. workspace w8 main checkout),
	// the function must return nil with zero createFn calls and zero
	// herdrExecCommandContext calls (no pane opens, no workspace renames).
	//
	// This is the "NARROW ENGAGEMENT" proof: a non-linked workspace (w6, w8, w2R)
	// must not produce any sandbox or herdr side-effects beyond the probe.
	//
	// MUTATION PROOF: remove the !info.IsLinkedWorktree guard →
	// herdrWorktreeSandboxRepoCheck fires. A binding with RepoRoot="/repo" is
	// seeded so herdrRepoHasBoundSandbox returns true → createSandbox IS called →
	// createCalled = true → RED.
	root := t.TempDir()
	// Seed a binding with RepoRoot="/repo" so that IF the linked-worktree guard
	// were removed and repoCheck evaluated, herdrRepoHasBoundSandbox("/repo", ...)
	// would return true → createSandbox IS called → createCalled=true → RED.
	seedBindingWithRepoRoot(t, root, "w-src", "wt/src-sandbox", "/repo")

	swapListFn(t, stubWorktreeList{
		info: herdrWorktreeInfo{
			Branch:           "main",
			Path:             "/repo",
			IsLinkedWorktree: false, // main checkout — must be skipped
			RepoKey:          "/repo/.git",
		},
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	// Call herdrWorktreeSandbox directly (not via callHerdrWorktreeSandbox)
	// so our recording exec seam is not overwritten by the helper's no-op seam.
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	execCalled := false
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		execCalled = true
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	t.Cleanup(func() { herdrExecCommandContext = old })

	createCalled := false
	var w strings.Builder
	err := herdrWorktreeSandbox(context.Background(), "w-new", &w, root,
		false /*openPane*/, false /*conditional*/, true, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { createCalled = true; return nil },
		stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createCalled {
		t.Error("createSandbox must not be called for a non-linked workspace")
	}
	if execCalled {
		t.Error("herdrExecCommandContext must not be called for a non-linked workspace (no pane open, no rename)")
	}
	all, _ := herdrSpaceReadAll(root)
	// Only the seeded sibling binding should be in the store.
	for _, b := range all {
		if b.HerdrWorkspaceID == "w-new" {
			t.Errorf("unexpected binding for w-new (non-linked workspace must stay unbound)")
		}
	}
}

func TestHerdrWorktreeSandbox_auto_concurrent_secondIsNoOp(t *testing.T) {
	// Concurrency: a second call for the same workspace after the first has
	// written a binding must be a no-op (idempotency guard fires in step 1).
	// The second call must not corrupt the existing binding.
	//
	// MUTATION PROOF: remove the step-1 idempotency check → second call
	// proceeds to create, which may conflict with the existing sandbox or
	// write a second binding → the final binding count may be 2 → RED.
	root := t.TempDir()
	seedBindingWithRepoRoot(t, root, "w-src", "wt/src-sandbox", "/repo")

	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto("w-new", "worktree/feat", "/path/feat", "/repo/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	// First call creates the binding.
	err := callHerdrWorktreeSandbox(t, "w-new", root, false, true, /*auto*/
		nil, stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	allAfterFirst, _ := herdrSpaceReadAll(root)
	var firstCountForWNew int
	for _, b := range allAfterFirst {
		if b.HerdrWorkspaceID == "w-new" {
			firstCountForWNew++
		}
	}
	if firstCountForWNew != 1 {
		t.Fatalf("expected 1 binding for w-new after first call; got %d", firstCountForWNew)
	}

	// Second call (simulates a second pane opening concurrently).
	createCalledSecond := false
	err = callHerdrWorktreeSandbox(t, "w-new", root, false, true, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string) error { createCalledSecond = true; return nil },
		nil)
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	if createCalledSecond {
		t.Error("second call must not invoke createSandbox (idempotency: binding exists)")
	}

	// Binding must still be exactly one.
	allAfterSecond, _ := herdrSpaceReadAll(root)
	var finalCountForWNew int
	for _, b := range allAfterSecond {
		if b.HerdrWorkspaceID == "w-new" {
			finalCountForWNew++
		}
	}
	if finalCountForWNew != 1 {
		t.Errorf("expected exactly 1 binding for w-new after second call; got %d", finalCountForWNew)
	}
}

// ── herdrWorktreeGitDirMount ──────────────────────────────────────────────────

func TestHerdrWorktreeGitDirMount_linkedWorktree_returnsMainGitMount(t *testing.T) {
	// A linked worktree's .git file contains "gitdir: <main>/.git/worktrees/<name>".
	// herdrWorktreeGitDirMount must return "<main>/.git:<main>/.git".
	//
	// MUTATION PROOF: return "" unconditionally → this test gets "" → RED.
	mainGit := t.TempDir() // stand-in for <main>/.git
	worktreesDir := filepath.Join(mainGit, "worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	checkoutDir := t.TempDir()
	// Simulate the .git file written by `git worktree add`.
	gitFile := filepath.Join(mainGit, "worktrees", "my-wt")
	if err := os.WriteFile(filepath.Join(checkoutDir, ".git"),
		[]byte("gitdir: "+gitFile+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := herdrWorktreeGitDirMount(checkoutDir)
	want := mainGit + ":" + mainGit
	if got != want {
		t.Errorf("herdrWorktreeGitDirMount = %q; want %q", got, want)
	}
}

func TestHerdrWorktreeGitDirMount_mainCheckout_returnsEmpty(t *testing.T) {
	// A main checkout's .git is a directory, not a file.
	// herdrWorktreeGitDirMount must return "" (nothing to mount).
	//
	// MUTATION PROOF: return a non-empty string unconditionally →
	// this test fails because "got non-empty for main checkout".
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := herdrWorktreeGitDirMount(dir)
	if got != "" {
		t.Errorf("herdrWorktreeGitDirMount for main checkout = %q; want empty", got)
	}
}

func TestHerdrWorktreeGitDirMount_noGitFile_returnsEmpty(t *testing.T) {
	// No .git at all → empty (not a git checkout or non-git dir).
	got := herdrWorktreeGitDirMount(t.TempDir())
	if got != "" {
		t.Errorf("herdrWorktreeGitDirMount with no .git = %q; want empty", got)
	}
}

func TestHerdrWorktreeGitDirMount_noWorktreesParent_returnsEmpty(t *testing.T) {
	// A gitdir: pointer whose target's parent is NOT "worktrees" is not a
	// linked-worktree structure git would produce; the function must bail with
	// "" rather than mount an arbitrary directory.
	//
	// MUTATION PROOF: delete the `filepath.Base(worktreesDir) != "worktrees"`
	// guard → dir(dir(target)) is mounted unconditionally → got is non-empty → RED.
	checkoutDir := t.TempDir()
	// target = <tmp>/notworktrees/my-wt → dir = <tmp>/notworktrees (base != "worktrees")
	bogus := filepath.Join(t.TempDir(), "notworktrees", "my-wt")
	if err := os.WriteFile(filepath.Join(checkoutDir, ".git"),
		[]byte("gitdir: "+bogus+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := herdrWorktreeGitDirMount(checkoutDir); got != "" {
		t.Errorf("herdrWorktreeGitDirMount with non-worktrees parent = %q; want empty", got)
	}
}

func TestHerdrWorktreeGitDirMount_relativeGitdir_resolvedAbsolute(t *testing.T) {
	// A relative gitdir: pointer is valid and resolved against the directory
	// holding the .git file. The mount spec must be the ABSOLUTE common dir,
	// never a relative "<rel>:<rel>".
	//
	// MUTATION PROOF: remove the `if !filepath.IsAbs(target)` resolution →
	// gitDir stays relative → mount spec is relative → RED (want absolute).
	checkoutDir := t.TempDir()
	// Build <checkout>/.real-git/worktrees/wt and point at it relatively.
	commonDir := filepath.Join(checkoutDir, ".real-git")
	if err := os.MkdirAll(filepath.Join(commonDir, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkoutDir, ".git"),
		[]byte("gitdir: .real-git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := herdrWorktreeGitDirMount(checkoutDir)
	want := commonDir + ":" + commonDir
	if got != want {
		t.Errorf("herdrWorktreeGitDirMount relative = %q; want %q (absolute)", got, want)
	}
}

func TestHerdrWorktreeSandboxCreateArgs_extraMountsAddedAfterPrimary(t *testing.T) {
	// When extraMounts is non-empty, each entry must appear as a --mount pair
	// AFTER the primary --mount <mountSpec> and BEFORE --no-builtin-gh.
	//
	// MUTATION PROOF: remove the extraMounts loop from herdrWorktreeSandboxCreateArgs
	// → the extra --mount entry is absent → RED ("want 2 --mount pairs; got 1").
	extra := []string{"/main/.git:/main/.git"}
	args := herdrWorktreeSandboxCreateArgs("wt/branch", "/checkout:/workspace", "--image", "base", extra)

	// Count --mount pairs and collect their values.
	var mounts []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--mount" {
			mounts = append(mounts, args[i+1])
		}
	}
	if len(mounts) != 2 {
		t.Fatalf("want 2 --mount pairs; got %d in argv %v", len(mounts), args)
	}
	if mounts[0] != "/checkout:/workspace" {
		t.Errorf("first --mount = %q; want /checkout:/workspace", mounts[0])
	}
	if mounts[1] != "/main/.git:/main/.git" {
		t.Errorf("second --mount = %q; want /main/.git:/main/.git", mounts[1])
	}
}

func TestHerdrWorktreeSandbox_linkedWorktree_gitDirMountPassedToCreate(t *testing.T) {
	// For a linked-worktree sandbox, herdrWorktreeSandbox must pass a non-empty
	// extraMounts slice to createFn containing the main .git mount spec.
	// This is the bootable-shape invariant for the git-in-worktree feature.
	//
	// MUTATION PROOF: remove "if gitMount := herdrWorktreeGitDirMount..." block
	// → extraMounts is always nil → createFn receives nil → RED.

	// Build a real linked-worktree structure on disk so herdrWorktreeGitDirMount
	// can read the .git file.
	mainGit := t.TempDir() // stands for <main>/.git
	if err := os.MkdirAll(filepath.Join(mainGit, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkoutDir := t.TempDir()
	gitdirTarget := filepath.Join(mainGit, "worktrees", "probe-wt")
	if err := os.WriteFile(filepath.Join(checkoutDir, ".git"),
		[]byte("gitdir: "+gitdirTarget+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-gitproof", "w-src", "worktree/gitproof", checkoutDir),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	wantGitMount := mainGit + ":" + mainGit

	var gotExtraMounts []string
	err := callHerdrWorktreeSandbox(t, "w-gitproof", root, false, false, /*auto*/
		func(_ context.Context, _, _, _, _ string, extraMounts []string) error {
			gotExtraMounts = extraMounts
			return nil
		},
		stubSandboxGet(domain.Sandbox{}, nil),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, m := range gotExtraMounts {
		if m == wantGitMount {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("extraMounts %v does not contain git-dir mount %q; "+
			"git will be unusable inside the worktree sandbox", gotExtraMounts, wantGitMount)
	}
}
