package cli

// Tests for herdrRepoHasBoundSandbox and herdrWorktreeSandboxRepoCheck.
//
// These tests pin the SINGLE MECHANISM invariant: both the dispatcher
// (herdrAutoCreatePredicateWith) and the subprocess (herdrWorktreeSandboxRepoCheck)
// must agree on "does this repo have a nexus3-bound sandbox?" for every input.
// Enforced by TestHerdrRepoHasBoundSandbox_BothCallSitesAgree.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestHerdrWorktreeSandboxRepoCheck_RepoRootMatch_Binds is the exact live
// scenario that failed before the fix:
//
//	AllWorkspaceIDs : w8, w3N, w3K, w3M, w3H, w3J  (workspaces opening worktrees)
//	bound workspaces: wN, w36, w37, w33, w38, w34  (nexus3-created)
//
// Zero overlap → old workspace-ID iteration returned false even though the
// repo had bound sandboxes. The new code uses RepoRoot matching and returns
// true whenever a binding's RepoRoot equals the worktree's main repo.
//
// MUTATION PROOF (herdrRepoHasBoundSandbox forced false):  this test → RED.
// MUTATION PROOF (herdrRepoHasBoundSandbox forced true):   no-match test → RED.
func TestHerdrWorktreeSandboxRepoCheck_RepoRootMatch_Binds(t *testing.T) {
	storeRoot := t.TempDir()
	mainRepo := t.TempDir()

	// A binding whose workspace ID is NOT among the workspace IDs that open
	// worktrees — the historically disjoint set.
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:dev/space1",
		HerdrWorkspaceID: "wN", // nexus3-created; never appears in open_workspace_id
		SandboxHandle:    "dev/space1",
		SandboxID:        "sb-space1",
		RepoRoot:         mainRepo,
	}
	makeBindings(t, storeRoot, []HerdrSpaceBinding{binding})

	info := herdrWorktreeInfo{
		Branch:           "feat/fix",
		Path:             filepath.Join(mainRepo, "..", "worktree"),
		IsLinkedWorktree: true,
		// RepoKey is the main repo's .git dir (source.repo_key from herdr).
		// Parent dir == mainRepo → the fix derives and matches this.
		RepoKey: filepath.Join(mainRepo, ".git"),
	}

	got := herdrWorktreeSandboxRepoCheck(context.Background(), storeRoot, info)
	if !got {
		t.Error("herdrWorktreeSandboxRepoCheck returned false for a binding whose RepoRoot matches the worktree repo — this is the live failure scenario; want true")
	}
}

// TestHerdrWorktreeSandboxRepoCheck_NoRepoRootMatch_Skips verifies that when
// no binding's RepoRoot matches the worktree's main repo, the check returns
// false (workspace stays a host shell).
func TestHerdrWorktreeSandboxRepoCheck_NoRepoRootMatch_Skips(t *testing.T) {
	storeRoot := t.TempDir()
	otherRepo := t.TempDir()
	worktreeMainRepo := t.TempDir()

	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:other/proj",
		HerdrWorkspaceID: "w36",
		SandboxHandle:    "other/proj",
		SandboxID:        "sb-other",
		RepoRoot:         otherRepo, // a different repo entirely
	}
	makeBindings(t, storeRoot, []HerdrSpaceBinding{binding})

	info := herdrWorktreeInfo{
		Branch:           "feat/fix",
		Path:             filepath.Join(worktreeMainRepo, "..", "worktree"),
		IsLinkedWorktree: true,
		RepoKey:          filepath.Join(worktreeMainRepo, ".git"),
	}

	got := herdrWorktreeSandboxRepoCheck(context.Background(), storeRoot, info)
	if got {
		t.Error("herdrWorktreeSandboxRepoCheck returned true for a binding whose RepoRoot does NOT match the worktree repo; want false")
	}
}

// TestHerdrRepoHasBoundSandbox_EmptyRepoRoot_IsNoMatch verifies that a
// binding with an empty RepoRoot (pre-dates repo tracking) is never treated
// as a wildcard match regardless of mainRepo.
//
// MUTATION PROOF: remove the `if b.RepoRoot == ""` guard in
// herdrRepoHasBoundSandbox → filepath.Clean("") == "." and
// filepath.Clean(".") == "." → herdrRepoHasBoundSandbox(".", legacy) = true
// → the mainRepo="." sub-case below fires RED.
// (Absolute mainRepo paths like "/home/user/repo" do not collide because
// filepath.Clean("") != "/home/user/repo"; the "." case is the decisive one.)
func TestHerdrRepoHasBoundSandbox_EmptyRepoRoot_IsNoMatch(t *testing.T) {
	legacy := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:legacy",
		HerdrWorkspaceID: "wLegacy",
		SandboxHandle:    "legacy",
		SandboxID:        "sb-legacy",
		RepoRoot:         "", // no repo tracked
	}

	for _, mainRepo := range []string{"/home/user/repo", "/some/other/path", t.TempDir()} {
		if herdrRepoHasBoundSandbox(mainRepo, []HerdrSpaceBinding{legacy}) {
			t.Errorf("herdrRepoHasBoundSandbox(mainRepo=%q, legacy binding) = true; empty RepoRoot must never match", mainRepo)
		}
	}

	// mainRepo="." is the decisive mutation-proof case: filepath.Clean("") == "."
	// and filepath.Clean(".") == "." → without the guard the call returns true.
	// Relative mainRepo paths arise when herdr returns a relative repo_key.
	if herdrRepoHasBoundSandbox(".", []HerdrSpaceBinding{legacy}) {
		t.Error("herdrRepoHasBoundSandbox(mainRepo=\".\", legacy binding) = true; empty RepoRoot must never match even for relative mainRepo")
	}

	// Empty mainRepo is also NO MATCH even against a real binding.
	real := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:real",
		HerdrWorkspaceID: "wReal",
		SandboxHandle:    "real",
		SandboxID:        "sb-real",
		RepoRoot:         t.TempDir(),
	}
	if herdrRepoHasBoundSandbox("", []HerdrSpaceBinding{real}) {
		t.Error("herdrRepoHasBoundSandbox(mainRepo=\"\") = true; empty mainRepo must never match")
	}
}

// TestHerdrRepoHasBoundSandbox_BothCallSitesAgree drives both the dispatcher
// (herdrAutoCreatePredicateWith) and the subprocess (herdrWorktreeSandboxRepoCheck)
// against the SAME binding set and repo, asserting they reach the SAME decision
// for both a matching and a non-matching case.
//
// This is the enforcement test for the "single mechanism" invariant declared in
// herdrRepoHasBoundSandbox. To make it fail, replace one call site with a
// different rule (e.g. always-true) and the assertion fires RED.
//
// MUTATION PROOF (make dispatcher always return true):
//
//	replace herdrRepoHasBoundSandbox call in herdrAutoCreatePredicateWith with
//	`return true` → non-matching sub-test: dispatcher=true, subprocess=false → RED.
//
// MUTATION PROOF (make subprocess always return true):
//
//	replace herdrRepoHasBoundSandbox call in herdrWorktreeSandboxRepoCheck with
//	`return true` → non-matching sub-test: dispatcher=false, subprocess=true → RED.
func TestHerdrRepoHasBoundSandbox_BothCallSitesAgree(t *testing.T) {
	dir := t.TempDir()
	// makeLinkedWorktreeFixture creates:
	//   dir/main/.git/           (directory — main repo)
	//   dir/worktree/.git        (file pointing at dir/main/.git/worktrees/feat)
	// binding.RepoRoot == dir/main
	worktreePath, binding := makeLinkedWorktreeFixture(t, dir)
	mainRepo := binding.RepoRoot

	differentBinding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:unrelated",
		HerdrWorkspaceID: "wUnrelated",
		SandboxHandle:    "unrelated",
		SandboxID:        "sb-unrelated",
		RepoRoot:         "/entirely/different/repo",
	}

	cases := []struct {
		name      string
		bindings  []HerdrSpaceBinding
		wantMatch bool
	}{
		{"matching RepoRoot", []HerdrSpaceBinding{binding}, true},
		{"non-matching RepoRoot", []HerdrSpaceBinding{differentBinding}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Dispatcher path.
			dispatcherGot := herdrAutoCreatePredicateWith(worktreePath, tc.bindings, os.Stat, os.ReadFile)

			// Subprocess path: write bindings to a temp storeRoot.
			storeRoot := t.TempDir()
			makeBindings(t, storeRoot, tc.bindings)
			info := herdrWorktreeInfo{
				Branch:           "feat",
				Path:             worktreePath,
				IsLinkedWorktree: true,
				RepoKey:          filepath.Join(mainRepo, ".git"),
			}
			subprocessGot := herdrWorktreeSandboxRepoCheck(context.Background(), storeRoot, info)

			if dispatcherGot != subprocessGot {
				t.Errorf("call-site disagreement: dispatcher=%v subprocess=%v (want %v, mainRepo=%q) — both must use herdrRepoHasBoundSandbox",
					dispatcherGot, subprocessGot, tc.wantMatch, mainRepo)
			}
			if dispatcherGot != tc.wantMatch {
				t.Errorf("got %v; want %v", dispatcherGot, tc.wantMatch)
			}
		})
	}
}
