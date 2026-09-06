package mitm

// ca_lifetime_test.go pins the minted CA's validity window (D-HSH-18, ticket
// 13 / slice s15-ca-persistence).
//
// The window used to be 10 years, which was defensible only while the private
// key died with the supervisor process. It is now written to disk, so the
// certificate's validity is the only thing bounding how long a leaked copy of
// that key remains a usable trust anchor. This test exists so that shortening
// is not silently undone, and so the two ends of the bound cannot drift: the
// value here and the guard in statedir.LoadCA (which refuses an expired CA).

import (
	"testing"
	"time"
)

func TestGenerateCA_LifetimeIsSandboxScoped(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	leaf := p.CACert()
	got := leaf.NotAfter.Sub(leaf.NotBefore)

	// NotBefore is backdated an hour for clock skew, so the window is
	// caLifetime + 1h.
	want := caLifetime + time.Hour
	if got != want {
		t.Errorf("minted CA validity window = %v, want %v", got, want)
	}

	if caLifetime > 365*24*time.Hour {
		t.Errorf("caLifetime = %v: a CA private key now lives on disk, so a multi-year window is not a sandbox-scoped bound", caLifetime)
	}
	// The lower bound matters too: a window shorter than a realistic sandbox
	// lifetime would let a CA expire mid-run, and a running proxy keeps
	// signing with its in-memory anchor regardless of expiry — a failure
	// persistence cannot recover from. The reference host's oldest observed
	// supervisor state dir was 13 days.
	if caLifetime < 30*24*time.Hour {
		t.Errorf("caLifetime = %v: shorter than a realistic sandbox lifetime; a CA could expire mid-run", caLifetime)
	}
}
