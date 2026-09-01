package perimeter

// has_mitm_proxy_test.go pins the property that makes HasMITMProxy safe to use
// as the handoff's "CA material is mandatory" predicate: it is true whenever a
// proxy exists, INCLUDING when that proxy's CA cannot be encoded.
//
// The trap this guards (motive nexus3-host-supervisor-hotswap, ticket 14):
// PerimeterSupervisor.CAKeyPair returns a non-nil error in three distinct
// situations, and only ONE of them means "no MITM proxy":
//
//	1. s.proxy == nil                        -> genuinely no proxy (AllowAll)
//	2. CA private key is not *ecdsa.PrivateKey (mitm/proxy.go) -> PROXY EXISTS
//	3. x509.MarshalECPrivateKey fails          (mitm/proxy.go) -> PROXY EXISTS
//
// Deriving the predicate from `CAKeyPair() == nil` collapses 2 and 3 into 1,
// which would drop the CA requirement for a sandbox that is actively
// intercepting TLS and let an empty-CA handoff payload through. These tests
// assert the predicate does NOT track CAKeyPair's error.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
)

// rsaCAPEM mints a self-signed CA whose private key is *rsa.PrivateKey, not
// *ecdsa.PrivateKey. Seeding a mitm.Proxy with it drives case 2 above through
// the real production constructor (mitm.New's SeedCACertPEM/SeedCAKeyPEM path,
// the same seam the adopt path uses) rather than by poking unexported fields.
func rsaCAPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "nexus3 test rsa CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

// TestHasMITMProxy_NoProxy is case 1: AllowAll mode. The predicate must be
// false so an AllowAll sandbox can hand off with an empty Payload.CA.
func TestHasMITMProxy_NoProxy(t *testing.T) {
	sup := &PerimeterSupervisor{al: newTestAllowList(t)}

	if sup.HasMITMProxy() {
		t.Error("HasMITMProxy() = true with proxy == nil; want false — " +
			"an AllowAll sandbox has no CA to hand off and must not be forced to carry one")
	}
	if _, _, err := sup.CAKeyPair(); err == nil {
		t.Fatal("CAKeyPair() error = nil with proxy == nil; test fixture is wrong")
	}
}

// TestHasMITMProxy_WorkingProxy is the ordinary MITM case: proxy present, CA
// encodable. Predicate true, CA material available.
func TestHasMITMProxy_WorkingProxy(t *testing.T) {
	sup := &PerimeterSupervisor{proxy: newTestProxy(t), al: newTestAllowList(t)}

	if !sup.HasMITMProxy() {
		t.Error("HasMITMProxy() = false with a live proxy; want true")
	}
	certPEM, keyPEM, err := sup.CAKeyPair()
	if err != nil {
		t.Fatalf("CAKeyPair() on a working proxy: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatalf("CAKeyPair() returned empty material: cert=%d key=%d bytes", len(certPEM), len(keyPEM))
	}
}

// TestHasMITMProxy_ProxyWithUnusableCA is the security case (AC-14b): a proxy
// EXISTS but its CA key is not ECDSA, so CAKeyPair fails. The predicate must
// still be true — this is precisely the divergence a `CAKeyPair() == nil`
// probe would get backwards.
func TestHasMITMProxy_ProxyWithUnusableCA(t *testing.T) {
	certPEM, keyPEM := rsaCAPEM(t)
	proxy, err := mitm.New(mitm.Config{SeedCACertPEM: certPEM, SeedCAKeyPEM: keyPEM})
	if err != nil {
		t.Fatalf("mitm.New with RSA-seeded CA: %v", err)
	}
	sup := &PerimeterSupervisor{proxy: proxy, al: newTestAllowList(t)}

	// Fixture precondition: this proxy must genuinely fail to yield a keypair,
	// otherwise the test below proves nothing.
	if _, _, caErr := sup.CAKeyPair(); caErr == nil {
		t.Fatal("CAKeyPair() error = nil for an RSA-seeded CA; the fixture no longer drives " +
			"the non-ECDSA branch of mitm.Proxy.CAKeyPair — fix the fixture before trusting this test")
	}

	if !sup.HasMITMProxy() {
		t.Fatal("HasMITMProxy() = false for a proxy whose CA is unusable; want true. " +
			"A proxy IS intercepting TLS here — reporting false drops the handoff's CA " +
			"requirement and converts a correct refusal into a wrong acceptance.")
	}
}
