package supervisor

// handoff_runtime_predicate_test.go is the end-to-end proof for ticket 14
// (motive nexus3-host-supervisor-hotswap): performHandoff's hasMITMProxy
// argument is derived from the LIVE supervisor, and the derivation fails
// CLOSED when a proxy exists whose CA cannot be encoded.
//
// These tests do NOT hand-roll the boolean, and they do not re-implement the
// call sites' wiring either. They construct a real
// *perimeter.PerimeterSupervisor and call handoffFromLiveSupervisor — the
// exact function both production call sites (supervisor.go's RunDetached and
// serve_adopted.go's serveAdoptedSupervisor) reduce to. So a regression at the
// predicate, at the builder, or at Validate all surface here; a test that
// re-derived the predicate itself would leave the production expression
// unmutated and prove nothing about it.
//
// The branch that matters is TestHandoffPredicate_ProxyWithUnusableCA_Refuses:
// mitm.Proxy.CAKeyPair errors for THREE reasons and only one of them means
// "no proxy". A predicate written as `_, _, err := sup.CAKeyPair(); err == nil`
// compiles, passes the other two tests in this file, and silently lets a
// TLS-intercepting sandbox hand off with no CA material. That mutation must
// turn this test RED.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/supervisor/handoff"
)

// rsaSeededCAPEM mints a self-signed CA backed by an *rsa.PrivateKey. Feeding
// it to mitm.New's seed path produces a proxy that EXISTS and is serving, but
// whose CAKeyPair fails the *ecdsa.PrivateKey type assertion — the exact
// "proxy present, CA unusable" state this ticket exists to keep fail-closed.
func rsaSeededCAPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "nexus3 handoff test rsa CA"},
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

// startTestPerimeter brings up a real PerimeterSupervisor over a net.Pipe with
// the given proxy (nil = AllowAll mode, no proxy).
func startTestPerimeter(t *testing.T, proxy *mitm.Proxy) *perimeter.PerimeterSupervisor {
	t.Helper()
	guestConn, hostConn := net.Pipe()
	t.Cleanup(func() { hostConn.Close() })

	al, err := netfilter.NewAllowList(nil, nil, nil)
	if err != nil {
		t.Fatalf("netfilter.NewAllowList: %v", err)
	}
	sup, err := perimeter.Start(context.Background(), domain.SandboxID{},
		guestConn, &perimeter.NoOpPerimeter{}, proxy, al)
	if err != nil {
		t.Fatalf("perimeter.Start: %v", err)
	}
	t.Cleanup(func() { sup.Close() })
	return sup
}

// offerWatcher returns a peer socket path plus a channel that receives the
// payload the replacement actually saw. Nothing arrives on that channel when
// performHandoff refuses before the wire — which is the assertion that
// separates "refused" from "sent an empty-CA payload and got lucky".
func offerWatcher(t *testing.T) (peerPath string, offered <-chan handoff.Payload) {
	t.Helper()
	path, acceptedCh := listenHandoffPeer(t)
	ch := make(chan handoff.Payload, 1)
	go func() {
		peerConn, chOK := <-acceptedCh
		if !chOK {
			return
		}
		defer peerConn.Close()
		p, fd, err := handoff.Accept(peerConn)
		if err != nil {
			return
		}
		if fd != nil {
			fd.Close()
		}
		ch <- p
		// Refuse at the handoff level so the caller's ok is false either way;
		// this test discriminates on whether the payload reached the wire, not
		// on the peer's verdict.
		_ = handoff.Refuse(peerConn, "test peer: always refuses")
	}()
	return path, ch
}

// TestHandoffPredicate_ProxyWithUnusableCA_Refuses is AC-14b. A proxy IS
// running; its CA simply cannot be marshalled. The runtime predicate must
// report true, the payload's CA comes out empty, and Validate must refuse
// BEFORE anything is offered to the replacement.
func TestHandoffPredicate_ProxyWithUnusableCA_Refuses(t *testing.T) {
	certPEM, keyPEM := rsaSeededCAPEM(t)
	proxy, err := mitm.New(mitm.Config{SeedCACertPEM: certPEM, SeedCAKeyPEM: keyPEM})
	if err != nil {
		t.Fatalf("mitm.New with RSA-seeded CA: %v", err)
	}
	sup := startTestPerimeter(t, proxy)

	// Fixture preconditions. If either of these stops holding, the test below
	// is vacuous, so fail loudly rather than reporting a green.
	if _, _, caErr := sup.CAKeyPair(); caErr == nil {
		t.Fatal("fixture: CAKeyPair() succeeded on an RSA-seeded CA; this no longer drives " +
			"the 'proxy exists, CA unusable' branch")
	}
	payload, fdFile, buildErr := buildHandoffPayload(sup, "sbx", 1, 512)
	if buildErr != nil {
		t.Fatalf("buildHandoffPayload: %v", buildErr)
	}
	if fdFile != nil {
		fdFile.Close()
	}
	if len(payload.CA.CertPEM) != 0 || len(payload.CA.KeyPEM) != 0 {
		t.Fatalf("fixture: builder produced CA material (cert=%d key=%d) for an unusable CA",
			len(payload.CA.CertPEM), len(payload.CA.KeyPEM))
	}

	peerPath, offered := offerWatcher(t)
	// This is the production call, the same one both call sites make.
	ok, reason, err := handoffFromLiveSupervisor(context.Background(), peerPath, sup, "sbx", 1, 512)

	if err != nil {
		t.Fatalf("performHandoff: unexpected error: %v", err)
	}
	if ok {
		t.Fatal("performHandoff: ok = true for a supervisor running a MITM proxy with an " +
			"unusable CA; want false. The replacement would inherit the perimeter without the " +
			"CA the guest has already pinned.")
	}
	if reason == "" {
		t.Error("performHandoff: refusal reason is empty; the refusal must name the missing CA")
	}
	select {
	case p := <-offered:
		t.Fatalf("performHandoff offered a payload to the replacement (CA cert=%d key=%d bytes) "+
			"instead of refusing — hasMITMProxy was derived as false for a supervisor that HAS "+
			"a proxy, which is the exact regression this test guards",
			len(p.CA.CertPEM), len(p.CA.KeyPEM))
	case <-time.After(500 * time.Millisecond):
		// Nothing reached the wire: correct.
	}
}

// TestHandoffPredicate_AllowAllNoProxy_ReachesWire is AC-14c: genuinely no
// proxy, so no CA is required and the handoff must proceed with an empty CA.
func TestHandoffPredicate_AllowAllNoProxy_ReachesWire(t *testing.T) {
	sup := startTestPerimeter(t, nil)

	if sup.HasMITMProxy() {
		t.Fatal("fixture: HasMITMProxy() = true for a supervisor started with proxy == nil")
	}

	peerPath, offered := offerWatcher(t)
	ok, _, err := handoffFromLiveSupervisor(context.Background(), peerPath, sup, "sbx", 1, 512)
	if err != nil {
		t.Fatalf("performHandoff: unexpected error: %v", err)
	}
	// The peer always refuses, so ok is false — but for the peer's reason, not
	// a validation short-circuit. The wire assertion below is the real one.
	if ok {
		t.Fatal("performHandoff: ok = true despite the test peer refusing; fixture is wrong")
	}
	select {
	case p := <-offered:
		if len(p.CA.CertPEM) != 0 || len(p.CA.KeyPEM) != 0 {
			t.Errorf("AllowAll payload carried CA material: cert=%d key=%d bytes",
				len(p.CA.CertPEM), len(p.CA.KeyPEM))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no payload reached the replacement — an AllowAll sandbox with no proxy and no " +
			"CA must still hand off, but Validate refused it")
	}
}

// TestHandoffPredicate_WorkingProxy_ReachesWireWithCA is the ordinary MITM
// path: proxy present, CA encodable, payload carries it, handoff proceeds.
func TestHandoffPredicate_WorkingProxy_ReachesWireWithCA(t *testing.T) {
	proxy, err := mitm.New(mitm.Config{})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	sup := startTestPerimeter(t, proxy)

	if !sup.HasMITMProxy() {
		t.Fatal("fixture: HasMITMProxy() = false for a supervisor started with a live proxy")
	}

	peerPath, offered := offerWatcher(t)
	ok, reason, err := handoffFromLiveSupervisor(context.Background(), peerPath, sup, "sbx", 1, 512)
	if err != nil {
		t.Fatalf("performHandoff: unexpected error: %v", err)
	}
	if ok {
		t.Fatal("performHandoff: ok = true despite the test peer refusing; fixture is wrong")
	}
	select {
	case p := <-offered:
		if len(p.CA.CertPEM) == 0 || len(p.CA.KeyPEM) == 0 {
			t.Errorf("MITM payload reached the wire without CA material: cert=%d key=%d bytes",
				len(p.CA.CertPEM), len(p.CA.KeyPEM))
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no payload reached the replacement for a working MITM proxy; "+
			"performHandoff refused a complete payload (reason=%q)", reason)
	}
}
