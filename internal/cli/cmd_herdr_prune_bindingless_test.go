package cli

// Tests for the bindingless worktree-sandbox fallback sweep in
// cmd_herdr_plugin.go — the record-derived path that collects a sandbox with
// NO binding row, which the binding-indexed herdrSpacePruneFull can never see.
//
// The sweep is a fallback, so the tests that matter most are the NEGATIVE ones:
// every guard is exercised in the direction that KEEPS a sandbox, because a
// wrong reap here costs someone's working VM. Each guard below names its
// mutation target so a guard that cannot bite is visible as such.
//
// Guards under test:
//   G1 no binding row      → TestBindingless_KeepsSandboxWithBinding{ByHandle,ByID}
//   G2 worktree-managed    → TestBindingless_KeepsWhenRecordIsNotWorktreeShaped
//   G3 checkout gone       → TestBindingless_KeepsLiveWorktree
//   G4 no live workspace   → TestBindingless_KeepsWhenLiveWorkspaceRefers{ByPath,ByLabel}
//   inputs missing → KEEP  → TestBindinglessStep_Skips*

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

// worktreeFixture is an on-disk skeleton of the layout herdrWorktreeSandbox
// mounts: a main repo whose .git is a real git common dir (it has a worktrees/
// subdir) plus a linked-worktree checkout beside it.
type worktreeFixture struct {
	MainRepo string // <tmp>/main
	GitDir   string // <tmp>/main/.git          (self-mapped mount)
	Checkout string // <tmp>/wt/feature         (mounted at /workspace)
}

// newWorktreeFixture builds the skeleton. The checkout directory is created;
// removeCheckout() below is what makes it "gone".
func newWorktreeFixture(t *testing.T) worktreeFixture {
	t.Helper()
	tmp := t.TempDir()
	f := worktreeFixture{
		MainRepo: filepath.Join(tmp, "main"),
		GitDir:   filepath.Join(tmp, "main", ".git"),
		Checkout: filepath.Join(tmp, "wt", "feature"),
	}
	// The worktrees/ subdir is the structural marker herdrIsGitCommonDir keys
	// on — the same marker herdrWorktreeGitDirMount anchors its mount to.
	if err := os.MkdirAll(filepath.Join(f.GitDir, "worktrees", "feature"), 0o755); err != nil {
		t.Fatalf("mkdir git common dir: %v", err)
	}
	if err := os.MkdirAll(f.Checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	return f
}

// removeCheckout deletes the linked-worktree checkout, which is the real-world
// event (herdr worktree remove / git worktree remove) the sweep reacts to.
func (f worktreeFixture) removeCheckout(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(f.Checkout); err != nil {
		t.Fatalf("remove checkout: %v", err)
	}
}

// sandbox returns a sandbox record carrying exactly the live-mount set
// herdrWorktreeSandbox writes: <checkout>:/workspace, <gitdir>:<gitdir>, and
// the self-mapped .groundwork mount (present so the tests prove the .groundwork
// mount is not mistaken for the git common dir).
func (f worktreeFixture) sandbox(project, name string, id byte) domain.Sandbox {
	gw := filepath.Join(f.MainRepo, ".groundwork")
	return domain.Sandbox{
		ID:      domain.SandboxID{id},
		Project: project,
		Name:    name,
		LiveMounts: []domain.LiveMount{
			{HostPath: f.Checkout, GuestPath: "/workspace"},
			{HostPath: f.GitDir, GuestPath: f.GitDir},
			{HostPath: gw, GuestPath: gw},
		},
	}
}

// recordingRemover captures the handles passed to removeSandbox so a test can
// assert both that a reap happened and that one did NOT.
type recordingRemover struct {
	handles []string
	err     error
}

func (r *recordingRemover) fn() func(context.Context, string) error {
	return func(_ context.Context, handle string) error {
		r.handles = append(r.handles, handle)
		return r.err
	}
}

// liveWorkspaces is a non-empty workspace list that refers to nothing in these
// tests — the "herdr is up, and nothing points at our sandbox" baseline.
// Non-empty on purpose: an empty list is refused upstream by
// herdrSpacePruneBindinglessStep, and passing one here would silently make
// every case vacuous.
func liveWorkspaces() []herdrWorkspaceRef {
	return []herdrWorkspaceRef{
		{WorkspaceID: "wOTHER", Label: "nexus3:other/sandbox", CheckoutPath: "/some/other/checkout"},
	}
}

// ── the positive case ────────────────────────────────────────────────────────

// TestBindingless_CollectsOrphanUnderApply is the case the ticket exists for:
// a sandbox with no binding row, a worktree-shaped record, a deleted checkout,
// and no live workspace referring to it.
func TestBindingless_CollectsOrphanUnderApply(t *testing.T) {
	ctx := context.Background()
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	rec := &recordingRemover{}
	var buf bytes.Buffer
	_, n := herdrSpaceSweepBindinglessSandboxes(ctx, &buf, nil,
		[]domain.Sandbox{sb}, liveWorkspaces(), rec.fn(), true)

	if n != 1 {
		t.Fatalf("reaped count = %d, want 1; output=%q", n, buf.String())
	}
	if len(rec.handles) != 1 || rec.handles[0] != "repo/feature" {
		t.Fatalf("removeSandbox handles = %v, want [repo/feature]", rec.handles)
	}
	if !strings.Contains(buf.String(), "BINDINGLESS reaped sandbox=repo/feature") {
		t.Errorf("output does not report the reap; got %q", buf.String())
	}
}

// TestBindingless_DryRunIsReadOnly: without --apply the sweep reports the
// orphan and touches nothing — no removeSandbox call, no filesystem change.
//
// MUTATION TARGET: the `if !apply` early-continue in
// herdrSpaceSweepBindinglessSandboxes. Deleting it makes the dry run reap → RED.
func TestBindingless_DryRunIsReadOnly(t *testing.T) {
	ctx := context.Background()
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	rec := &recordingRemover{}
	var buf bytes.Buffer
	_, n := herdrSpaceSweepBindinglessSandboxes(ctx, &buf, nil,
		[]domain.Sandbox{sb}, liveWorkspaces(), rec.fn(), false)

	if n != 0 {
		t.Errorf("dry run reaped %d sandbox(es), want 0", n)
	}
	if len(rec.handles) != 0 {
		t.Errorf("dry run called removeSandbox with %v, want no calls", rec.handles)
	}
	if !strings.Contains(buf.String(), "BINDINGLESS sandbox=repo/feature") ||
		!strings.Contains(buf.String(), "would be reaped") {
		t.Errorf("dry run does not report the orphan; got %q", buf.String())
	}
	// Read-only means read-only: the git common dir the sweep stat()s is intact.
	if fi, err := os.Stat(filepath.Join(f.GitDir, "worktrees")); err != nil || !fi.IsDir() {
		t.Errorf("dry run disturbed the git common dir: err=%v", err)
	}
}

// ── G1: a sandbox with a binding belongs to the existing path ────────────────

// TestBindingless_KeepsSandboxWithBindingByHandle: the binding-indexed path
// owns any sandbox that has a binding row. The fallback must not widen it.
//
// MUTATION TARGET: the `b.SandboxHandle == v.Handle` comparison in G1 of
// herdrClassifyBindinglessSandbox. Breaking it collects a bound sandbox → RED.
func TestBindingless_KeepsSandboxWithBindingByHandle(t *testing.T) {
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	bindings := []HerdrSpaceBinding{{
		SpaceLabel:       "nexus3:repo/feature",
		HerdrWorkspaceID: "wBOUND",
		SandboxHandle:    "repo/feature",
		SandboxID:        "sb-unrelated-id",
		WorktreeManaged:  true,
	}}
	v := herdrClassifyBindinglessSandbox(sb, bindings, liveWorkspaces())
	if v.Collect {
		t.Fatalf("sandbox with a binding row must be KEPT by the fallback; reason=%q", v.Reason)
	}
	if !strings.Contains(v.Reason, "binding") {
		t.Errorf("reason should name the binding; got %q", v.Reason)
	}
}

// TestBindingless_KeepsSandboxWithBindingByID: the binding may name the sandbox
// by ID even when its handle was rewritten. Still owned by the existing path.
//
// MUTATION TARGET: the `b.SandboxID == sbID` clause in G1.
func TestBindingless_KeepsSandboxWithBindingByID(t *testing.T) {
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	bindings := []HerdrSpaceBinding{{
		SpaceLabel:       "nexus3:repo/renamed",
		HerdrWorkspaceID: "wBOUND",
		SandboxHandle:    "repo/renamed", // handle does NOT match
		SandboxID:        sb.ID.String(), // ID does
		WorktreeManaged:  true,
	}}
	if v := herdrClassifyBindinglessSandbox(sb, bindings, liveWorkspaces()); v.Collect {
		t.Fatalf("sandbox named by binding SandboxID must be KEPT; reason=%q", v.Reason)
	}
}

// TestBindingless_EmptyBindingSandboxIDIsNotAWildcard: a legacy binding with an
// empty SandboxID must not match a sandbox whose ID string is also compared —
// otherwise one malformed binding would shield (or, mutated, expose) everything.
func TestBindingless_EmptyBindingSandboxIDIsNotAWildcard(t *testing.T) {
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	bindings := []HerdrSpaceBinding{{
		SpaceLabel:    "nexus3:repo/unrelated",
		SandboxHandle: "repo/unrelated",
		SandboxID:     "", // legacy row
	}}
	if v := herdrClassifyBindinglessSandbox(sb, bindings, liveWorkspaces()); !v.Collect {
		t.Fatalf("unrelated legacy binding must not shield this sandbox; reason=%q", v.Reason)
	}
}

// ── G2: worktree-managedness must be positively established ──────────────────

// TestBindingless_KeepsWhenRecordIsNotWorktreeShaped walks every way the
// live-mount signature can fail to identify a worktree sandbox. Each case must
// KEEP.
//
// MUTATION TARGETS: the `len(checkouts) != 1 || len(commons) != 1` refusal and
// the main-checkout refusal in herdrWorktreeMountShapeOf. Relaxing either to
// `< 1` / deleting it collects one of these cases → RED.
func TestBindingless_KeepsWhenRecordIsNotWorktreeShaped(t *testing.T) {
	f := newWorktreeFixture(t)
	f.removeCheckout(t)
	gw := filepath.Join(f.MainRepo, ".groundwork")
	missingGitDir := filepath.Join(f.MainRepo, "not-a-git-dir")

	cases := []struct {
		name   string
		mounts []domain.LiveMount
	}{
		{
			name:   "no mounts at all",
			mounts: nil,
		},
		{
			name: "workspace mount but no git common dir mount",
			mounts: []domain.LiveMount{
				{HostPath: f.Checkout, GuestPath: "/workspace"},
			},
		},
		{
			name: "self-mapped mount that is not a git common dir",
			mounts: []domain.LiveMount{
				{HostPath: f.Checkout, GuestPath: "/workspace"},
				// .groundwork is self-mapped but has no worktrees/ subdir.
				{HostPath: gw, GuestPath: gw},
			},
		},
		{
			name: "git dir mount whose common dir no longer exists on disk",
			mounts: []domain.LiveMount{
				{HostPath: f.Checkout, GuestPath: "/workspace"},
				{HostPath: missingGitDir, GuestPath: missingGitDir},
			},
		},
		{
			name: "git common dir present but no /workspace mount",
			mounts: []domain.LiveMount{
				{HostPath: f.GitDir, GuestPath: f.GitDir},
			},
		},
		{
			name: "two /workspace mounts — record not understood",
			mounts: []domain.LiveMount{
				{HostPath: f.Checkout, GuestPath: "/workspace"},
				{HostPath: filepath.Join(f.MainRepo, "other"), GuestPath: "/workspace"},
				{HostPath: f.GitDir, GuestPath: f.GitDir},
			},
		},
		{
			name: "relative host path is never usable evidence",
			mounts: []domain.LiveMount{
				{HostPath: "relative/checkout", GuestPath: "/workspace"},
				{HostPath: f.GitDir, GuestPath: f.GitDir},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := domain.Sandbox{
				ID:         domain.SandboxID{9},
				Project:    "repo",
				Name:       "feature",
				LiveMounts: tc.mounts,
			}
			if v := herdrClassifyBindinglessSandbox(sb, nil, liveWorkspaces()); v.Collect {
				t.Fatalf("record must NOT be classified as a collectable worktree sandbox; reason=%q", v.Reason)
			}
		})
	}
}

// TestWorktreeMountShapeOf_RefusesMainCheckout pins the main-checkout refusal
// against herdrWorktreeMountShapeOf DIRECTLY, not through the classifier.
//
// It has to be tested here: the classifier's G3 ("checkout positively gone")
// masks this case entirely, because a main repo root whose .git still exists
// necessarily still exists itself, so G3 keeps the sandbox before the refusal
// can be observed. Routing this assertion through the classifier would produce
// a test that passes with the refusal deleted — which is exactly the shape of
// non-biting guard this suite exists to rule out.
//
// MUTATION TARGET: the `shape.CheckoutPath == filepath.Dir(shape.CommonGitDir)`
// refusal in herdrWorktreeMountShapeOf.
func TestWorktreeMountShapeOf_RefusesMainCheckout(t *testing.T) {
	f := newWorktreeFixture(t)

	// Sanity: the same shape with a LINKED checkout is accepted, so the refusal
	// below is attributable to the main-checkout path and not to the fixture.
	linked := domain.Sandbox{LiveMounts: []domain.LiveMount{
		{HostPath: f.Checkout, GuestPath: "/workspace"},
		{HostPath: f.GitDir, GuestPath: f.GitDir},
	}}
	if _, ok := herdrWorktreeMountShapeOf(linked); !ok {
		t.Fatalf("linked-worktree mount set should be recognised")
	}

	// MainRepo is filepath.Dir(GitDir) — a main checkout, never reapable.
	main := domain.Sandbox{LiveMounts: []domain.LiveMount{
		{HostPath: f.MainRepo, GuestPath: "/workspace"},
		{HostPath: f.GitDir, GuestPath: f.GitDir},
	}}
	if shape, ok := herdrWorktreeMountShapeOf(main); ok {
		t.Fatalf("main-checkout mount set must be refused; got shape=%+v", shape)
	}
}

// TestBindingless_KeepsSandboxWithNoHandle: a record with no project or name
// has nothing to pass to removeSandbox. Refuse rather than guess.
func TestBindingless_KeepsSandboxWithNoHandle(t *testing.T) {
	f := newWorktreeFixture(t)
	sb := f.sandbox("", "", 1)
	f.removeCheckout(t)
	if v := herdrClassifyBindinglessSandbox(sb, nil, liveWorkspaces()); v.Collect {
		t.Fatalf("handleless record must be KEPT; reason=%q", v.Reason)
	}
}

// ── G3: the checkout must be positively gone ─────────────────────────────────

// TestBindingless_KeepsLiveWorktree is the guard that protects a developer who
// is using the worktree right now. Everything else about the record says
// "collectable"; only the live checkout stops it.
//
// MUTATION TARGET: the `!herdrPathPositivelyGone(shape.CheckoutPath)` guard in
// herdrClassifyBindinglessSandbox. Deleting it reaps a live worktree → RED.
func TestBindingless_KeepsLiveWorktree(t *testing.T) {
	ctx := context.Background()
	f := newWorktreeFixture(t) // checkout deliberately NOT removed
	sb := f.sandbox("repo", "feature", 1)

	v := herdrClassifyBindinglessSandbox(sb, nil, liveWorkspaces())
	if v.Collect {
		t.Fatalf("a sandbox whose worktree still exists must be KEPT; reason=%q", v.Reason)
	}

	// And the same through the sweep, under --apply, since that is where the
	// damage would actually happen.
	rec := &recordingRemover{}
	var buf bytes.Buffer
	if _, n := herdrSpaceSweepBindinglessSandboxes(ctx, &buf, nil,
		[]domain.Sandbox{sb}, liveWorkspaces(), rec.fn(), true); n != 0 {
		t.Fatalf("apply reaped %d live-worktree sandbox(es), want 0; output=%q", n, buf.String())
	}
	if len(rec.handles) != 0 {
		t.Fatalf("removeSandbox called for a live worktree: %v", rec.handles)
	}
}

// TestPathPositivelyGone covers the "gone" predicate directly, including the
// two ways a path can look absent without being absent.
//
// MUTATION TARGET: os.Lstat → os.Stat in herdrPathPositivelyGone. A dangling
// symlink stats as ErrNotExist but the path itself exists, so the mutation
// turns "dangling symlink" GREEN→RED here.
func TestPathPositivelyGone(t *testing.T) {
	tmp := t.TempDir()
	present := filepath.Join(tmp, "present")
	if err := os.MkdirAll(present, 0o755); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(tmp, "dangling")
	if err := os.Symlink(filepath.Join(tmp, "nowhere"), dangling); err != nil {
		t.Fatal(err)
	}

	// An unreadable PARENT makes Lstat fail with EACCES rather than ENOENT.
	// This is the only case that separates "positively absent" from "Lstat
	// returned some error", and it is the whole point of the guard: an
	// unreadable path is AMBIGUOUS and must resolve to KEEP, never to a reap.
	// Without this case `errors.Is(err, os.ErrNotExist)` and a bare
	// `err != nil` are indistinguishable, and the mutation that swaps them
	// survives — verified on 2026-09-02.
	var unreadable string
	if os.Geteuid() != 0 { // root ignores the mode bits, so the case cannot be built
		locked := filepath.Join(tmp, "locked")
		if err := os.MkdirAll(filepath.Join(locked, "child"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		unreadable = filepath.Join(locked, "child")
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"absent path is positively gone", filepath.Join(tmp, "absent"), true},
		{"present directory is not gone", present, false},
		{"dangling symlink still exists", dangling, false},
		{"empty path is never gone", "", false},
	}
	if unreadable != "" {
		cases = append(cases, struct {
			name string
			path string
			want bool
		}{"unreadable parent is ambiguous, not gone", unreadable, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := herdrPathPositivelyGone(tc.path); got != tc.want {
				t.Fatalf("herdrPathPositivelyGone(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// ── G4: no live herdr workspace may refer to the sandbox ─────────────────────

// TestBindingless_KeepsWhenLiveWorkspaceRefersByPath: herdr still has a
// workspace open on that checkout path. Whatever the filesystem says, something
// live points at this sandbox.
//
// MUTATION TARGET: the CheckoutPath comparison in G4.
func TestBindingless_KeepsWhenLiveWorkspaceRefersByPath(t *testing.T) {
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	ws := []herdrWorkspaceRef{
		{WorkspaceID: "wOTHER", Label: "nexus3:other/sandbox"},
		// Deliberately unclean so the comparison's filepath.Clean is exercised.
		{WorkspaceID: "wLIVE", Label: "some-label", CheckoutPath: f.Checkout + "/."},
	}
	v := herdrClassifyBindinglessSandbox(sb, nil, ws)
	if v.Collect {
		t.Fatalf("a checkout a live workspace still opens must be KEPT; reason=%q", v.Reason)
	}
	if !strings.Contains(v.Reason, "wLIVE") {
		t.Errorf("reason should name the workspace; got %q", v.Reason)
	}
}

// TestBindingless_KeepsWhenLiveWorkspaceRefersByLabel: herdr's worktree info is
// nullable, so a live workspace may carry no checkout path at all. The
// "nexus3:<handle>" label herdrWorktreeSandbox renames it to is the second way
// a live workspace claims a sandbox.
//
// MUTATION TARGET: the label comparison in G4.
func TestBindingless_KeepsWhenLiveWorkspaceRefersByLabel(t *testing.T) {
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	ws := []herdrWorkspaceRef{
		{WorkspaceID: "wOTHER", Label: "nexus3:other/sandbox"},
		{WorkspaceID: "wLABEL", Label: "nexus3:repo/feature"}, // no CheckoutPath
	}
	v := herdrClassifyBindinglessSandbox(sb, nil, ws)
	if v.Collect {
		t.Fatalf("a sandbox a live workspace is labelled for must be KEPT; reason=%q", v.Reason)
	}
	if !strings.Contains(v.Reason, "wLABEL") {
		t.Errorf("reason should name the workspace; got %q", v.Reason)
	}
}

// TestBindingless_EmptyWorkspaceFieldsAreNotWildcards: a workspace row with a
// blank label and blank checkout path must not match a sandbox. Otherwise one
// degenerate row would shield everything — and, mutated the other way, a
// sandbox with a blank handle would match every row.
func TestBindingless_EmptyWorkspaceFieldsAreNotWildcards(t *testing.T) {
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	ws := []herdrWorkspaceRef{{WorkspaceID: "wBLANK", Label: "", CheckoutPath: ""}}
	if v := herdrClassifyBindinglessSandbox(sb, nil, ws); !v.Collect {
		t.Fatalf("a blank workspace row must not shield the sandbox; reason=%q", v.Reason)
	}
}

// ── input availability: a missing input is never evidence ────────────────────

// fakeLister implements herdrSpacePruneLister for the step tests.
type fakeLister struct {
	sbs []domain.Sandbox
	err error
}

func (f fakeLister) List(_ context.Context) ([]domain.Sandbox, error) { return f.sbs, f.err }

// withFakeHerdrOutput points herdrExecCommandContext at `cat` fed with payload.
// Named distinctly from withFakeHerdr in cmd_herdr_space_prune_test.go so the
// two files stay independently readable.
func withFakeHerdrOutput(t *testing.T, payload string) {
	t.Helper()
	orig := herdrExecCommandContext
	t.Cleanup(func() { herdrExecCommandContext = orig })
	herdrExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "cat")
		cmd.Stdin = strings.NewReader(payload)
		return cmd
	}
}

// withFailingHerdr makes every herdr invocation exit non-zero.
func withFailingHerdr(t *testing.T) {
	t.Helper()
	orig := herdrExecCommandContext
	t.Cleanup(func() { herdrExecCommandContext = orig })
	herdrExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
}

// TestBindinglessStep_SkipsWhenInputsUnavailable: every way an input can be
// missing must skip the sweep entirely rather than let the absence clear a
// guard. This is the difference between "nothing refers to this sandbox" and
// "I could not find out".
//
// MUTATION TARGETS: each early `return` in herdrSpacePruneBindinglessStep,
// and the `len(workspaces) == 0` refusal in particular — deleting that one
// makes an unreachable-but-exit-zero herdr clear G4 for every sandbox at once.
func TestBindinglessStep_SkipsWhenInputsUnavailable(t *testing.T) {
	ctx := context.Background()
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	const oneWorkspace = `{"result":{"workspaces":[{"workspace_id":"wOTHER","label":"other"}]}}`

	cases := []struct {
		name     string
		herdrBin string
		setup    func(t *testing.T)
		lister   fakeLister
		wantSkip string
	}{
		{
			name:     "no herdr binary",
			herdrBin: "",
			setup:    func(t *testing.T) { withFakeHerdrOutput(t, oneWorkspace) },
			lister:   fakeLister{sbs: []domain.Sandbox{sb}},
			wantSkip: "could not be listed",
		},
		{
			name:     "herdr workspace list fails",
			herdrBin: "herdr",
			setup:    withFailingHerdr,
			lister:   fakeLister{sbs: []domain.Sandbox{sb}},
			wantSkip: "could not be listed",
		},
		{
			name:     "herdr response does not parse",
			herdrBin: "herdr",
			setup:    func(t *testing.T) { withFakeHerdrOutput(t, "not json") },
			lister:   fakeLister{sbs: []domain.Sandbox{sb}},
			wantSkip: "could not be listed",
		},
		{
			name:     "herdr reports zero workspaces",
			herdrBin: "herdr",
			setup:    func(t *testing.T) { withFakeHerdrOutput(t, `{"result":{"workspaces":[]}}`) },
			lister:   fakeLister{sbs: []domain.Sandbox{sb}},
			wantSkip: "no workspaces",
		},
		{
			name:     "sandbox list unavailable",
			herdrBin: "herdr",
			setup:    func(t *testing.T) { withFakeHerdrOutput(t, oneWorkspace) },
			lister:   fakeLister{err: errors.New("store unreadable")},
			wantSkip: "sandbox list unavailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			rec := &recordingRemover{}
			var buf bytes.Buffer
			herdrSpacePruneBindinglessStep(ctx, &buf, tc.herdrBin, tc.lister, nil, rec.fn(), true)

			if len(rec.handles) != 0 {
				t.Fatalf("sweep reaped %v despite a missing input", rec.handles)
			}
			if !strings.Contains(buf.String(), "Bindingless sweep skipped") ||
				!strings.Contains(buf.String(), tc.wantSkip) {
				t.Fatalf("output should report the skip (%q); got %q", tc.wantSkip, buf.String())
			}
		})
	}
}

// TestBindinglessStep_CollectsWhenAllInputsPresent is the counterpart: with a
// reachable herdr and a readable sandbox list, the same orphan IS collected.
// Without this, every skip case above would pass against a sweep that never
// collects anything.
func TestBindinglessStep_CollectsWhenAllInputsPresent(t *testing.T) {
	ctx := context.Background()
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	withFakeHerdrOutput(t, `{"result":{"workspaces":[{"workspace_id":"wOTHER","label":"other"}]}}`)
	rec := &recordingRemover{}
	var buf bytes.Buffer
	herdrSpacePruneBindinglessStep(ctx, &buf, "herdr", fakeLister{sbs: []domain.Sandbox{sb}},
		nil, rec.fn(), true)

	if len(rec.handles) != 1 || rec.handles[0] != "repo/feature" {
		t.Fatalf("removeSandbox handles = %v, want [repo/feature]; output=%q", rec.handles, buf.String())
	}
	if !strings.Contains(buf.String(), "1 sandbox(es) examined, 1 collectable, 1 reaped") {
		t.Errorf("output missing summary; got %q", buf.String())
	}
}

// TestBindinglessStep_ReportsEvenWhenItFindsNothing: the sweep must announce
// that it looked, including the all-clear. A sweep that prints nothing when it
// finds nothing is indistinguishable from a sweep that never ran — which is
// precisely the defect class this path exists to close.
func TestBindinglessStep_ReportsEvenWhenItFindsNothing(t *testing.T) {
	ctx := context.Background()
	f := newWorktreeFixture(t) // checkout left in place → nothing collectable
	sb := f.sandbox("repo", "feature", 1)

	withFakeHerdrOutput(t, `{"result":{"workspaces":[{"workspace_id":"wOTHER","label":"other"}]}}`)
	rec := &recordingRemover{}
	var buf bytes.Buffer
	herdrSpacePruneBindinglessStep(ctx, &buf, "herdr", fakeLister{sbs: []domain.Sandbox{sb}},
		nil, rec.fn(), false)

	if len(rec.handles) != 0 {
		t.Fatalf("dry run reaped %v", rec.handles)
	}
	if !strings.Contains(buf.String(), "1 sandbox(es) examined, 0 collectable") {
		t.Fatalf("sweep did not report that it ran; got %q", buf.String())
	}
}

// TestBindingless_ReapFailureIsNotCounted: a removeSandbox error leaves the
// sandbox for the next run and must not be reported as reclaimed.
func TestBindingless_ReapFailureIsNotCounted(t *testing.T) {
	ctx := context.Background()
	f := newWorktreeFixture(t)
	sb := f.sandbox("repo", "feature", 1)
	f.removeCheckout(t)

	rec := &recordingRemover{err: errors.New("vm still shutting down")}
	var buf bytes.Buffer
	_, n := herdrSpaceSweepBindinglessSandboxes(ctx, &buf, nil,
		[]domain.Sandbox{sb}, liveWorkspaces(), rec.fn(), true)

	if n != 0 {
		t.Fatalf("failed reap counted as %d, want 0", n)
	}
	if !strings.Contains(buf.String(), "BINDINGLESS reap FAILED") {
		t.Errorf("failure not reported; got %q", buf.String())
	}
}

// ── the herdr response parse ─────────────────────────────────────────────────

// TestParseWorkspaceRefs pins the response shape against herdr 0.8.0's
// published schema: WorkspaceInfo carries workspace_id, label, and a NULLABLE
// worktree object whose checkout_path is the linked-worktree path.
// (Verified from `herdr api schema --json`, schemas.success_response.$defs.)
func TestParseWorkspaceRefs(t *testing.T) {
	payload := `{"result":{"workspaces":[
	  {"workspace_id":"w1","label":"nexus3:repo/feature","worktree":{"repo_key":"/main/.git","repo_name":"main","repo_root":"/main","checkout_path":"/wt/feature","is_linked_worktree":true}},
	  {"workspace_id":"w2","label":"plain","worktree":null}
	]}}`
	refs, err := herdrParseWorkspaceRefs([]byte(payload))
	if err != nil {
		t.Fatalf("herdrParseWorkspaceRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2", len(refs))
	}
	if refs[0].WorkspaceID != "w1" || refs[0].Label != "nexus3:repo/feature" || refs[0].CheckoutPath != "/wt/feature" {
		t.Errorf("worktree workspace parsed wrong: %+v", refs[0])
	}
	if refs[1].WorkspaceID != "w2" || refs[1].CheckoutPath != "" {
		t.Errorf("null worktree must yield an empty checkout path: %+v", refs[1])
	}
	if _, err := herdrParseWorkspaceRefs([]byte("{")); err == nil {
		t.Error("malformed response must be an error, not an empty success")
	}
}
