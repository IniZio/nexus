package supervisor

// reacquire_ca_test.go proves the crash path actually SEEDS the persisted MITM
// CA (D-HSH-18, ticket 13 / slice s15-ca-persistence).
//
// # Why these tests target reacquireSeedInput
//
// reacquireSeedInput is the function RunReacquire calls, and its return value
// is handed straight to serveAdoptedSupervisor — it is the production wiring,
// not a stand-in. That matters here more than usual: this slice exists because
// the whole CA-seeding mechanism (service.CASeed, mitm.Config.SeedCACertPEM,
// serveAdoptedInput.seedCA) was already built and unit-tested while the crash
// path passed nil, and a mechanism with no caller is the defect class this
// motive has already shipped once (53ac4a8).
//
// The mutation these tests exist to catch is reverting `seedCA: seedCA` back
// to `seedCA: nil` in reacquireSeedInput: that must turn
// TestReacquireSeedInput_SeedsThePersistedCA RED.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/statedir"
)

// persistCAFor mints a CA the way the perimeter does and persists it where the
// crash path will look for it, returning the proxy it came from so the caller
// can compare trust anchors.
func persistCAFor(t *testing.T, storeRoot string, id domain.SandboxID) *mitm.Proxy {
	t.Helper()
	p, err := mitm.New(mitm.Config{SandboxID: id})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	certPEM, keyPEM, err := p.CAKeyPair()
	if err != nil {
		t.Fatalf("CAKeyPair: %v", err)
	}
	if err := statedir.SaveCA(statedir.SupervisorDir(storeRoot, id), certPEM, keyPEM); err != nil {
		t.Fatalf("SaveCA: %v", err)
	}
	return p
}

// TestReacquireSeedInput_SeedsThePersistedCA is the mutation-bearing proof of
// this slice. It asserts three things at once, all on the real call site:
//
//  1. the input handed to serveAdoptedSupervisor carries a NON-NIL seedCA
//     (the mutation target — reverting to nil fails here),
//  2. that seed is the SAME trust anchor the guest already imported, checked
//     by feeding it through mitm.New exactly as StartPerimeterOnly does,
//  3. caLost is false, so the operator is told TLS survived.
func TestReacquireSeedInput_SeedsThePersistedCA(t *testing.T) {
	storeRoot := t.TempDir()
	sb := completeReacquirableSandbox()
	original := persistCAFor(t, storeRoot, sb.ID)

	in, caLost := reacquireSeedInput(
		Config{SandboxRef: sb.ID.String(), StoreRoot: storeRoot},
		nil, nil, nil, sb, nil,
	)

	if in.seedCA == nil {
		t.Fatal("crash path built a serveAdoptedInput with seedCA == nil despite a persisted CA on disk — " +
			"the replacement perimeter would mint a fresh CA and break every in-guest TLS session")
	}
	if caLost {
		t.Error("caLost is true even though the CA was recovered")
	}
	if in.logPrefix != "supervisor.reacquire" {
		t.Errorf("logPrefix = %q, want supervisor.reacquire", in.logPrefix)
	}
	if in.waitForPID != 0 {
		t.Errorf("waitForPID = %d, want 0 — the previous supervisor is dead on this path", in.waitForPID)
	}

	// The seed must be a trust anchor mitm.New accepts AND the same one.
	seeded, err := mitm.New(mitm.Config{
		SandboxID:     sb.ID,
		SeedCACertPEM: in.seedCA.CertPEM,
		SeedCAKeyPEM:  in.seedCA.KeyPEM,
	})
	if err != nil {
		t.Fatalf("mitm.New with the crash path's seed: %v", err)
	}
	if seeded.CACert().SerialNumber.Cmp(original.CACert().SerialNumber) != 0 {
		t.Fatalf("re-acquired perimeter's CA serial = %v, want %v",
			seeded.CACert().SerialNumber, original.CACert().SerialNumber)
	}
}

// TestReacquireSeedInput_FailsClosedOnDamagedCA covers the fail-closed leg for
// every damage mode: the CA is NOT seeded, caLost is true, and — critically —
// reacquireSeedInput still returns a usable input rather than failing the
// re-acquisition. A crash-recovery path that died on a truncated CA file would
// turn a recoverable sandbox into an unrecoverable one.
func TestReacquireSeedInput_FailsClosedOnDamagedCA(t *testing.T) {
	p, err := mitm.New(mitm.Config{})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	certPEM, keyPEM, err := p.CAKeyPair()
	if err != nil {
		t.Fatalf("CAKeyPair: %v", err)
	}

	cases := []struct {
		name  string
		write []byte // nil means: write no file at all
	}{
		{name: "absent"},
		{name: "empty", write: []byte{}},
		{name: "garbage", write: []byte("-----BEGIN NONSENSE-----\n")},
		{name: "cert without key (asymmetry)", write: certPEM},
		{name: "key without cert (asymmetry)", write: keyPEM},
		{name: "truncated", write: append(append([]byte(nil), certPEM...), keyPEM[:len(keyPEM)/2]...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storeRoot := t.TempDir()
			sb := completeReacquirableSandbox()
			dir := statedir.SupervisorDir(storeRoot, sb.ID)
			if tc.write != nil {
				if err := statedir.Ensure(dir); err != nil {
					t.Fatalf("Ensure: %v", err)
				}
				if err := os.WriteFile(statedir.CAPath(dir), tc.write, 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			}

			in, caLost := reacquireSeedInput(
				Config{SandboxRef: sb.ID.String(), StoreRoot: storeRoot},
				nil, nil, nil, sb, nil,
			)

			if in.seedCA != nil {
				t.Fatalf("a %s CA file was seeded into the replacement perimeter", tc.name)
			}
			if !caLost {
				t.Fatalf("caLost is false for a %s CA file; the operator would believe TLS survived", tc.name)
			}
			// Not wedged: the rest of the input is still serve-able.
			if in.logPrefix != "supervisor.reacquire" || in.waitForPID != 0 {
				t.Fatalf("damaged CA disturbed the rest of the serve input: %+v", in)
			}
		})
	}
}

// TestReacquireSeedInput_ReadsTheSandboxScopedPath pins WHERE the CA is looked
// for. A CA persisted for a DIFFERENT sandbox must never be seeded into this
// one: each sandbox is its own trust domain, and cross-seeding would let one
// guest's compromised CA sign for another.
func TestReacquireSeedInput_ReadsTheSandboxScopedPath(t *testing.T) {
	storeRoot := t.TempDir()
	sb := completeReacquirableSandbox()
	persistCAFor(t, storeRoot, domain.NewSandboxID()) // a DIFFERENT sandbox

	in, caLost := reacquireSeedInput(
		Config{SandboxRef: sb.ID.String(), StoreRoot: storeRoot},
		nil, nil, nil, sb, nil,
	)
	if in.seedCA != nil || !caLost {
		t.Fatal("crash path seeded another sandbox's CA — per-sandbox trust domains are not separated")
	}

	// And the positive control: the same call finds this sandbox's own CA.
	persistCAFor(t, storeRoot, sb.ID)
	in, caLost = reacquireSeedInput(
		Config{SandboxRef: sb.ID.String(), StoreRoot: storeRoot},
		nil, nil, nil, sb, nil,
	)
	if in.seedCA == nil || caLost {
		t.Fatalf("crash path did not find the CA at %s",
			filepath.Join(statedir.SupervisorDir(storeRoot, sb.ID), statedir.CAFileName))
	}
}
