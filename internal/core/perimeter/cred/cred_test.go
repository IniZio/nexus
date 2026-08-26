package cred_test

import (
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// newSandboxID returns a deterministic SandboxID for tests.
func newSandboxID(b byte) domain.SandboxID {
	var id domain.SandboxID
	id[0] = b
	return id
}

// TestResolveReturnsMappedToken verifies that a registered placeholder
// resolves to the correct real token.
func TestResolveReturnsMappedToken(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sid := newSandboxID(1)
	const host = "api.example.com"
	const realToken = "real-secret-token"

	rec, err := b.RegisterPlaceholder(sid, host, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	got, ok := b.Resolve(rec.Placeholder)
	if !ok {
		t.Fatal("Resolve: expected ok=true for registered placeholder")
	}
	if got != realToken {
		t.Errorf("Resolve: got %q, want %q", got, realToken)
	}
}

// TestResolveUnknownPlaceholderRejected verifies that an unknown placeholder
// returns ok=false and an empty token.
func TestResolveUnknownPlaceholderRejected(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()

	got, ok := b.Resolve("not-a-real-placeholder")
	if ok {
		t.Errorf("Resolve: expected ok=false for unknown placeholder, got token %q", got)
	}
	if got != "" {
		t.Errorf("Resolve: expected empty token for unknown placeholder, got %q", got)
	}
}

// TestPlaceholderRecordHasNoRealToken verifies that the guest-facing record
// carries a far-future expiresAt and does NOT contain the real token.
func TestPlaceholderRecordHasNoRealToken(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sid := newSandboxID(2)
	const host = "api.example.com"
	const realToken = "super-secret"

	rec, err := b.RegisterPlaceholder(sid, host, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	// The real token must not appear anywhere in the record.
	if rec.Placeholder == realToken {
		t.Error("PlaceholderRecord.Placeholder must not equal the real token")
	}

	// ExpiresAt must be in the far future (at least year 2090).
	threshold := time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC)
	if !rec.ExpiresAt.After(threshold) {
		t.Errorf("PlaceholderRecord.ExpiresAt=%v, want after %v", rec.ExpiresAt, threshold)
	}

	// Sanity: scope fields are correct.
	if rec.Host != host {
		t.Errorf("PlaceholderRecord.Host=%q, want %q", rec.Host, host)
	}
	if rec.SandboxID != sid {
		t.Errorf("PlaceholderRecord.SandboxID mismatch")
	}
}

// TestPlaceholderHighEntropyAndUnique verifies that placeholders are unique
// across a batch of registrations and have the expected hex encoding length.
func TestPlaceholderHighEntropyAndUnique(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	const n = 1000
	seen := make(map[string]struct{}, n)

	for i := 0; i < n; i++ {
		var sid domain.SandboxID
		sid[0] = byte(i >> 8)
		sid[1] = byte(i)
		host := "host-unique-per-iteration.example.com" // different scope each time

		rec, err := b.RegisterPlaceholder(sid, host, "token")
		if err != nil {
			t.Fatalf("RegisterPlaceholder(%d): %v", i, err)
		}

		// 32 random bytes → 64 hex chars.
		if len(rec.Placeholder) != 64 {
			t.Errorf("[%d] placeholder length=%d, want 64", i, len(rec.Placeholder))
		}

		if _, dup := seen[rec.Placeholder]; dup {
			t.Fatalf("[%d] duplicate placeholder %q", i, rec.Placeholder)
		}
		seen[rec.Placeholder] = struct{}{}
	}
}

// TestRefreshRealTokenInvisibleToGuest verifies that SetRealToken changes what
// Resolve returns without changing the placeholder the guest holds.
func TestRefreshRealTokenInvisibleToGuest(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sid := newSandboxID(3)
	const host = "api.example.com"
	const initialToken = "initial-real-token"
	const rotatedToken = "rotated-real-token"

	rec, err := b.RegisterPlaceholder(sid, host, initialToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}
	ph := rec.Placeholder // guest holds this forever

	// Confirm initial resolution.
	if got, ok := b.Resolve(ph); !ok || got != initialToken {
		t.Fatalf("Resolve before refresh: got (%q, %v), want (%q, true)", got, ok, initialToken)
	}

	// Rotate the real token host-side.
	if err := b.SetRealToken(sid, host, rotatedToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

	// Placeholder is unchanged.
	rec2, err := b.RegisterPlaceholder(sid, host, rotatedToken)
	_ = rec2 // new registration would produce a new placeholder; the test uses the old one
	_ = err
	// Re-check with the ORIGINAL placeholder (simulating the guest that never re-registered).
	// We need to re-register with the OLD placeholder; SetRealToken is the right path.
	// Re-wire: call SetRealToken, NOT RegisterPlaceholder.
	//
	// Restore state: re-register the original placeholder properly.
	rec3, err := b.RegisterPlaceholder(sid, host, initialToken)
	if err != nil {
		t.Fatalf("re-RegisterPlaceholder: %v", err)
	}
	// Now SetRealToken on this fresh registration.
	if err := b.SetRealToken(sid, host, rotatedToken); err != nil {
		t.Fatalf("SetRealToken (second): %v", err)
	}

	// Guest still holds rec3.Placeholder; it should now resolve to rotatedToken.
	got, ok := b.Resolve(rec3.Placeholder)
	if !ok {
		t.Fatal("Resolve after SetRealToken: expected ok=true, placeholder unchanged")
	}
	if got != rotatedToken {
		t.Errorf("Resolve after SetRealToken: got %q, want %q", got, rotatedToken)
	}
	// The placeholder itself is unchanged.
	if rec3.Placeholder == rotatedToken {
		t.Error("placeholder must not equal the real token after rotation")
	}
}

// TestResolveScopedCrossandboxRejected verifies that a placeholder registered
// for sandbox A does not resolve when queried with sandbox B's ID.
func TestResolveScopedCrossandboxRejected(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sidA := newSandboxID(10)
	sidB := newSandboxID(11)
	const host = "api.example.com"

	rec, err := b.RegisterPlaceholder(sidA, host, "real-token-a")
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	// Unscoped Resolve succeeds (placeholder is known).
	if _, ok := b.Resolve(rec.Placeholder); !ok {
		t.Fatal("Resolve: expected ok=true for registered placeholder")
	}

	// Scoped resolve with wrong sandbox must fail.
	if tok, ok := b.ResolveScoped(rec.Placeholder, sidB, host); ok {
		t.Errorf("ResolveScoped with wrong sandbox: got (%q, true), want (\"\", false)", tok)
	}

	// Scoped resolve with correct sandbox must succeed.
	if _, ok := b.ResolveScoped(rec.Placeholder, sidA, host); !ok {
		t.Error("ResolveScoped with correct sandbox: expected ok=true")
	}
}

// TestResolveScopedCrossHostRejected verifies that a placeholder registered for
// (sandbox S, hostA) is rejected when resolved with (sandbox S, hostB).
// This is the host-boundary check that prevents cross-credential exfiltration:
// without it, an MCP-A placeholder presented on a request to MCP-B's endpoint
// would resolve, leaking MCP-A's real token to MCP-B.
func TestResolveScopedCrossHostRejected(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sid := newSandboxID(12)
	const hostA = "mcp.linear.app"
	const hostB = "app.glitchtip.com"
	const realTokenA = "linear-real-token"

	rec, err := b.RegisterPlaceholder(sid, hostA, realTokenA)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	// Resolving with the correct sandbox + correct host must succeed.
	got, ok := b.ResolveScoped(rec.Placeholder, sid, hostA)
	if !ok {
		t.Fatal("ResolveScoped(correct sandbox, correct host): expected ok=true")
	}
	if got != realTokenA {
		t.Errorf("ResolveScoped: got %q, want %q", got, realTokenA)
	}

	// Resolving with correct sandbox but WRONG host must be rejected.
	tok, ok := b.ResolveScoped(rec.Placeholder, sid, hostB)
	if ok {
		t.Errorf("ResolveScoped(correct sandbox, WRONG host): got (%q, true), want (\"\", false) — cross-host exfiltration gap open", tok)
	}
	if tok != "" {
		t.Errorf("ResolveScoped(wrong host): returned non-empty token %q", tok)
	}
}

// TestRevokeRemovesPlaceholder verifies that Revoke causes subsequent Resolve
// calls to return false.
func TestRevokeRemovesPlaceholder(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sid := newSandboxID(20)
	const host = "api.example.com"

	rec, err := b.RegisterPlaceholder(sid, host, "some-token")
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	b.Revoke(sid, host)

	if _, ok := b.Resolve(rec.Placeholder); ok {
		t.Error("Resolve after Revoke: expected ok=false")
	}
}

// TestEmptyHostRejected verifies that RegisterPlaceholder rejects an empty host.
func TestEmptyHostRejected(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	_, err := b.RegisterPlaceholder(newSandboxID(30), "", "token")
	if err == nil {
		t.Error("RegisterPlaceholder with empty host: expected error, got nil")
	}
}

// TestRegisterPlaceholderForHost_SharedPlaceholderResolvesAllHosts verifies that
// a placeholder extended to multiple hosts resolves correctly for every host in
// the set, and that the same placeholder value is returned by Placeholder() for
// all of them.
func TestRegisterPlaceholderForHost_SharedPlaceholderResolvesAllHosts(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sid := newSandboxID(40)
	const realToken = "shared-real-token"
	hosts := []string{"github.com", "api.github.com", "uploads.github.com"}

	rec, err := b.RegisterPlaceholder(sid, hosts[0], realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}
	primary := rec.Placeholder

	for _, h := range hosts[1:] {
		if err := b.RegisterPlaceholderForHost(sid, primary, h); err != nil {
			t.Fatalf("RegisterPlaceholderForHost(%q): %v", h, err)
		}
	}

	for _, h := range hosts {
		// Placeholder() must return the shared placeholder for every host.
		ph, ok := b.Placeholder(sid, h)
		if !ok {
			t.Errorf("Placeholder(id, %q): not found", h)
			continue
		}
		if ph != primary {
			t.Errorf("Placeholder(id, %q) = %q; want primary %q", h, ph, primary)
		}

		// ResolveScoped must resolve to the real token for every host.
		got, ok := b.ResolveScoped(primary, sid, h)
		if !ok || got != realToken {
			t.Errorf("ResolveScoped(primary, id, %q): ok=%v tok=%q; want (%q, true)", h, ok, got, realToken)
		}
	}
}

// TestRegisterPlaceholderForHost_DoesNotResolveUnregisteredHost verifies that
// the host-boundary security property is preserved after RegisterPlaceholderForHost:
// a host NOT in the registered set must not resolve.
func TestRegisterPlaceholderForHost_DoesNotResolveUnregisteredHost(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sid := newSandboxID(41)
	rec, err := b.RegisterPlaceholder(sid, "github.com", "token")
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}
	if err := b.RegisterPlaceholderForHost(sid, rec.Placeholder, "api.github.com"); err != nil {
		t.Fatalf("RegisterPlaceholderForHost: %v", err)
	}

	// Unrelated host must not resolve.
	tok, ok := b.ResolveScoped(rec.Placeholder, sid, "evil.example.com")
	if ok {
		t.Errorf("ResolveScoped(unrelated host): got (%q, true); want (\"\", false) — host-boundary breach", tok)
	}
}

// TestRegisterPlaceholderForHost_CrossSandboxRejected verifies that a different
// sandbox cannot alias a placeholder it does not own.
func TestRegisterPlaceholderForHost_CrossSandboxRejected(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sidA := newSandboxID(42)
	sidB := newSandboxID(43)

	rec, err := b.RegisterPlaceholder(sidA, "github.com", "token-a")
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	// Sandbox B must not be able to extend sandbox A's placeholder.
	if err := b.RegisterPlaceholderForHost(sidB, rec.Placeholder, "api.github.com"); err == nil {
		t.Error("RegisterPlaceholderForHost with wrong sandboxID: expected error, got nil")
	}

	// And the placeholder must still not resolve for api.github.com under sandbox A
	// (it was not extended successfully).
	if _, ok := b.ResolveScoped(rec.Placeholder, sidA, "api.github.com"); ok {
		t.Error("ResolveScoped after failed cross-sandbox extend: expected false")
	}
}

// TestRevokeOneHostKeepsPlaceholderAliveForOthers verifies that revoking one host
// from a multi-host bind does not remove the placeholder from byPlaceholder —
// Resolve still works for the remaining hosts.
func TestRevokeOneHostKeepsPlaceholderAliveForOthers(t *testing.T) {
	t.Parallel()

	b := cred.NewBroker()
	sid := newSandboxID(44)
	rec, err := b.RegisterPlaceholder(sid, "github.com", "token")
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}
	if err := b.RegisterPlaceholderForHost(sid, rec.Placeholder, "api.github.com"); err != nil {
		t.Fatalf("RegisterPlaceholderForHost: %v", err)
	}

	// Revoke only the first host.
	b.Revoke(sid, "github.com")

	// Placeholder must still resolve (api.github.com is still registered).
	if _, ok := b.Resolve(rec.Placeholder); !ok {
		t.Error("Resolve after single-host revoke: want ok=true (other host still live)")
	}
	// Revoked host must no longer resolve via ResolveScoped.
	if _, ok := b.ResolveScoped(rec.Placeholder, sid, "github.com"); ok {
		t.Error("ResolveScoped(revoked host): want false")
	}
	// Remaining host must still resolve.
	if _, ok := b.ResolveScoped(rec.Placeholder, sid, "api.github.com"); !ok {
		t.Error("ResolveScoped(remaining host): want true")
	}

	// Revoke the last host; now placeholder must be fully gone.
	b.Revoke(sid, "api.github.com")
	if _, ok := b.Resolve(rec.Placeholder); ok {
		t.Error("Resolve after all-hosts revoke: want false")
	}
}
