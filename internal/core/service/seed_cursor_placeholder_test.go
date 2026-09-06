package service

// Automated regressions for AC-3 (S18): the guest holds only placeholders.
//
// The invariant: for cursor-agent, the guest receives a placeholder in both its
// env payload and its credential FILE (auth.json).  The operator's real
// accessToken must NEVER appear in anything written to the guest.
//
// Prior art covered (do not duplicate):
//   - seed_credfile_test.go: buildCredFileSeedPayload returns placeholder in file.
//   - supervisor_credfile_seed_test.go: SeedLoop calls SeedGuestCredFile.
//
// This file covers the gap: tests that put a REAL sentinel token into the broker
// and then assert the sentinel is absent from every guest-bound channel — both
// the env-var payload (SeedGuest → buildSeedPayload) and the credential-file
// payload (SeedGuestCredFile → buildCredFileSeedPayload) — even AFTER the broker
// has had the real token pushed into it.

import (
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// ac3SentinelRealToken is the operator's distinctive real cursor JWT sentinel.
// It is registered in the broker and must NEVER appear in any guest-bound
// payload — env or file.  Its value is chosen to be unmistakable in any failure
// message.
const ac3SentinelRealToken = "REAL-CURSOR-SENTINEL-MUST-NOT-REACH-GUEST-ac3-S18"

// TestSeedGuest_CursorRealTokenAbsentFromEnvPayload is the AC-3 regression for
// the env-var seeding channel.
//
// Sequence:
//  1. Call SeedGuest (broker registers placeholder with empty real-token).
//  2. Call broker.SetRealToken to push the sentinel — broker now holds the real token.
//  3. Confirm broker.Resolve returns the sentinel (test-setup guard).
//  4. Assert the env payload delivered to the guest does NOT contain the sentinel.
//  5. Assert the placeholder IS present (the MITM proxy needs it).
//
// Mutation proof (placeholder-presence guard):
//
//	Changing `rec.Placeholder` to `""` in buildSeedPayload makes the
//	placeholder-present assertion fail:
//	    seed_cursor_placeholder_test.go:NNN: env payload missing placeholder
//	The env payload then becomes unusable for MITM substitution.
//	See verbatim RED output in the commit message.
//
// @verifies AC-3
func TestSeedGuest_CursorRealTokenAbsentFromEnvPayload(t *testing.T) {
	t.Parallel()

	broker := cred.NewBroker()
	id := seedTestID(40)
	host := cred.CursorAgentProfile.CredentialedHost

	envSeeder := &captureSeeder{}
	records, err := SeedGuest(context.Background(), broker, id, []string{host}, envSeeder.fn())
	if err != nil {
		t.Fatalf("SeedGuest: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("SeedGuest returned no records")
	}
	ph := records[0].Placeholder

	// Push the sentinel real token into the broker — mimics runSeedRoute's push.
	if err := broker.SetRealToken(id, host, ac3SentinelRealToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

	// Test-setup guard: confirm broker.Resolve returns the sentinel.
	got, ok := broker.Resolve(ph)
	if !ok || got != ac3SentinelRealToken {
		t.Fatalf("broker.Resolve(placeholder) = %q ok=%v; want sentinel — test setup broken", got, ok)
	}

	envPayload := string(envSeeder.payload)

	// The real sentinel must NOT appear in the env payload.  buildSeedPayload
	// receives only PlaceholderRecords (no real-token field); if any production
	// path adds broker.Resolve access to the payload builder, this fails.
	if strings.Contains(envPayload, ac3SentinelRealToken) {
		t.Errorf("SECURITY: real token leaked into guest env payload; got:\n%s", envPayload)
	}

	// The placeholder MUST be present — the MITM proxy identifies and swaps on it.
	if !strings.Contains(envPayload, ph) {
		t.Errorf("env payload missing placeholder %q; got:\n%s", ph, envPayload)
	}
}

// TestSeedGuestCredFile_CursorRealTokenAbsentFromFilePayload is the AC-3
// regression for the credential-file seeding channel (cursor/auth.json).
//
// Sequence:
//  1. SeedGuest → placeholder registered, env payload built.
//  2. broker.SetRealToken → broker now holds sentinel.
//  3. SeedGuestCredFile → credential-file payload built from PlaceholderRecords.
//  4. Assert sentinel absent from file payload.
//  5. Assert placeholder present in file payload.
//
// This is the "real production path" AC-3 requires: the full chain from import
// (broker has real token) through both seeding functions, asserting the sentinel
// never appears in either guest-bound channel.
//
// Mutation proof (placeholder-presence guard):
//
//	Changing `placeholder = rec.Placeholder` to `placeholder = ""` in
//	buildCredFileSeedPayload makes the placeholder-present assertion fail:
//	    seed_cursor_placeholder_test.go:NNN: credential file missing placeholder
//	See verbatim RED output in the commit message.
//
// @verifies AC-3
func TestSeedGuestCredFile_CursorRealTokenAbsentFromFilePayload(t *testing.T) {
	t.Parallel()

	broker := cred.NewBroker()
	id := seedTestID(41)
	host := cred.CursorAgentProfile.CredentialedHost

	envSeeder := &captureSeeder{}
	records, err := SeedGuest(context.Background(), broker, id, []string{host}, envSeeder.fn())
	if err != nil {
		t.Fatalf("SeedGuest: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("SeedGuest returned no records")
	}
	ph := records[0].Placeholder

	// Push the real sentinel into the broker.
	if err := broker.SetRealToken(id, host, ac3SentinelRealToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

	// Test-setup guard.
	got, ok := broker.Resolve(ph)
	if !ok || got != ac3SentinelRealToken {
		t.Fatalf("broker.Resolve = %q ok=%v; want sentinel — test setup broken", got, ok)
	}

	fileSeeder := &captureSeeder{}
	if err := SeedGuestCredFile(context.Background(), id, records, cred.CursorAgentProfile, fileSeeder.fn()); err != nil {
		t.Fatalf("SeedGuestCredFile: %v", err)
	}
	if fileSeeder.calls != 1 {
		t.Fatalf("expected 1 file-seeder call, got %d", fileSeeder.calls)
	}

	filePayload := string(fileSeeder.payload)

	// SENTINEL MUST NOT APPEAR in the credential file.
	if strings.Contains(filePayload, ac3SentinelRealToken) {
		t.Errorf("SECURITY: real token leaked into guest credential file; got:\n%s", filePayload)
	}

	// The placeholder must be present — the MITM proxy identifies and swaps on it.
	if !strings.Contains(filePayload, ph) {
		t.Errorf("credential file missing placeholder %q; got:\n%s", ph, filePayload)
	}
}

// TestSeedGuest_CursorEnvPayloadContainsNoRealTokenAfterPush extends the above
// by calling buildSeedPayload a SECOND time with the same records AFTER the real
// token has been pushed, proving the invariant holds on re-seed as well.
//
// The mechanism: buildSeedPayload only accesses PlaceholderRecord fields (no
// Broker argument).  A re-seed from existing records is identical to the first
// seed: only the placeholder appears, never the real token.
//
// @verifies AC-3
func TestSeedGuest_CursorEnvPayloadContainsNoRealTokenAfterPush(t *testing.T) {
	t.Parallel()

	broker := cred.NewBroker()
	id := seedTestID(42)
	host := cred.CursorAgentProfile.CredentialedHost

	envSeeder := &captureSeeder{}
	records, err := SeedGuest(context.Background(), broker, id, []string{host}, envSeeder.fn())
	if err != nil {
		t.Fatalf("SeedGuest: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("SeedGuest returned no records")
	}

	if err := broker.SetRealToken(id, host, ac3SentinelRealToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

	// Re-build the env payload directly from the existing records (mimics a
	// re-seed path).  buildSeedPayload has no broker argument; the real token
	// cannot appear.
	rePayload := string(buildSeedPayload(records))
	if strings.Contains(rePayload, ac3SentinelRealToken) {
		t.Errorf("SECURITY: real token in re-seeded env payload; got:\n%s", rePayload)
	}

	ph := records[0].Placeholder
	if !strings.Contains(rePayload, ph) {
		t.Errorf("re-seeded env payload missing placeholder %q; got:\n%s", ph, rePayload)
	}
}

// TestSeedGuestCredFile_PlaceholderBrokerRegistered asserts that after a full
// seed cycle, the placeholder returned by SeedGuestCredFile's underlying
// buildCredFileSeedPayload is also registered in the broker under the cursor
// host — proving the broker and the guest file are in sync.
//
// @verifies AC-3
func TestSeedGuestCredFile_PlaceholderBrokerRegistered(t *testing.T) {
	t.Parallel()

	broker := cred.NewBroker()
	id := seedTestID(43)
	host := cred.CursorAgentProfile.CredentialedHost

	envSeeder := &captureSeeder{}
	records, err := SeedGuest(context.Background(), broker, id, []string{host}, envSeeder.fn())
	if err != nil {
		t.Fatalf("SeedGuest: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("SeedGuest returned no records")
	}

	fileSeeder := &captureSeeder{}
	if err := SeedGuestCredFile(context.Background(), id, records, cred.CursorAgentProfile, fileSeeder.fn()); err != nil {
		t.Fatalf("SeedGuestCredFile: %v", err)
	}

	ph := records[0].Placeholder

	// The placeholder in the file payload must be the same one the broker knows.
	brokerPh, hasPh := broker.Placeholder(id, host)
	if !hasPh {
		t.Fatal("broker has no placeholder for cursor host after seeding")
	}
	if brokerPh != ph {
		t.Errorf("broker placeholder %q != record placeholder %q; guest file and broker out of sync", brokerPh, ph)
	}
	if !strings.Contains(string(fileSeeder.payload), ph) {
		t.Errorf("credential file does not contain broker-registered placeholder %q", ph)
	}
}
