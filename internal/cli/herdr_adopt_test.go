package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

// fakeAdoptGetter resolves a fixed set of sandboxes by handle or ID.
type fakeAdoptGetter struct {
	byRef map[string]domain.Sandbox
	calls int
}

func (f *fakeAdoptGetter) Get(_ context.Context, ref string) (domain.Sandbox, error) {
	f.calls++
	if sb, ok := f.byRef[ref]; ok {
		return sb, nil
	}
	return domain.Sandbox{}, store.ErrNotFound
}

func adoptFixture(t *testing.T) (string, *fakeAdoptGetter, domain.Sandbox) {
	t.Helper()
	storeRoot := t.TempDir()
	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Project: "proj",
		Name:    "outside-herdr",
		State:   domain.Running,
	}
	g := &fakeAdoptGetter{byRef: map[string]domain.Sandbox{
		sb.Handle():    sb,
		sb.ID.String(): sb,
	}}
	return storeRoot, g, sb
}

// TBR-PD-20. A sandbox created outside herdr had no binding, so every herdr
// ACTION dead-ended with "binding not found" even though the sandbox was
// listed in the workspaces overlay. Visible and inert is the worst state: it
// looks controllable and is not.
func TestHerdrSpaceResolveOrAdopt_AdoptsSandboxCreatedOutsideHerdr(t *testing.T) {
	storeRoot, g, sb := adoptFixture(t)
	ctx := context.Background()

	// Precondition: the old path genuinely fails, so this test is exercising
	// the gap rather than asserting something that always worked.
	if _, err := herdrSpaceResolve(ctx, storeRoot, sb.Handle()); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Fatalf("precondition: expected ErrHerdrSpaceNotFound, got %v", err)
	}

	b, adopted, err := herdrSpaceResolveOrAdopt(ctx, g, storeRoot, sb.Handle())
	if err != nil {
		t.Fatalf("resolveOrAdopt: %v", err)
	}
	if !adopted {
		t.Error("adopted = false; the binding did not exist, so it must have been minted")
	}
	if b.SandboxHandle != sb.Handle() {
		t.Errorf("SandboxHandle = %q, want %q", b.SandboxHandle, sb.Handle())
	}
	if b.SandboxID != sb.ID.String() {
		t.Errorf("SandboxID = %q, want %q", b.SandboxID, sb.ID.String())
	}
	if b.SpaceLabel != herdrSpaceLabelForRef(sb.Handle()) {
		t.Errorf("SpaceLabel = %q, want %q", b.SpaceLabel, herdrSpaceLabelForRef(sb.Handle()))
	}
	// No herdr workspace may be created by adoption: pause/resume/remove do
	// not need one, and minting one here would leave an empty workspace behind
	// every time someone paused a sandbox.
	if b.HerdrWorkspaceID != "" {
		t.Errorf("HerdrWorkspaceID = %q, want empty — adoption must not create a workspace", b.HerdrWorkspaceID)
	}

	// The binding must be PERSISTED, or the next subcommand adopts again and
	// pause/resume (which look the label up in the store) still fail.
	got, err := HerdrSpaceGetByLabel(ctx, storeRoot, b.SpaceLabel)
	if err != nil {
		t.Fatalf("binding was not persisted: %v", err)
	}
	if got.SandboxHandle != sb.Handle() {
		t.Errorf("persisted handle = %q, want %q", got.SandboxHandle, sb.Handle())
	}
}

// Adoption must be idempotent and must not re-mint on the second call.
func TestHerdrSpaceResolveOrAdopt_SecondCallResolvesWithoutAdopting(t *testing.T) {
	storeRoot, g, sb := adoptFixture(t)
	ctx := context.Background()

	if _, _, err := herdrSpaceResolveOrAdopt(ctx, g, storeRoot, sb.Handle()); err != nil {
		t.Fatalf("first adopt: %v", err)
	}
	callsAfterFirst := g.calls

	b, adopted, err := herdrSpaceResolveOrAdopt(ctx, g, storeRoot, sb.Handle())
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if adopted {
		t.Error("adopted = true on the second call; the binding already existed")
	}
	if b.SandboxHandle != sb.Handle() {
		t.Errorf("SandboxHandle = %q, want %q", b.SandboxHandle, sb.Handle())
	}
	if g.calls != callsAfterFirst {
		t.Errorf("service was consulted again (%d -> %d); an existing binding must resolve from the store alone",
			callsAfterFirst, g.calls)
	}
}

// A key that names neither a binding nor a sandbox must still fail, and the
// error must say both things were tried. Adopting anything that is asked for
// would create bindings to sandboxes that do not exist.
func TestHerdrSpaceResolveOrAdopt_UnknownKeyStillFails(t *testing.T) {
	storeRoot, g, _ := adoptFixture(t)

	_, adopted, err := herdrSpaceResolveOrAdopt(context.Background(), g, storeRoot, "no/such-sandbox")
	if err == nil {
		t.Fatal("adopted a key that names no sandbox")
	}
	if adopted {
		t.Error("adopted = true for a non-existent sandbox")
	}
	if got := err.Error(); !strings.Contains(got, "no sandbox by that name") {
		t.Errorf("error %q does not explain that no sandbox matched", got)
	}
}

// Adoption resolves by ID too, not just handle — herdr actions pass whatever
// the overlay row carries.
func TestHerdrSpaceResolveOrAdopt_AdoptsByID(t *testing.T) {
	storeRoot, g, sb := adoptFixture(t)

	b, adopted, err := herdrSpaceResolveOrAdopt(context.Background(), g, storeRoot, sb.ID.String())
	if err != nil {
		t.Fatalf("adopt by ID: %v", err)
	}
	if !adopted {
		t.Error("adopted = false")
	}
	// The binding must be keyed by HANDLE, not by whatever key was passed, or
	// adopting by ID and then by handle would mint two bindings for one sandbox.
	if b.SpaceLabel != herdrSpaceLabelForRef(sb.Handle()) {
		t.Errorf("SpaceLabel = %q, want the handle-derived label %q",
			b.SpaceLabel, herdrSpaceLabelForRef(sb.Handle()))
	}
}

// herdrSpaceEnsureWorkspace must not touch herdr when a workspace already
// exists, and must refuse (rather than silently produce an empty ID) when it
// needs to create one and herdr is not reachable.
func TestHerdrSpaceEnsureWorkspace_NoopWhenPresent(t *testing.T) {
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:proj/x", SandboxHandle: "proj/x", HerdrWorkspaceID: "wZ"}
	got, rootPaneID, err := herdrSpaceEnsureWorkspace(context.Background(), nil, t.TempDir(), "", b)
	if err != nil {
		t.Fatalf("ensure with an existing workspace consulted herdr: %v", err)
	}
	if got.HerdrWorkspaceID != "wZ" {
		t.Errorf("workspace ID = %q, want wZ unchanged", got.HerdrWorkspaceID)
	}
	if rootPaneID != "" {
		t.Errorf("root pane ID = %q, want empty — no workspace was minted", rootPaneID)
	}
}

func TestHerdrSpaceEnsureWorkspace_RefusesWithoutHerdrBin(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:proj/x", SandboxHandle: "proj/x"}
	got, rootPaneID, err := herdrSpaceEnsureWorkspace(context.Background(), nil, t.TempDir(), "", b)
	if err == nil {
		t.Fatal("returned nil error with no herdr binary and no workspace to reuse")
	}
	if got.HerdrWorkspaceID != "" {
		t.Errorf("workspace ID = %q, want empty on failure", got.HerdrWorkspaceID)
	}
	if rootPaneID != "" {
		t.Errorf("root pane ID = %q, want empty on failure", rootPaneID)
	}
}
