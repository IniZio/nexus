package supervisor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

// refuseFastBound is the ceiling asserted for a refusal that must happen
// BEFORE RunAdopt lists on the handoff socket and calls Accept. It is far
// below adoptHandoffAcceptTimeout (20s): a passing check that took the
// Accept-timeout path instead of the fast refusal path would still return a
// non-nil error, so elapsed time — not just "did it error" — is what proves
// the guard actually ran before Listen/Accept. A test that only asserted
// err != nil would pass identically whether the guard fired or was deleted
// entirely, since a deleted guard still eventually errors out from the
// Accept timeout; that is exactly the "checker shares the broken mechanism"
// failure shape this repo has been bitten by before, so this bound is the
// real discriminator.
const refuseFastBound = 2 * time.Second

// TestRunAdopt_IncompleteNetnsIdentity_Refuses is the mutation-bearing proof
// that RunAdopt independently re-checks the persisted netns identity rather
// than trusting its caller (the CLI verb already checked the same fields
// before spawning this process). A sandbox record with all five identity
// fields at their zero value must refuse well before the 20s handoff-accept
// deadline — proving the guard, not the timeout, produced the error.
func TestRunAdopt_IncompleteNetnsIdentity_Refuses(t *testing.T) {
	storeRoot := t.TempDir()
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Project: "proj",
		Name:    "adopt-refuse",
		State:   domain.Running,
		// NetnsChildPID/PGID/StartTime, GuestTapName, CHAPISocket all left
		// at their zero value: the incomplete-identity case.
	}
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stateDir := t.TempDir()
	handoffSock := filepath.Join(stateDir, "handoff-test.sock")

	start := time.Now()
	err = RunAdopt(Config{
		SandboxRef: sb.ID.String(),
		StoreRoot:  storeRoot,
		StateDir:   stateDir,
	}, handoffSock)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected RunAdopt to refuse a sandbox with an incomplete netns identity")
	}
	if elapsed > refuseFastBound {
		t.Errorf("RunAdopt took %s to refuse (bound %s) — this is the Accept-timeout path, not the identity guard: "+
			"the guard did not fire before Listen/Accept", elapsed, refuseFastBound)
	}
}

// TestRunAdopt_PartialNetnsIdentity_EachFieldAloneRefuses mirrors the CLI-side
// guard test. Two families of row, per field — see the CLI-side
// TestSupervisorUpgrade_PartialNetnsIdentity_EachFieldAloneRefuses for the
// full rationale: "only X set" (every other field zero) refuses trivially
// off the other four zero fields and does NOT pin conjunct X on its own;
// "missing only X" (every field except X populated) is the row that
// actually pins X — it is the only row that would go green if X's check
// were deleted. A prior version of this table had only one "missing only X"
// row (CHAPISocket), so four of the five conjuncts were asserted but never
// exercised.
func TestRunAdopt_PartialNetnsIdentity_EachFieldAloneRefuses(t *testing.T) {
	cases := []struct {
		name string
		set  func(sb *domain.Sandbox)
	}{
		{"only PID", func(sb *domain.Sandbox) { sb.NetnsChildPID = 4242 }},
		{"only PGID", func(sb *domain.Sandbox) { sb.NetnsChildPGID = 4242 }},
		{"only StartTime", func(sb *domain.Sandbox) { sb.NetnsChildStartTime = 123456 }},
		{"only GuestTapName", func(sb *domain.Sandbox) { sb.GuestTapName = "nx3g-test" }},
		{"only CHAPISocket", func(sb *domain.Sandbox) { sb.CHAPISocket = "/tmp/fake.sock" }},
		{"missing only PID", func(sb *domain.Sandbox) {
			sb.NetnsChildPGID = 4242
			sb.NetnsChildStartTime = 123456
			sb.GuestTapName = "nx3g-test"
			sb.CHAPISocket = "/tmp/fake.sock"
		}},
		{"missing only PGID", func(sb *domain.Sandbox) {
			sb.NetnsChildPID = 4242
			sb.NetnsChildStartTime = 123456
			sb.GuestTapName = "nx3g-test"
			sb.CHAPISocket = "/tmp/fake.sock"
		}},
		{"missing only StartTime", func(sb *domain.Sandbox) {
			sb.NetnsChildPID = 4242
			sb.NetnsChildPGID = 4242
			sb.GuestTapName = "nx3g-test"
			sb.CHAPISocket = "/tmp/fake.sock"
		}},
		{"missing only GuestTapName", func(sb *domain.Sandbox) {
			sb.NetnsChildPID = 4242
			sb.NetnsChildPGID = 4242
			sb.NetnsChildStartTime = 123456
			sb.CHAPISocket = "/tmp/fake.sock"
		}},
		{"missing only CHAPISocket", func(sb *domain.Sandbox) {
			sb.NetnsChildPID = 4242
			sb.NetnsChildPGID = 4242
			sb.NetnsChildStartTime = 123456
			sb.GuestTapName = "nx3g-test"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storeRoot := t.TempDir()
			st, err := store.NewFileStore(storeRoot)
			if err != nil {
				t.Fatalf("NewFileStore: %v", err)
			}
			sb := domain.Sandbox{
				ID:      domain.NewSandboxID(),
				Project: "proj",
				Name:    "adopt-refuse-partial",
				State:   domain.Running,
			}
			tc.set(&sb)
			if err := st.Create(context.Background(), sb); err != nil {
				t.Fatalf("Create: %v", err)
			}

			stateDir := t.TempDir()
			handoffSock := filepath.Join(stateDir, "handoff-test.sock")
			start := time.Now()
			err = RunAdopt(Config{
				SandboxRef: sb.ID.String(),
				StoreRoot:  storeRoot,
				StateDir:   stateDir,
			}, handoffSock)
			elapsed := time.Since(start)
			if err == nil {
				t.Fatalf("case %q: expected refusal", tc.name)
			}
			if elapsed > refuseFastBound {
				t.Errorf("case %q: took %s to refuse (bound %s) — Accept-timeout path, not the identity guard", tc.name, elapsed, refuseFastBound)
			}
		})
	}
}
