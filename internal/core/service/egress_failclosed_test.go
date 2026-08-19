package service_test

// egress_failclosed_test.go — D-PD-33 regression tests
//
// Each test drives real production code and is proven below by mutation:
// reverting the target production line makes the test fail. The mutation
// evidence is in the commit message / PR description; run the annotated
// command to reproduce.
//
// Tests:
//  1. TestD_PD33_StartSupervisor_FailClosed — line service.go:678
//     A sandbox with empty AllowedHosts and OpenEgress=false must NOT get
//     AllowAll; it must get a deny-all MITM perimeter instead.
//  2. TestD_PD33_Fork_OpenEgressInherited — line service.go:961
//     A forked child of an open-egress parent inherits OpenEgress=true.
//  3. TestD_PD33_Fork_ClosedEgressInherited — line service.go:961
//     A forked child of a closed-egress parent inherits OpenEgress=false.
//  4. TestD_PD33_Restore_OpenEgressInherited — line service.go:1119
//     A restored child of an open-egress origin inherits OpenEgress=true.
//  5. TestD_PD33_Restore_MissingOriginFailsLoudly — service.go:1102-1104
//     Restore with a missing origin returns an error.
//  6. TestD_PD33_GitHubNotInAgentEgressHosts — service/seed.go
//     GitHub hosts absent from AgentEgressHosts; GitHubSecretHosts has all 3.

import (
	"context"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── Test 1: startSupervisor fail-closed (service.go:678) ─────────────────────

// TestD_PD33_StartSupervisor_FailClosed drives service.Start on a sandbox with
// empty AllowedHosts and OpenEgress=false, then asserts that a MITM perimeter
// was assembled (GetPerimeterCACert non-nil). A MITM perimeter means the
// AllowAll branch was NOT taken — the sandbox is fail-closed.
//
// MUTATION PROOF (service.go:678):
//
//	Revert:  allowAll := sb.Envelope.OpenEgress
//	To:      allowAll := len(sb.Envelope.AllowedHosts) == 0
//	Effect:  empty AllowedHosts → allowAll=true → no MITM → GetPerimeterCACert=nil
//	Result:  t.Errorf fires ("must be behind a MITM perimeter"), test FAILS
func TestD_PD33_StartSupervisor_FailClosed(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := newForkNetHookDriver()
	t.Cleanup(drv.closeHosts)
	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker())

	// Seed a sandbox with OpenEgress=false and empty AllowedHosts.
	// This is the fail-closed posture: no curated list, no open egress.
	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "failclosed-sb",
		Project: "proj",
		State:   domain.Created,
		Envelope: domain.Envelope{
			AllowedHosts: nil,   // empty
			OpenEgress:   false, // explicit: closed
		},
	}
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}

	// Start drives startSupervisor when a NetworkHook + broker are attached.
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _, _ = svc.Stop(context.Background(), sb.ID.String()) })

	// The MITM proxy is instantiated when allowAll=false (the new code) because
	// the condition is `!allowAll || len(SecretHosts) > 0`. With allowAll=false
	// and empty SecretHosts, !allowAll is still true → proxy assembled → CA exists.
	//
	// Under the OLD code: allowAll := len(AllowedHosts)==0 → allowAll=true →
	// !allowAll=false, len(SecretHosts)=0 → no proxy → CA=nil → test FAILS.
	if svc.GetPerimeterCACert(sb.ID) == nil {
		t.Errorf(
			"SECURITY REGRESSION — D-PD-33\n" +
				"A sandbox with empty AllowedHosts and OpenEgress=false has no MITM perimeter.\n\n" +
				"startSupervisor must derive allowAll from sb.Envelope.OpenEgress (not from\n" +
				"len(AllowedHosts)==0). With OpenEgress=false the MITM proxy must be assembled\n" +
				"so that all egress is denied. A nil CA cert means no MITM — the sandbox has\n" +
				"uncontrolled network access.\n\n" +
				"Restore service.go:678 to: allowAll := sb.Envelope.OpenEgress",
		)
	}
}

// ── Test 2: Fork inherits OpenEgress=true (service.go:961) ───────────────────

// TestD_PD33_Fork_OpenEgressInherited creates an open-egress parent, forks one
// child, and asserts the child inherits OpenEgress=true. OpenEgress=true with
// empty SecretHosts means no MITM proxy (AllowAll path), so GetPerimeterCACert
// returns nil. Reverting the inheritance line flips OpenEgress to false, causing
// a MITM proxy to be assembled for the child — CA becomes non-nil.
//
// MUTATION PROOF (service.go:961):
//
//	Remove:  OpenEgress: parent.Envelope.OpenEgress,
//	Effect:  child.Envelope.OpenEgress = false → allowAll=false → MITM assembled
//	Result:  CA non-nil → child "open parent must pass no CA" assertion FAILS
func TestD_PD33_Fork_OpenEgressInherited(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := newForkNetHookDriver()
	t.Cleanup(drv.closeHosts)
	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker())

	// Parent: open egress, no AllowedHosts, no SecretHosts.
	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "open-egress-parent",
		Project: "proj",
		State:   domain.Created,
		Envelope: domain.Envelope{
			OpenEgress: true,
		},
	}
	if err := st.Create(context.Background(), parent); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	started, err := svc.Start(context.Background(), parent.ID.String())
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}
	t.Cleanup(func() { _, _ = svc.Stop(context.Background(), started.ID.String()) })

	// Open-egress parent: allowAll=true, no SecretHosts → no MITM → CA=nil.
	if ca := svc.GetPerimeterCACert(parent.ID); ca != nil {
		t.Fatal("harness: open-egress parent must have no MITM CA (allowAll, no SecretHosts)")
	}

	children, err := svc.Fork(context.Background(), parent.ID.String(), 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	child := children[0]

	// Child must inherit OpenEgress=true.
	if !child.Envelope.OpenEgress {
		t.Errorf(
			"SECURITY REGRESSION — D-PD-33\n" +
				"Forked child of open-egress parent has OpenEgress=false.\n\n" +
				"service.go Fork loop must copy parent.Envelope.OpenEgress onto the child.\n" +
				"Restore the line: OpenEgress: parent.Envelope.OpenEgress,",
		)
	}

	// Child must also have no MITM CA (allowAll=true, no SecretHosts).
	if ca := svc.GetPerimeterCACert(child.ID); ca != nil {
		t.Errorf(
			"SECURITY REGRESSION — D-PD-33\n" +
				"Forked child of open-egress parent has a MITM CA, meaning allowAll=false was\n" +
				"used in startSupervisor. The child did not inherit OpenEgress=true from the\n" +
				"parent. Restore: OpenEgress: parent.Envelope.OpenEgress in service.go:961.",
		)
	}
}

// ── Test 3: Fork inherits OpenEgress=false (service.go:961) ──────────────────

// TestD_PD33_Fork_ClosedEgressInherited creates a closed-egress parent with a
// curated allowlist, forks one child, and asserts the child also has
// OpenEgress=false. A closed-egress child with a curated allowlist gets a MITM
// perimeter (CA non-nil). Reverting the inheritance line leaves OpenEgress=false
// by Go's zero-value — this case happens to pass trivially, which is why both
// tests are needed. The meaningful check here is that AllowedHosts IS copied.
//
// MUTATION PROOF (service.go:959 — AllowedHosts clone):
//
//	Remove:  AllowedHosts: slices.Clone(parent.Envelope.AllowedHosts),
//	Effect:  child.AllowedHosts = nil → still no MITM since OpenEgress=false
//	         AND empty AllowedHosts triggers MITM (allowAll=false) — but
//	         child.Envelope.AllowedHosts assertion catches the missing clone.
func TestD_PD33_Fork_ClosedEgressInherited(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := newForkNetHookDriver()
	t.Cleanup(drv.closeHosts)
	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker())

	curated := []string{"api.anthropic.com", "platform.claude.com"}
	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "closed-egress-parent",
		Project: "proj",
		State:   domain.Created,
		Envelope: domain.Envelope{
			AllowedHosts: curated,
			OpenEgress:   false,
		},
	}
	if err := st.Create(context.Background(), parent); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	started, err := svc.Start(context.Background(), parent.ID.String())
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}
	t.Cleanup(func() { _, _ = svc.Stop(context.Background(), started.ID.String()) })

	// Closed-egress parent with curated hosts → MITM assembled → CA non-nil.
	if svc.GetPerimeterCACert(parent.ID) == nil {
		t.Fatal("harness: closed-egress parent with curated hosts must have MITM CA")
	}

	children, err := svc.Fork(context.Background(), parent.ID.String(), 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	child := children[0]

	if child.Envelope.OpenEgress {
		t.Errorf(
			"SECURITY REGRESSION — D-PD-33\n" +
				"Forked child of closed-egress parent has OpenEgress=true.\n" +
				"Restore: OpenEgress: parent.Envelope.OpenEgress in service.go:961.",
		)
	}
	if len(child.Envelope.AllowedHosts) != len(curated) {
		t.Errorf("child AllowedHosts = %v, want clone of parent's %v", child.Envelope.AllowedHosts, curated)
	}
	// Child must also be behind a MITM perimeter (allowAll=false, curated hosts).
	if svc.GetPerimeterCACert(child.ID) == nil {
		t.Errorf(
			"SECURITY REGRESSION — D-PD-33\n" +
				"Forked child of closed-egress parent has no MITM CA.\n" +
				"The child must be governed by a deny-all perimeter, not open egress.",
		)
	}
}

// ── Test 4: Restore inherits OpenEgress (service.go:1119) ────────────────────

// TestD_PD33_Restore_OpenEgressInherited creates an open-egress origin sandbox,
// writes a snapshot referencing it, restores from that snapshot, and asserts the
// child inherits OpenEgress=true.
//
// MUTATION PROOF (service.go:1119):
//
//	Remove:  OpenEgress: origin.Envelope.OpenEgress,
//	Effect:  child.Envelope.OpenEgress = false (Go zero value)
//	Result:  "child must inherit OpenEgress=true" assertion FAILS
func TestD_PD33_Restore_OpenEgressInherited(t *testing.T) {
	aStore, err := artifact.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := newForkNetHookDriver()
	t.Cleanup(drv.closeHosts)
	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker()).WithArtifacts(aStore)

	// Origin sandbox: open egress.
	origin := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "restore-origin",
		Project: "proj",
		State:   domain.Created,
		Envelope: domain.Envelope{
			OpenEgress: true,
		},
	}
	if err := st.Create(context.Background(), origin); err != nil {
		t.Fatalf("seed origin: %v", err)
	}

	// Write a snapshot referencing origin.
	snap := artifact.Snapshot{
		ID:           "d-pd-33-restore-snap",
		SandboxID:    origin.ID,
		Kind:         artifact.KindRetained,
		Size:         4,
		CommitMarker: "committed",
		CreatedAt:    time.Now(),
	}
	if err := aStore.Write(snap, []byte("data")); err != nil {
		t.Fatalf("aStore.Write: %v", err)
	}

	children, err := svc.RestoreFromSnapshot(context.Background(), snap.ID, 1)
	if err != nil {
		t.Fatalf("RestoreFromSnapshot: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}

	if !children[0].Envelope.OpenEgress {
		t.Errorf(
			"SECURITY REGRESSION — D-PD-33\n" +
				"Restored child of open-egress origin has OpenEgress=false.\n\n" +
				"RestoreFromSnapshot must copy origin.Envelope.OpenEgress onto the child.\n" +
				"Restore the line: OpenEgress: origin.Envelope.OpenEgress, in service.go:1119.",
		)
	}
}

// ── Test 5: Restore missing origin fails loudly (service.go:1102-1104) ───────

// TestD_PD33_Restore_MissingOriginFailsLoudly writes a snapshot whose SandboxID
// is the zero value (no sandbox record exists for it) and asserts that
// RestoreFromSnapshot returns an error rather than silently creating children
// with an unknown egress policy.
//
// MUTATION PROOF (service.go:1102-1104 — the new early-return error):
//
//	Revert:  if originErr != nil { return nil, fmt.Errorf(...) }
//	To:      haveOrigin := originErr == nil (old optional behavior)
//	Effect:  RestoreFromSnapshot returns children, err=nil
//	Result:  "expected error for missing origin" assertion FAILS
func TestD_PD33_Restore_MissingOriginFailsLoudly(t *testing.T) {
	aStore, err := artifact.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := newForkNetHookDriver()
	t.Cleanup(drv.closeHosts)
	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker()).WithArtifacts(aStore)

	// Snapshot whose SandboxID is zero — no sandbox record exists for it.
	snap := artifact.Snapshot{
		ID:           "d-pd-33-no-origin-snap",
		SandboxID:    domain.SandboxID{}, // zero: store.Get will fail
		Kind:         artifact.KindRetained,
		Size:         4,
		CommitMarker: "committed",
		CreatedAt:    time.Now(),
	}
	if err := aStore.Write(snap, []byte("data")); err != nil {
		t.Fatalf("aStore.Write: %v", err)
	}

	_, err = svc.RestoreFromSnapshot(context.Background(), snap.ID, 1)
	if err == nil {
		t.Error(
			"SECURITY REGRESSION — D-PD-33\n" +
				"RestoreFromSnapshot with a missing origin returned no error.\n\n" +
				"When the origin sandbox cannot be found, egress policy cannot be\n" +
				"reconstructed. The call must fail loudly rather than producing a\n" +
				"child with unknown or no egress policy.\n\n" +
				"Restore the early-return at service.go:1102-1104.",
		)
	}
}

// ── Test 6: GitHub hosts absent from AgentEgressHosts (seed.go) ──────────────

// TestD_PD33_GitHubNotInAgentEgressHosts calls AgentEgressHosts(cred.ClaudeCodeProfile) and
// GitHubSecretHosts directly — both are package-level calls into production
// code. It also verifies all three GitHub hostnames are present in
// GitHubSecretHosts (the uploads host is new in D-PD-33).
//
// MUTATION PROOF (secret.go:23 — GitHubSecretHosts):
//
//	Remove "uploads.github.com" from GitHubSecretHosts
//	Result: "GitHubSecretHosts missing uploads.github.com" assertion FAILS
//
// MUTATION PROOF (seed.go:AgentEgressHosts):
//
//	Add "github.com" to the returned slice
//	Result: "AgentEgressHosts contains github.com" assertion FAILS
func TestD_PD33_GitHubNotInAgentEgressHosts(t *testing.T) {
	for _, h := range service.AgentEgressHosts(cred.ClaudeCodeProfile) {
		if isGitHubHostForTest(h) {
			t.Errorf(
				"SECURITY VIOLATION — D-PD-33\n"+
					"AgentEgressHosts(cred.ClaudeCodeProfile) contains GitHub host %q.\n"+
					"GitHub hosts must never appear in agent AllowedHosts.", h,
			)
		}
	}

	required := []string{"github.com", "api.github.com", "uploads.github.com"}
	for _, want := range required {
		found := false
		for _, h := range service.GitHubSecretHosts {
			if h == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf(
				"D-PD-33: GitHubSecretHosts is missing %q.\n"+
					"All three GitHub hosts must be present for MITM credential swap.\n"+
					"Add it to service.GitHubSecretHosts in secret.go.", want,
			)
		}
	}
}

// isGitHubHostForTest mirrors isGitHubHost from git_identity.go without the
// internal package dependency.
func isGitHubHostForTest(h string) bool {
	return h == "github.com" || h == "api.github.com" || h == "uploads.github.com" ||
		len(h) > len("github.com") && h[len(h)-len(".github.com"):] == ".github.com"
}
