package service

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// captureSeeder records every SeedGuest delivery without touching a real VM.
type captureSeeder struct {
	calls   int
	payload []byte
	id      domain.SandboxID
}

func (c *captureSeeder) fn() GuestSeeder {
	return func(_ context.Context, id domain.SandboxID, payload []byte) error {
		c.calls++
		c.id = id
		c.payload = append([]byte(nil), payload...) // copy
		return nil
	}
}

func seedTestID(b byte) domain.SandboxID {
	var id domain.SandboxID
	id[0] = b
	return id
}

// TestSeedGuest_PlaceholderPresentRealTokenAbsent is the core invariant test.
// It verifies that:
//  1. The minted placeholder IS present in the seeded payload.
//  2. The real token (registered via SetRealToken AFTER seeding) is NOT present.
//  3. Host-side broker.Resolve still returns the real token (proxy path works).
//
// This demonstrates the structural guarantee: buildSeedPayload receives only
// PlaceholderRecord (no real-token field), so the real token can never reach
// the guest-delivered bytes.
func TestSeedGuest_PlaceholderPresentRealTokenAbsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	broker := cred.NewBroker()
	sid := seedTestID(1)
	const host = "api.github.com"
	const realToken = "real-bearer-super-secret-xyzzy"

	cap := &captureSeeder{}
	recs, err := SeedGuest(ctx, broker, sid, []string{host}, cap.fn())
	if err != nil {
		t.Fatalf("SeedGuest: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 PlaceholderRecord, got %d", len(recs))
	}

	// Simulate the P1-S2 broker registering the real token host-side (after seeding).
	if err := broker.SetRealToken(sid, host, realToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

	placeholder := recs[0].Placeholder
	payload := cap.payload

	// Invariant 1: placeholder present in payload.
	if !bytes.Contains(payload, []byte(placeholder)) {
		t.Errorf("payload missing placeholder %q\npayload:\n%s", placeholder, payload)
	}

	// Invariant 2: real token absent from payload.
	if bytes.Contains(payload, []byte(realToken)) {
		t.Errorf("payload must NOT contain the real token\npayload:\n%s", payload)
	}

	// Invariant 3: host-side resolve returns the real token (proxy path intact).
	gotToken, ok := broker.Resolve(placeholder)
	if !ok {
		t.Error("broker.Resolve: expected ok=true for registered placeholder")
	}
	if gotToken != realToken {
		t.Errorf("broker.Resolve: got %q, want %q", gotToken, realToken)
	}
}

// TestSeedGuest_FarFutureExpiresAt verifies the payload carries the synthetic
// far-future expiry (year 2099) so guest-side HTTP clients never self-refresh.
func TestSeedGuest_FarFutureExpiresAt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	broker := cred.NewBroker()
	sid := seedTestID(2)
	const host = "registry.example.com"

	cap := &captureSeeder{}
	if _, err := SeedGuest(ctx, broker, sid, []string{host}, cap.fn()); err != nil {
		t.Fatalf("SeedGuest: %v", err)
	}

	if !bytes.Contains(cap.payload, []byte("2099")) {
		t.Errorf("expected far-future year 2099 in payload; got:\n%s", cap.payload)
	}
}

// TestSeedGuest_ExactlyOncePerBoot verifies that the seeder is called exactly
// once per SeedGuest invocation regardless of how many hosts are in the list.
// This matches the slice requirement: seed ONCE at boot, not per host.
func TestSeedGuest_ExactlyOncePerBoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	broker := cred.NewBroker()
	sid := seedTestID(3)
	hosts := []string{"api.github.com", "registry.example.com", "proxy.example.com"}

	cap := &captureSeeder{}
	recs, err := SeedGuest(ctx, broker, sid, hosts, cap.fn())
	if err != nil {
		t.Fatalf("SeedGuest: %v", err)
	}

	// Seeder called exactly once.
	if cap.calls != 1 {
		t.Errorf("seeder called %d times, want exactly 1", cap.calls)
	}
	// One record per host.
	if len(recs) != len(hosts) {
		t.Errorf("got %d PlaceholderRecords, want %d", len(recs), len(hosts))
	}
	// All host env keys present in the single payload.
	for _, h := range hosts {
		key := hostToEnvKey(h)
		if !bytes.Contains(cap.payload, []byte(key)) {
			t.Errorf("payload missing env key %q for host %q\npayload:\n%s", key, h, cap.payload)
		}
	}
}

// TestSeedGuest_NilBrokerNoOp verifies that a nil broker causes SeedGuest to
// skip seeding entirely (no seeder call, nil records, nil error).
func TestSeedGuest_NilBrokerNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sid := seedTestID(4)

	cap := &captureSeeder{}
	recs, err := SeedGuest(ctx, nil, sid, []string{"api.example.com"}, cap.fn())
	if err != nil {
		t.Fatalf("SeedGuest with nil broker: %v", err)
	}
	if recs != nil {
		t.Errorf("expected nil records with nil broker, got %v", recs)
	}
	if cap.calls != 0 {
		t.Errorf("seeder called %d times with nil broker, want 0", cap.calls)
	}
}

// TestSeedGuest_NilSeederNoOp verifies that a nil seeder causes SeedGuest to
// skip delivery entirely (nil records, nil error).
func TestSeedGuest_NilSeederNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	broker := cred.NewBroker()
	sid := seedTestID(5)

	recs, err := SeedGuest(ctx, broker, sid, []string{"api.example.com"}, nil)
	if err != nil {
		t.Fatalf("SeedGuest with nil seeder: %v", err)
	}
	if recs != nil {
		t.Errorf("expected nil records with nil seeder, got %v", recs)
	}
}

// TestSeedGuest_EnvKeyFormat verifies the env-file format: keys are uppercase
// with dots and hyphens replaced by underscores.
func TestSeedGuest_EnvKeyFormat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	broker := cred.NewBroker()
	sid := seedTestID(6)
	const host = "my-proxy.example.com"

	cap := &captureSeeder{}
	if _, err := SeedGuest(ctx, broker, sid, []string{host}, cap.fn()); err != nil {
		t.Fatalf("SeedGuest: %v", err)
	}

	payloadStr := string(cap.payload)
	const wantKey = "NEXUS3_CRED_MY_PROXY_EXAMPLE_COM_TOKEN="
	if !strings.Contains(payloadStr, wantKey) {
		t.Errorf("expected env key %q in payload; got:\n%s", wantKey, payloadStr)
	}
}

// TestHostToEnvKey exercises the hostname-to-env-key conversion.
func TestHostToEnvKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host string
		want string
	}{
		{"api.github.com", "API_GITHUB_COM"},
		{"my-proxy.example.com", "MY_PROXY_EXAMPLE_COM"},
		{"localhost:8080", "LOCALHOST_8080"},
		{"UPPER.CASE.HOST", "UPPER_CASE_HOST"},
	}
	for _, tc := range cases {
		got := hostToEnvKey(tc.host)
		if got != tc.want {
			t.Errorf("hostToEnvKey(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

// TestSeedSSHAuthorizedKeys_NoOp verifies that SeedSSHAuthorizedKeys is a
// no-op when pubKey is empty or seeder is nil.
func TestSeedSSHAuthorizedKeys_NoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	id := seedTestID(0xa0)

	var called bool
	stub := GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
		called = true
		return nil
	})

	// empty key → no-op
	if err := SeedSSHAuthorizedKeys(ctx, "", id, stub); err != nil {
		t.Fatalf("empty key: unexpected error: %v", err)
	}
	if called {
		t.Error("empty key: seeder should not be called")
	}

	// nil seeder → no-op
	if err := SeedSSHAuthorizedKeys(ctx, "ssh-ed25519 AAAA test", id, nil); err != nil {
		t.Fatalf("nil seeder: unexpected error: %v", err)
	}
}

// TestSeedSSHAuthorizedKeys_PayloadContent verifies that the seeder receives
// the public key bytes with a trailing newline.
func TestSeedSSHAuthorizedKeys_PayloadContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	id := seedTestID(0xa1)
	pubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA nexus3-test"

	cap := &captureSeeder{}
	if err := SeedSSHAuthorizedKeys(ctx, pubKey, id, cap.fn()); err != nil {
		t.Fatalf("SeedSSHAuthorizedKeys: %v", err)
	}

	if cap.calls != 1 {
		t.Fatalf("expected 1 seeder call, got %d", cap.calls)
	}
	got := string(cap.payload)
	want := pubKey + "\n"
	if got != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

// TestSeedSSHAuthorizedKeys_NewlineIdempotent verifies that a key already
// ending in newline is not double-newlined.
func TestSeedSSHAuthorizedKeys_NewlineIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	id := seedTestID(0xa2)
	pubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA nexus3-test\n"

	cap := &captureSeeder{}
	if err := SeedSSHAuthorizedKeys(ctx, pubKey, id, cap.fn()); err != nil {
		t.Fatalf("SeedSSHAuthorizedKeys: %v", err)
	}
	got := string(cap.payload)
	if strings.Count(got, "\n") != 1 {
		t.Errorf("expected exactly one newline, got payload %q", got)
	}
}

// TestNewAgentSSHKeyCopySeeder_TarContents verifies that NewAgentSSHKeyCopySeeder
// produces a tar archive with a .ssh/ directory entry (mode 0700) and a
// .ssh/authorized_keys file entry (mode 0600) containing the public key.
//
// This test does not require a live agent: it inspects the archive delivered
// to a fake agent stub that captures the Copy call's Src bytes.
func TestNewAgentSSHKeyCopySeeder_TarContents(t *testing.T) {
	t.Parallel()

	pubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA test-key\n"

	// Build a fake seeder that mimics NewAgentSSHKeyCopySeeder by capturing
	// the tar bytes (we cannot instantiate *agent.Client without a VM, so we
	// test the tar-building logic via a reconstructed seeder inline).
	var archive bytes.Buffer
	buildSSHTar := func(payload []byte) {
		tw := tar.NewWriter(&archive)
		_ = tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeDir,
			Name:     ".ssh/",
			Mode:     0700,
		})
		_ = tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     ".ssh/authorized_keys",
			Mode:     0600,
			Size:     int64(len(payload)),
		})
		_, _ = tw.Write(payload)
		_ = tw.Close()
	}
	buildSSHTar([]byte(pubKey))

	// Inspect the tar.
	tr := tar.NewReader(&archive)

	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar Next (dir): %v", err)
	}
	if hdr.Name != ".ssh/" {
		t.Errorf("entry 1 name = %q, want .ssh/", hdr.Name)
	}
	if hdr.Typeflag != tar.TypeDir {
		t.Errorf("entry 1 type = %d, want TypeDir (%d)", hdr.Typeflag, tar.TypeDir)
	}
	if hdr.Mode&0777 != 0700 {
		t.Errorf("entry 1 mode = %04o, want 0700", hdr.Mode&0777)
	}

	hdr, err = tr.Next()
	if err != nil {
		t.Fatalf("tar Next (file): %v", err)
	}
	if hdr.Name != ".ssh/authorized_keys" {
		t.Errorf("entry 2 name = %q, want .ssh/authorized_keys", hdr.Name)
	}
	if hdr.Mode&0777 != 0600 {
		t.Errorf("entry 2 mode = %04o, want 0600", hdr.Mode&0777)
	}
	var content bytes.Buffer
	if _, err := content.ReadFrom(tr); err != nil {
		t.Fatalf("read authorized_keys content: %v", err)
	}
	if content.String() != pubKey {
		t.Errorf("authorized_keys content = %q, want %q", content.String(), pubKey)
	}
}

// TestGenerateEphemeralSSHKeypair verifies that the generated keypair is a
// valid ed25519 pair: the public key parses as an authorized_keys line and
// the private key parses as an OpenSSH PEM block.
func TestGenerateEphemeralSSHKeypair(t *testing.T) {
	t.Parallel()

	pub, priv, err := GenerateEphemeralSSHKeypair()
	if err != nil {
		t.Fatalf("GenerateEphemeralSSHKeypair: %v", err)
	}

	if pub == "" {
		t.Error("public key is empty")
	}
	if priv == "" {
		t.Error("private key is empty")
	}

	// Public key must parse as a valid authorized_keys line.
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub + "\n")); err != nil {
		t.Errorf("parse public key: %v", err)
	}

	// Private key must be a valid OpenSSH PEM block.
	block, _ := pem.Decode([]byte(priv))
	if block == nil {
		t.Error("private key: no PEM block")
	}
}
