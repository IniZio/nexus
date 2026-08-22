package cli

// Tests for HerdrBackfillRepoRoot and related invariants.
//
// All seams are injected — no real herdr binary or live service is touched.
//
// Cases covered:
//  1. Backfill fills an empty RepoRoot from herdr workspace list.
//  2. Backfill does NOT overwrite a non-empty RepoRoot.      MUTATION PROOF → RED
//  3. Backfill skips a binding whose workspace herdr does not report.
//                                                             MUTATION PROOF → RED
//  4. Backfill is a no-op and returns an error when herdr call fails
//     (bindings file is untouched).
//  5. Round-trip: after backfill, herdrAutoCreatePredicateWith returns true
//     for a linked worktree of that repo.                    MUTATION PROOF → RED
//  6. NO-SPAWN PROOF: unbound non-worktree workspace → herdrDefaultShellCore
//     writes zero bytes and never invokes the auto-create seam.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// swapBackfillFn replaces herdrWorkspaceListForBackfillFn for the duration of
// the test, restoring the original via t.Cleanup.
func swapBackfillFn(t *testing.T, fn func(context.Context, string) ([]byte, error)) {
	t.Helper()
	old := herdrWorkspaceListForBackfillFn
	herdrWorkspaceListForBackfillFn = fn
	t.Cleanup(func() { herdrWorkspaceListForBackfillFn = old })
}

// makeWorkspaceListJSON returns the JSON that `herdr workspace list` would
// return given a map of workspace_id → repo_root.
func makeWorkspaceListJSON(entries map[string]string) []byte {
	var sb strings.Builder
	sb.WriteString(`{"result":{"workspaces":[`)
	first := true
	for id, root := range entries {
		if !first {
			sb.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&sb, `{"workspace_id":%q,"worktree":{"repo_root":%q}}`, id, root)
	}
	sb.WriteString(`]}}`)
	return []byte(sb.String())
}

// seedBackfillBinding writes a binding into storeRoot and returns it.
func seedBackfillBinding(t *testing.T, storeRoot string, b HerdrSpaceBinding) {
	t.Helper()
	if err := HerdrSpacePut(context.Background(), storeRoot, b); err != nil {
		t.Fatalf("seedBackfillBinding: %v", err)
	}
}

// readAllBackfill reads all bindings from storeRoot, fatally failing on error.
func readAllBackfill(t *testing.T, storeRoot string) []HerdrSpaceBinding {
	t.Helper()
	bs, err := HerdrSpaceList(context.Background(), storeRoot)
	if err != nil {
		t.Fatalf("readAllBackfill: %v", err)
	}
	return bs
}

// ── Test 1: fills empty RepoRoot ──────────────────────────────────────────────

func TestHerdrBackfillRepoRoot_FillsEmpty(t *testing.T) {
	// Binding with empty RepoRoot → backfill must populate it from workspace list.
	//
	// MUTATION PROOF (repo_root extraction deleted/broken): b.RepoRoot stays
	// empty → t.Errorf → RED.
	root := t.TempDir()
	ctx := context.Background()

	seedBackfillBinding(t, root, HerdrSpaceBinding{
		SpaceLabel:       "nexus3:dev/alpha",
		HerdrWorkspaceID: "w10",
		SandboxHandle:    "dev/alpha",
		SandboxID:        "sb-alpha",
		// RepoRoot intentionally empty (legacy binding).
	})

	swapBackfillFn(t, func(_ context.Context, _ string) ([]byte, error) {
		return makeWorkspaceListJSON(map[string]string{
			"w10": "/home/user/magic/myrepo",
		}), nil
	})

	var out bytes.Buffer
	n, err := HerdrBackfillRepoRoot(ctx, root, "ignored-bin", &out)
	if err != nil {
		t.Fatalf("HerdrBackfillRepoRoot: %v", err)
	}
	if n != 1 {
		t.Errorf("changed count = %d; want 1", n)
	}

	bindings := readAllBackfill(t, root)
	if len(bindings) != 1 {
		t.Fatalf("binding count = %d; want 1", len(bindings))
	}
	if got := bindings[0].RepoRoot; got != "/home/user/magic/myrepo" {
		t.Errorf("RepoRoot = %q; want %q", got, "/home/user/magic/myrepo")
	}
	if !strings.Contains(out.String(), "w10") {
		t.Errorf("output missing workspace ID %q; got %q", "w10", out.String())
	}
}

// ── Test 2: never overwrites non-empty RepoRoot ───────────────────────────────

func TestHerdrBackfillRepoRoot_DoesNotOverwriteNonEmpty(t *testing.T) {
	// Binding with an existing RepoRoot → backfill must leave it unchanged.
	//
	// MUTATION PROOF: remove the `if b.RepoRoot != "" { continue }` guard
	// in HerdrBackfillRepoRoot → existing value "/original/path" is overwritten
	// by "/new/path" → t.Errorf → RED.
	root := t.TempDir()
	ctx := context.Background()

	seedBackfillBinding(t, root, HerdrSpaceBinding{
		SpaceLabel:       "nexus3:dev/beta",
		HerdrWorkspaceID: "w20",
		SandboxHandle:    "dev/beta",
		SandboxID:        "sb-beta",
		RepoRoot:         "/original/path",
	})

	swapBackfillFn(t, func(_ context.Context, _ string) ([]byte, error) {
		return makeWorkspaceListJSON(map[string]string{
			"w20": "/new/path",
		}), nil
	})

	var out bytes.Buffer
	n, err := HerdrBackfillRepoRoot(ctx, root, "ignored-bin", &out)
	if err != nil {
		t.Fatalf("HerdrBackfillRepoRoot: %v", err)
	}
	if n != 0 {
		t.Errorf("changed count = %d; want 0 (must not overwrite existing RepoRoot)", n)
	}

	bindings := readAllBackfill(t, root)
	if len(bindings) != 1 {
		t.Fatalf("binding count = %d; want 1", len(bindings))
	}
	if got := bindings[0].RepoRoot; got != "/original/path" {
		t.Errorf("RepoRoot = %q; want %q (must not be overwritten)", got, "/original/path")
	}
}

// ── Test 3: skips unknown workspace ──────────────────────────────────────────

func TestHerdrBackfillRepoRoot_SkipsUnknownWorkspace(t *testing.T) {
	// Binding whose workspace herdr does not report → RepoRoot stays empty.
	//
	// MUTATION PROOF: remove the `if !known { continue }` guard →
	// repoRoot defaults to "" → the `if repoRoot == "" { continue }` guard
	// masks the first mutation. Remove BOTH guards → changed becomes non-zero
	// with an empty RepoRoot → t.Errorf ("must not touch stale binding") → RED.
	root := t.TempDir()
	ctx := context.Background()

	seedBackfillBinding(t, root, HerdrSpaceBinding{
		SpaceLabel:       "nexus3:old/stale",
		HerdrWorkspaceID: "w99",
		SandboxHandle:    "old/stale",
		SandboxID:        "sb-stale",
		// RepoRoot empty.
	})

	// herdr reports no workspaces (w99 is stale/closed).
	swapBackfillFn(t, func(_ context.Context, _ string) ([]byte, error) {
		return makeWorkspaceListJSON(map[string]string{}), nil
	})

	var out bytes.Buffer
	n, err := HerdrBackfillRepoRoot(ctx, root, "ignored-bin", &out)
	if err != nil {
		t.Fatalf("HerdrBackfillRepoRoot: %v", err)
	}
	if n != 0 {
		t.Errorf("changed count = %d; want 0 (must not touch stale binding)", n)
	}

	bindings := readAllBackfill(t, root)
	if len(bindings) != 1 {
		t.Fatalf("binding count = %d; want 1", len(bindings))
	}
	if got := bindings[0].RepoRoot; got != "" {
		t.Errorf("RepoRoot = %q; want %q (must not invent a value)", got, "")
	}
}

// ── Test 4: error from herdr → bindings untouched ────────────────────────────

func TestHerdrBackfillRepoRoot_HerdrFailure_NoPartialWrite(t *testing.T) {
	// herdr workspace list fails → HerdrBackfillRepoRoot returns a non-nil
	// error and leaves the bindings file byte-identical.
	root := t.TempDir()
	ctx := context.Background()

	seedBackfillBinding(t, root, HerdrSpaceBinding{
		SpaceLabel:       "nexus3:dev/gamma",
		HerdrWorkspaceID: "w30",
		SandboxHandle:    "dev/gamma",
		SandboxID:        "sb-gamma",
	})

	// Record the file bytes before.
	before, err := os.ReadFile(filepath.Join(root, "herdr-space-bindings.json"))
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	herdrErr := errors.New("herdr: connection refused")
	swapBackfillFn(t, func(_ context.Context, _ string) ([]byte, error) {
		return nil, herdrErr
	})

	var out bytes.Buffer
	n, backfillErr := HerdrBackfillRepoRoot(ctx, root, "ignored-bin", &out)
	if backfillErr == nil {
		t.Fatal("HerdrBackfillRepoRoot: expected non-nil error on herdr failure; got nil")
	}
	if n != 0 {
		t.Errorf("changed count = %d; want 0 on herdr failure", n)
	}

	// Bindings file must be byte-identical.
	after, err := os.ReadFile(filepath.Join(root, "herdr-space-bindings.json"))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("bindings file changed after herdr failure; before=%q after=%q", before, after)
	}
}

// ── Test 5: round-trip — after backfill predicate fires ──────────────────────

func TestHerdrBackfillRepoRoot_RoundTrip_PredicateEngages(t *testing.T) {
	// After backfill fills a binding's RepoRoot, herdrAutoCreatePredicateWith
	// returns true for a linked worktree of that repo.
	//
	// This is the end-to-end property the entire feature depends on:
	// without backfill, legacy bindings have empty RepoRoot, the predicate
	// returns false, and auto-create never fires.
	//
	// MUTATION PROOF (repo_root extraction in backfill broken):
	// RepoRoot stays empty → predicate returns false → t.Error → RED.
	//
	// MUTATION PROOF (predicate repo comparison replaced with true):
	// a linked worktree of ANY repo engages, but that is tested separately.
	// Drive this through the real predicate, not a restatement.
	dir := t.TempDir()
	root := t.TempDir()
	ctx := context.Background()

	// Build a main checkout and a linked worktree under dir.
	mainPath := filepath.Join(dir, "main")
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatal(err)
	}
	mainGit := filepath.Join(mainPath, ".git")
	if err := os.MkdirAll(mainGit, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create the worktrees/feat admin directory that a linked worktree points at.
	worktreesDir := filepath.Join(mainGit, "worktrees", "feat")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Linked worktree: .git is a FILE referencing mainGit/worktrees/feat.
	worktreePath := filepath.Join(dir, "feat")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(worktreePath, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+worktreesDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed a legacy binding with empty RepoRoot for workspace "w50".
	seedBackfillBinding(t, root, HerdrSpaceBinding{
		SpaceLabel:       "nexus3:dev/main",
		HerdrWorkspaceID: "w50",
		SandboxHandle:    "dev/main",
		SandboxID:        "sb-main",
		// RepoRoot empty — legacy binding.
	})

	// Before backfill: predicate must return false (empty RepoRoot = no match).
	bindingsBefore := readAllBackfill(t, root)
	gotBefore := herdrAutoCreatePredicateWith(worktreePath, bindingsBefore, os.Stat, os.ReadFile)
	if gotBefore {
		t.Fatal("predicate returned true BEFORE backfill with empty RepoRoot; want false (pre-condition broken)")
	}

	// Run backfill with workspace list reporting w50 → mainPath.
	swapBackfillFn(t, func(_ context.Context, _ string) ([]byte, error) {
		return makeWorkspaceListJSON(map[string]string{
			"w50": mainPath,
		}), nil
	})

	var out bytes.Buffer
	n, err := HerdrBackfillRepoRoot(ctx, root, "ignored-bin", &out)
	if err != nil {
		t.Fatalf("HerdrBackfillRepoRoot: %v", err)
	}
	if n != 1 {
		t.Errorf("changed count = %d; want 1", n)
	}

	// After backfill: predicate must return true.
	bindingsAfter := readAllBackfill(t, root)
	gotAfter := herdrAutoCreatePredicateWith(worktreePath, bindingsAfter, os.Stat, os.ReadFile)
	if !gotAfter {
		t.Error("predicate returned false AFTER backfill; want true (the end-to-end property is broken)")
	}
}

// ── Test 6: NO-SPAWN PROOF ────────────────────────────────────────────────────

func TestHerdrDefaultShellCore_UnboundNonWorktreeWorkspace_NoSpawn(t *testing.T) {
	// Unbound workspace that is NOT inside a linked worktree:
	// herdrDefaultShellCore must write zero bytes to the guest exec path
	// and must never invoke the auto-create seam (herdrDefaultShellAutoCreateFn).
	//
	// Verifies the dispatcher's existing invariant is preserved after the
	// backfill changes: the dispatcher itself must remain free of subprocess
	// or herdr calls. The auto-create seam (herdrDefaultShellAutoCreateFn)
	// is a subprocess call; it must never fire when the predicate returns false.
	root := t.TempDir()
	ctx := context.Background()

	// Seed one binding for a different workspace so the bindings file exists.
	seedBackfillBinding(t, root, HerdrSpaceBinding{
		SpaceLabel:       "nexus3:dev/other",
		HerdrWorkspaceID: "wOTHER",
		SandboxHandle:    "dev/other",
		SandboxID:        "sb-other",
		RepoRoot:         "/some/repo",
	})

	// Inject auto-create seam: if called, fail the test.
	autoCreateCalled := false
	old := herdrDefaultShellAutoCreateFn
	herdrDefaultShellAutoCreateFn = func(_ context.Context, _, _, _ string, _ io.Writer) (HerdrSpaceBinding, bool) {
		autoCreateCalled = true
		return HerdrSpaceBinding{}, false
	}
	t.Cleanup(func() { herdrDefaultShellAutoCreateFn = old })

	// Set up execFn to capture what exec would be called with.
	var execCalled bool
	var execPath string
	execFn := func(path string, _ []string, _ []string) error {
		execCalled = true
		execPath = path
		return nil
	}

	// HERDR_WORKSPACE_ID = "wUNBOUND" — not in bindings.
	// HERDR_PLUGIN_CONTEXT_JSON is empty so cwd lookup is skipped.
	getenv := func(key string) string {
		switch key {
		case "HERDR_WORKSPACE_ID":
			return "wUNBOUND"
		case "SHELL":
			return "/bin/sh"
		default:
			return ""
		}
	}

	err := herdrDefaultShellCore(ctx, getenv, root, nil, "", execFn)
	if err != nil {
		t.Errorf("herdrDefaultShellCore: %v", err)
	}

	// Must have exec'd the host shell (the predicate returned false — not a
	// linked worktree), not the guest shell.
	if !execCalled {
		t.Error("execFn not called; herdrDefaultShellCore should have exec'd the host shell")
	}
	if execPath != "/bin/sh" {
		t.Errorf("exec path = %q; want /bin/sh (host shell, not guest)", execPath)
	}

	// Auto-create seam must NEVER have been called.
	if autoCreateCalled {
		t.Error("auto-create seam was invoked for an unbound non-worktree workspace; must not be")
	}
}

// ── herdrParseWorkspaceListForBackfill unit tests ────────────────────────────

func TestHerdrParseWorkspaceListForBackfill_Valid(t *testing.T) {
	data := makeWorkspaceListJSON(map[string]string{
		"w1": "/repo/a",
		"w2": "/repo/b",
	})
	m, err := herdrParseWorkspaceListForBackfill(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("map len = %d; want 2", len(m))
	}
	if got := m["w1"]; got != "/repo/a" {
		t.Errorf("w1 = %q; want %q", got, "/repo/a")
	}
	if got := m["w2"]; got != "/repo/b" {
		t.Errorf("w2 = %q; want %q", got, "/repo/b")
	}
}

func TestHerdrParseWorkspaceListForBackfill_InvalidJSON(t *testing.T) {
	_, err := herdrParseWorkspaceListForBackfill([]byte("not json"))
	if err == nil {
		t.Error("expected error on invalid JSON; got nil")
	}
}

func TestHerdrParseWorkspaceListForBackfill_EmptyWorkspaceIDSkipped(t *testing.T) {
	data := []byte(`{"result":{"workspaces":[{"workspace_id":"","worktree":{"repo_root":"/repo"}}]}}`)
	m, err := herdrParseWorkspaceListForBackfill(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("map len = %d; want 0 (empty workspace_id must be skipped)", len(m))
	}
}
