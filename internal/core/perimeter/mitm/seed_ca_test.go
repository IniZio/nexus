package mitm

// seed_ca_test.go proves the CA-seeding path added for the hot-swap handoff
// (motive nexus3-host-supervisor-hotswap, ticket 08): a replacement
// supervisor must continue signing leaf certificates with the SAME CA the
// guest already trusts, not a freshly minted one, or every HTTPS connection
// through the proxy fails certificate validation after an adopt.

import (
	"testing"
)

// TestNew_SeedCA_ReusesGivenCA proves that when SeedCACertPEM/SeedCAKeyPEM
// are both set, New() installs THAT CA rather than minting a fresh one.
func TestNew_SeedCA_ReusesGivenCA(t *testing.T) {
	original, err := New(Config{})
	if err != nil {
		t.Fatalf("New (original): %v", err)
	}
	certPEM, keyPEM, err := original.CAKeyPair()
	if err != nil {
		t.Fatalf("CAKeyPair: %v", err)
	}

	seeded, err := New(Config{SeedCACertPEM: certPEM, SeedCAKeyPEM: keyPEM})
	if err != nil {
		t.Fatalf("New (seeded): %v", err)
	}

	if seeded.CACert().SerialNumber.Cmp(original.CACert().SerialNumber) != 0 {
		t.Errorf("seeded CA serial = %v, want %v (same CA as original) — a replacement supervisor minted a fresh CA instead of reusing the seeded one",
			seeded.CACert().SerialNumber, original.CACert().SerialNumber)
	}
}

// TestNew_NoSeed_MintsFreshCA is the control: with no seed given, two
// independently constructed proxies must have DIFFERENT CAs (never
// accidentally sharing state).
func TestNew_NoSeed_MintsFreshCA(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatalf("New (a): %v", err)
	}
	b, err := New(Config{})
	if err != nil {
		t.Fatalf("New (b): %v", err)
	}
	if a.CACert().SerialNumber.Cmp(b.CACert().SerialNumber) == 0 {
		t.Error("two unseeded proxies produced the same CA serial — CAs must be independently generated")
	}
}

// TestNew_PartialSeed_Refuses proves the guard against a partial seed pair
// (cert without key, or vice versa) — silently falling back to a fresh CA
// in that case would mask a caller bug (e.g. a dropped field in a handoff
// payload) as a normal boot.
func TestNew_PartialSeed_Refuses(t *testing.T) {
	full, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	certPEM, keyPEM, err := full.CAKeyPair()
	if err != nil {
		t.Fatalf("CAKeyPair: %v", err)
	}

	if _, err := New(Config{SeedCACertPEM: certPEM}); err == nil {
		t.Error("New with cert-only seed: expected error, got nil")
	}
	if _, err := New(Config{SeedCAKeyPEM: keyPEM}); err == nil {
		t.Error("New with key-only seed: expected error, got nil")
	}
}
