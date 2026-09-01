package statedir_test

// ca_test.go covers the persisted MITM CA (D-HSH-18, ticket 13 / slice
// s15-ca-persistence).
//
// Every case here drives the REAL functions the perimeter and the crash path
// call — statedir.SaveCA, statedir.LoadCA — and the round-trip case takes its
// input from a real mitm.Proxy's CAKeyPair and feeds the output back into a
// real mitm.New seed, so the assertion is "the recovered CA is the same trust
// anchor", not "the bytes came back".

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/statedir"
)

// ── 1. round trip through the real production types ──────────────────────────

// TestSaveLoadCA_RoundTripsTheSameTrustAnchor is the core proof of this slice:
// the CA a perimeter minted, persisted, and later loaded back is the SAME CA,
// so a replacement supervisor keeps signing leaf certificates the guest
// already trusts.
//
// It deliberately routes through mitm.New/CAKeyPair on the way in and
// mitm.New's seed on the way out — the two production ends — rather than
// comparing PEM bytes, because equal bytes would not prove that mitm.New
// accepts them.
func TestSaveLoadCA_RoundTripsTheSameTrustAnchor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "supervisors", "01ARZ3NDEKTSV4RRFFQ69G5FAV")

	original, err := mitm.New(mitm.Config{})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	certPEM, keyPEM, err := original.CAKeyPair()
	if err != nil {
		t.Fatalf("CAKeyPair: %v", err)
	}

	if err := statedir.SaveCA(dir, certPEM, keyPEM); err != nil {
		t.Fatalf("SaveCA: %v", err)
	}

	gotCert, gotKey, err := statedir.LoadCA(dir)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}

	seeded, err := mitm.New(mitm.Config{SeedCACertPEM: gotCert, SeedCAKeyPEM: gotKey})
	if err != nil {
		t.Fatalf("mitm.New with the loaded seed: %v — a persisted CA must always be a seed mitm.New accepts", err)
	}
	if seeded.CACert().SerialNumber.Cmp(original.CACert().SerialNumber) != 0 {
		t.Fatalf("recovered CA serial = %v, want %v — the replacement perimeter would sign with a CA the guest does not trust",
			seeded.CACert().SerialNumber, original.CACert().SerialNumber)
	}
}

// TestSaveCA_Overwrites proves a second Save replaces the first cleanly (the
// perimeter writes on every start, including after an adopt that seeded an
// existing CA).
func TestSaveCA_Overwrites(t *testing.T) {
	dir := t.TempDir()
	first, _ := mitm.New(mitm.Config{})
	second, _ := mitm.New(mitm.Config{})

	for _, p := range []*mitm.Proxy{first, second} {
		c, k, err := p.CAKeyPair()
		if err != nil {
			t.Fatalf("CAKeyPair: %v", err)
		}
		if err := statedir.SaveCA(dir, c, k); err != nil {
			t.Fatalf("SaveCA: %v", err)
		}
	}

	gotCert, gotKey, err := statedir.LoadCA(dir)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	seeded, err := mitm.New(mitm.Config{SeedCACertPEM: gotCert, SeedCAKeyPEM: gotKey})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	if seeded.CACert().SerialNumber.Cmp(second.CACert().SerialNumber) != 0 {
		t.Fatalf("LoadCA returned the FIRST CA after an overwrite; the state dir must hold the current anchor")
	}
	// And exactly one file remains: no temp litter to confuse a later reader.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != statedir.CAFileName {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("state dir holds %v, want just %s — a leftover temp file is a second copy of a private key", names, statedir.CAFileName)
	}
}

// ── 2. permissions ───────────────────────────────────────────────────────────

// TestSaveCA_FileIs0600InA0700Dir pins the boundary D-HSH-18 named as the real
// one, given the file is deliberately NOT encrypted: owner-only file inside an
// owner-only directory.
func TestSaveCA_FileIs0600InA0700Dir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "supervisors", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	p, _ := mitm.New(mitm.Config{})
	c, k, err := p.CAKeyPair()
	if err != nil {
		t.Fatalf("CAKeyPair: %v", err)
	}
	if err := statedir.SaveCA(dir, c, k); err != nil {
		t.Fatalf("SaveCA: %v", err)
	}

	fi, err := os.Stat(statedir.CAPath(dir))
	if err != nil {
		t.Fatalf("stat CA file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("CA file mode = %04o, want 0600 — this file holds an unencrypted private key", got)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("state dir mode = %04o, want 0700", got)
	}
}

// TestSaveCA_TightensAPreExisting0755Dir covers the 641 directories already on
// the reference host: SaveCA must not drop a private key into one of them at
// its inherited 0755.
func TestSaveCA_TightensAPreExisting0755Dir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "supervisors", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // defeat umask
		t.Fatalf("chmod: %v", err)
	}
	p, _ := mitm.New(mitm.Config{})
	c, k, _ := p.CAKeyPair()
	if err := statedir.SaveCA(dir, c, k); err != nil {
		t.Fatalf("SaveCA: %v", err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Fatalf("SaveCA wrote a private key into a %04o directory", got)
	}
}

// ── 3. fail closed ───────────────────────────────────────────────────────────

// TestLoadCA_AbsentIsADistinctError — "never persisted" and "persisted then
// damaged" are different faults and the log line must be able to say which.
func TestLoadCA_AbsentIsADistinctError(t *testing.T) {
	_, _, err := statedir.LoadCA(t.TempDir())
	if !errors.Is(err, statedir.ErrCAAbsent) {
		t.Fatalf("LoadCA on an empty dir: err = %v, want ErrCAAbsent", err)
	}
}

// TestLoadCA_RejectsDamagedFiles walks the damage modes D-HSH-18 calls out.
// Each must produce an error naming the cause — never a partial seed handed on
// to mitm.New, and never a silent success.
func TestLoadCA_RejectsDamagedFiles(t *testing.T) {
	p, _ := mitm.New(mitm.Config{})
	certPEM, keyPEM, err := p.CAKeyPair()
	if err != nil {
		t.Fatalf("CAKeyPair: %v", err)
	}
	other, _ := mitm.New(mitm.Config{})
	_, otherKeyPEM, _ := other.CAKeyPair()

	good := append(append([]byte(nil), certPEM...), keyPEM...)

	cases := []struct {
		name    string
		content []byte
	}{
		{"cert without key", certPEM},
		{"key without cert", keyPEM},
		{"empty file", nil},
		{"garbage", []byte("this is not a PEM file at all\n")},
		{"truncated mid-key", good[:len(certPEM)+len(keyPEM)/2]},
		{"truncated mid-cert", certPEM[:len(certPEM)/2]},
		{"mismatched pair", append(append([]byte(nil), certPEM...), otherKeyPEM...)},
		{"two certs one key", append(append(append([]byte(nil), certPEM...), certPEM...), keyPEM...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(statedir.CAPath(dir), tc.content, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			gotCert, gotKey, err := statedir.LoadCA(dir)
			if err == nil {
				t.Fatalf("LoadCA accepted a %s file; the crash path would seed a half-trusted perimeter", tc.name)
			}
			if gotCert != nil || gotKey != nil {
				t.Fatalf("LoadCA returned material alongside an error (cert %d bytes, key %d bytes); a partial seed must never escape",
					len(gotCert), len(gotKey))
			}
		})
	}
}

// TestLoadCA_RejectsExpired — an expired anchor signs leaves nothing can
// validate, which presents as a network fault rather than as an expired CA.
// Minting fresh and reporting the loss is the honest outcome. This case also
// makes the shortened generateCA lifetime safe: an old sandbox recovers with a
// loud CALost instead of a silently dead perimeter.
func TestLoadCA_RejectsExpired(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := mustCAValidFor(t, -48*time.Hour, -24*time.Hour)
	if err := os.WriteFile(statedir.CAPath(dir), append(certPEM, keyPEM...), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := statedir.LoadCA(dir); err == nil {
		t.Fatal("LoadCA accepted an EXPIRED CA")
	} else if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("LoadCA error does not name expiry as the cause: %v", err)
	}
}

// TestSaveCA_RefusesPartialPair — a file LoadCA will always reject is worse
// than no file: it turns "no CA persisted" into "CA corrupt" and hides the
// caller bug that produced the empty half.
func TestSaveCA_RefusesPartialPair(t *testing.T) {
	p, _ := mitm.New(mitm.Config{})
	certPEM, keyPEM, _ := p.CAKeyPair()

	for _, tc := range []struct{ name string; c, k []byte }{
		{"no key", certPEM, nil},
		{"no cert", nil, keyPEM},
		{"neither", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := statedir.SaveCA(dir, tc.c, tc.k); err == nil {
				t.Fatal("SaveCA wrote a partial pair")
			}
			if _, err := os.Stat(statedir.CAPath(dir)); !os.IsNotExist(err) {
				t.Fatalf("SaveCA left a file behind after refusing (stat err = %v)", err)
			}
		})
	}
}

// mustCAValidFor mints a CA whose validity window is offset from now, so the
// expiry branch can be exercised without waiting or mocking the clock.
func mustCAValidFor(t *testing.T, notBefore, notAfter time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "expired test CA"},
		NotBefore:             now.Add(notBefore),
		NotAfter:              now.Add(notAfter),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
