package cli

// Tests for herdrAgentEnsureSandboxExists and the colon-label safety rule.
//
// Cases covered:
//  1. Sandbox already exists → create not called (no-op)
//  2. Sandbox absent → create called (MUTATION-PROOF: remove create call, this goes RED)
//  3. Create error propagated correctly
//  4. herdrSpaceLabelForRef always uses "nexus3:" prefix (colon load-bearing)
//  5. Label "nexus3" (no colon — the operator's w8 workspace) never matches a
//     colon-qualified lookup (HerdrSpaceGetByLabel with "nexus3:…")
//  6. A second ensure call with a now-existing sandbox does NOT call create again
//     (idempotency: sequential calls are safe)
//  7. Transient get error is propagated; create is NOT called (only store.ErrNotFound
//     may trigger create — MUTATION-PROOF: remove the ErrNotFound guard, this goes RED)
//  8. herdrPluginSpaceAgent wires to herdrEnsureFn (MUTATION-PROOF: delete the
//     ensure call block in herdrPluginSpaceAgent, this goes RED)
//  9. space-create --no-focus flag is consumed without a UsageError
//
// All seams are injected — no real herdr binary, sandbox service, or store is
// touched except HerdrSpacePut / HerdrSpaceGetByLabel (which use t.TempDir()).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── helper ─────────────────────────────────────────────────────────────────

func nopGet(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{LiveMounts: []domain.LiveMount{{GuestPath: "/work"}}}, nil
}

// absentGet mimics what svc.Get ACTUALLY returns for a missing sandbox:
// service.resolve wraps the store's sentinel as `resolve %q: %w`. A fake that
// returned a bare errors.New("not found") would pass against a classifier that
// treats every error as absence — which is the bug this fake must be able to
// catch, so it has to carry the real sentinel.
func absentGet(_ context.Context, ref string) (domain.Sandbox, error) {
	return domain.Sandbox{}, fmt.Errorf("resolve %q: %w", ref, store.ErrNotFound)
}

// ── TestHerdrEnsureSandbox_NilWhenExists ───────────────────────────────────

// TestHerdrEnsureSandbox_NilWhenExists: if get succeeds, create is never called.
func TestHerdrEnsureSandbox_NilWhenExists(t *testing.T) {
	createCalled := false
	create := func(_ context.Context, _ string, _ io.Writer) error {
		createCalled = true
		return nil
	}
	var buf bytes.Buffer
	if err := herdrAgentEnsureSandboxExists(context.Background(), "proj/box", &buf, nopGet, create); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createCalled {
		t.Error("create must not be called when sandbox already exists")
	}
}

// ── TestHerdrEnsureSandbox_CallsCreateWhenAbsent ───────────────────────────

// TestHerdrEnsureSandbox_CallsCreateWhenAbsent: if get fails, create is called.
//
// MUTATION PROOF: remove the `return create(ctx, ref, w)` call from
// herdrAgentEnsureSandboxExists and this test goes RED:
//
//	cmd_herdr_ensure_sandbox_test.go: create was not called for absent sandbox
func TestHerdrEnsureSandbox_CallsCreateWhenAbsent(t *testing.T) {
	createCalled := false
	var calledRef string
	create := func(_ context.Context, r string, _ io.Writer) error {
		createCalled = true
		calledRef = r
		return nil
	}
	var buf bytes.Buffer
	const ref = "myproj/mybox"
	if err := herdrAgentEnsureSandboxExists(context.Background(), ref, &buf, absentGet, create); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !createCalled {
		t.Error("create was not called for absent sandbox")
	}
	if calledRef != ref {
		t.Errorf("create called with ref %q, want %q", calledRef, ref)
	}
	// The log message must name the ref so the operator knows what was created.
	if !strings.Contains(buf.String(), ref) {
		t.Errorf("output %q does not contain ref %q", buf.String(), ref)
	}
}

// ── TestHerdrEnsureSandbox_PropagatesCreateError ───────────────────────────

// TestHerdrEnsureSandbox_PropagatesCreateError: errors from create reach the caller.
func TestHerdrEnsureSandbox_PropagatesCreateError(t *testing.T) {
	sentinel := errors.New("nexus3.yaml: no image specified")
	create := func(_ context.Context, _ string, _ io.Writer) error { return sentinel }
	var buf bytes.Buffer
	err := herdrAgentEnsureSandboxExists(context.Background(), "p/b", &buf, absentGet, create)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// ── TestHerdrSpaceLabel_ColonIsRequired ────────────────────────────────────

// TestHerdrSpaceLabel_ColonIsRequired: herdrSpaceLabelForRef always produces a
// label of the form "nexus3:<ref>", never the bare string "nexus3".
//
// This proves that a sandbox ref (however short) can never produce the label
// used by operator workspace w8 (label "nexus3").
func TestHerdrSpaceLabel_ColonIsRequired(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"proj/box", "nexus3:proj/box"},
		{"a/b", "nexus3:a/b"},
		// Single-segment handle — still gets the colon.
		{"single", "nexus3:single"},
		// The operator's w8 workspace label is "nexus3" — no ref maps to it.
		// herdrSpaceLabelForRef("") = "nexus3:" (still has colon), not "nexus3".
		{"", "nexus3:"},
	}
	for _, c := range cases {
		got := herdrSpaceLabelForRef(c.ref)
		if got != c.want {
			t.Errorf("herdrSpaceLabelForRef(%q) = %q, want %q", c.ref, got, c.want)
		}
		// Extra safety: the produced label must never equal the bare "nexus3" string
		// (the operator's w8 workspace label).
		if got == "nexus3" {
			t.Errorf("herdrSpaceLabelForRef(%q) = %q which equals the operator's protected label", c.ref, got)
		}
	}
}

// ── TestHerdrSpaceLabel_OperatorWorkspaceNeverClaimed ─────────────────────

// TestHerdrSpaceLabel_OperatorWorkspaceNeverClaimed: even if a binding with
// label "nexus3" (the operator's w8 workspace, no colon) were somehow present
// in the store, HerdrSpaceGetByLabel with any colon-qualified label can never
// match it. This is the exact real-world shape: w8 label = "nexus3".
func TestHerdrSpaceLabel_OperatorWorkspaceNeverClaimed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// Seed a binding with the operator's w8 label (no colon) directly into the store.
	operatorBinding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3", // w8's exact label — PROTECTED
		HerdrWorkspaceID: "w8",
		SandboxHandle:    "w8",
		SandboxID:        "sb-operator",
	}
	if err := HerdrSpacePut(ctx, root, operatorBinding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	// A colon-qualified lookup for any real sandbox must not find w8.
	for _, ref := range []string{"proj/box", "a/b", "nexus3/sub"} {
		label := herdrSpaceLabelForRef(ref) // always "nexus3:<ref>"
		_, err := HerdrSpaceGetByLabel(ctx, root, label)
		if !errors.Is(err, ErrHerdrSpaceNotFound) {
			t.Errorf("lookup of %q found something in store; must not claim operator w8: err=%v", label, err)
		}
	}

	// Verify the operator binding is still untouched.
	got, err := HerdrSpaceGetByLabel(ctx, root, "nexus3")
	if err != nil {
		t.Fatalf("operator binding was removed unexpectedly: %v", err)
	}
	if got.HerdrWorkspaceID != "w8" {
		t.Errorf("operator binding corrupted; got workspace_id=%q, want w8", got.HerdrWorkspaceID)
	}
}

// ── TestHerdrEnsureSandbox_CreateNotCalledOnSecondEnsure ──────────────────

// TestHerdrEnsureSandbox_CreateNotCalledOnSecondEnsure: a second ensure call
// that sees the sandbox as existing (get returns nil) does NOT call create.
// This covers the sequential-call idempotency property: once a sandbox is
// created, a subsequent ensure on the same ref is a safe no-op.
//
// Note: herdrAgentEnsureSandboxExists has no rollback logic; the failure policy
// "VM stays, binding failure is the caller's problem" is enforced at the
// herdrPluginSpaceAgent level, not here. Testing rollback absence at this layer
// would require adding a seam: herdrPluginSpaceCreate takes a concrete
// *service.Service, so it cannot be injected AS WRITTEN — but a
// herdrSpaceCreateFn var is one line, the same pattern used for herdrEnsureFn.
// Saying "not reachable" would overstate it. This test covers the directly
// testable property; the rollback path remains unguarded by choice, not by
// impossibility.
func TestHerdrEnsureSandbox_CreateNotCalledOnSecondEnsure(t *testing.T) {
	// Simulate: sandbox absent → create called → record now "exists".
	created := false
	createFn := func(_ context.Context, _ string, _ io.Writer) error {
		created = true
		return nil
	}

	var buf bytes.Buffer
	// First call: absent → create called.
	if err := herdrAgentEnsureSandboxExists(context.Background(), "p/b", &buf, absentGet, createFn); err != nil {
		t.Fatalf("ensure failed: %v", err)
	}
	if !created {
		t.Fatal("create not called on first ensure")
	}

	// Second call: sandbox now "exists" (nopGet) → create must NOT be called again.
	created = false
	if err := herdrAgentEnsureSandboxExists(context.Background(), "p/b", &buf, nopGet, createFn); err != nil {
		t.Fatalf("second ensure failed: %v", err)
	}
	if created {
		t.Error("create called on second ensure when sandbox already exists")
	}
}

// TestHerdrEnsureSandbox_TransientGetErrorDoesNotCreate pins the classifier.
//
// The first version of herdrAgentEnsureSandboxExists treated ANY error from
// get as "absent" and fell through to create. That is wrong in the one case
// where being wrong is expensive: a transient store failure would trigger the
// creation of a sandbox that already exists, precisely when the store is least
// able to say otherwise. Only a definite store.ErrNotFound may create.
//
// MUTATION PROOF: in herdrAgentEnsureSandboxExists, delete the
// `if !errors.Is(err, store.ErrNotFound)` guard so every error falls through
// to create — this test goes RED with "create called after a transient error".
func TestHerdrEnsureSandbox_TransientGetErrorDoesNotCreate(t *testing.T) {
	boom := errors.New("store: connection reset")
	getFails := func(_ context.Context, _ string) (domain.Sandbox, error) {
		return domain.Sandbox{}, boom
	}
	created := false
	create := func(_ context.Context, _ string, _ io.Writer) error {
		created = true
		return nil
	}

	var buf bytes.Buffer
	err := herdrAgentEnsureSandboxExists(context.Background(), "proj/box", &buf, getFails, create)

	if created {
		t.Error("create called after a transient error: a store failure must not be read as absence")
	}
	if err == nil {
		t.Fatal("want error propagated from get, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error does not wrap the underlying cause: %v", err)
	}
}

// ── TestHerdrPluginSpaceAgent_EnsureIsCalled ──────────────────────────────

// TestHerdrPluginSpaceAgent_EnsureIsCalled: herdrPluginSpaceAgent must invoke
// herdrEnsureFn. This test pins that it is CALLED, not WHEN: moving the call
// after step 1 still fails, but via the nil-svc panic and with a message that
// misdiagnoses it as "never called". Ordering is not guarded here — do not
// read this test as protecting it. Without this test the ENTIRE
// create-if-absent feature could be deleted from production and the suite would
// stay green — every other test here exercises the callee in isolation.
//
// The stub records that it RAN rather than relying only on its sentinel
// propagating. An earlier version asserted the sentinel alone, which meant the
// mutation was caught by a nil-pointer panic in a LATER step rather than by
// this test's own predicate: correct verdict, wrong reason. Making
// herdrSpaceAgentProjectDir nil-safe would have silently disarmed it. The
// recover() below converts that incidental panic into a legible failure, so
// the test states why it failed instead of dumping a stack.
//
// MUTATION PROOF: delete the herdrEnsureFn(...) block in herdrPluginSpaceAgent
// and this test goes RED with "herdrEnsureFn was never called".
func TestHerdrPluginSpaceAgent_EnsureIsCalled(t *testing.T) {
	sentinel := errors.New("ensure hook reached")
	called := false
	old := herdrEnsureFn
	herdrEnsureFn = func(
		_ context.Context, _ string, _ io.Writer,
		_ func(context.Context, string) (domain.Sandbox, error),
		_ func(context.Context, string, io.Writer) error,
	) error {
		called = true
		return sentinel
	}
	defer func() { herdrEnsureFn = old }()

	var buf bytes.Buffer
	var err error
	func() {
		// A nil svc panics at the first svc use. That only happens when the
		// ensure call block is gone, so translate it into this test's own
		// claim rather than letting a raw stack trace stand in for a verdict.
		defer func() {
			if r := recover(); r != nil && !called {
				t.Fatalf("herdrEnsureFn was never called: herdrPluginSpaceAgent "+
					"ran past step 0 and panicked on the nil svc (%v) — the "+
					"create-if-absent wiring is missing", r)
			} else if r != nil {
				panic(r)
			}
		}()
		err = herdrPluginSpaceAgent(context.Background(), "proj/box", "brief", false, false, &buf, nil, "")
	}()

	if !called {
		t.Fatal("herdrEnsureFn was never called: the create-if-absent wiring is missing")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("ensure ran but its error did not propagate; got %v", err)
	}
}

// ── TestSpaceCreate_NoFocusFlagParsed ─────────────────────────────────────

// TestSpaceCreate_NoFocusFlagParsed: --no-focus is accepted before the sandbox
// ref and does not produce a UsageError. The call fails later (no real sandbox
// service), but the flag must be consumed without complaint. The default is
// focus=true; this test exercises the opt-out path.
func TestSpaceCreate_NoFocusFlagParsed(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)
	err := runHerdrPlugin(context.Background(), []string{"space-create", "--no-focus", "proj/x"}, out)
	if err == nil {
		t.Fatal("expected an error (no real sandbox service), got nil")
	}
	if _, ok := err.(*UsageError); ok {
		t.Fatalf("--no-focus caused a UsageError; flag was not consumed: %v", err)
	}
}
