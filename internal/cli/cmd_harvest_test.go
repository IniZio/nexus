package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// newHarvestSvc builds a service backed by a FileStore pre-populated with one
// or more sandboxes tagged to motiveID. The fake driver is configured with a
// DialGuest error so that every copy attempt fails immediately — no real agent
// or KVM required. This lets us exercise the partial-failure code path without
// blocking on gRPC handshake timeouts.
func newHarvestSvc(t *testing.T, motiveID string, count int) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < count; i++ {
		sb := domain.Sandbox{
			ID:      domain.NewSandboxID(),
			Name:    "w",
			Project: "test",
			State:   domain.Created,
			Labels:  map[string]string{"motive": motiveID},
		}
		if err := st.Create(ctx, sb); err != nil {
			t.Fatalf("store.Create sandbox %d: %v", i, err)
		}
	}
	fd := fake.New()
	// Inject a DialGuest error so copyOneForHarvest fails immediately without
	// any blocking gRPC handshake attempt.
	fd.SetDialGuestError(errDialGuestInjected)
	return service.New(st, fd, lifecycle.New())
}

// errDialGuestInjected is the sentinel injected into the fake driver.
var errDialGuestInjected = errors.New("dial guest: injected test failure")

// ── flag parsing / usage errors ───────────────────────────────────────────────

func TestHarvest_UsageError_NoArgs(t *testing.T) {
	out, _, _ := capture(false)
	err := runHarvest(context.Background(), []string{}, out)
	if err == nil {
		t.Fatal("expected UsageError, got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

func TestHarvest_UsageError_TooFewArgs(t *testing.T) {
	out, _, _ := capture(false)
	err := runHarvest(context.Background(), []string{"motive-x", "/guest/path"}, out)
	if err == nil {
		t.Fatal("expected UsageError, got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

func TestHarvest_UsageError_TooManyArgs(t *testing.T) {
	out, _, _ := capture(false)
	err := runHarvest(context.Background(), []string{"motive-x", "/guest/path", "/host/dest", "extra"}, out)
	if err == nil {
		t.Fatal("expected UsageError, got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

func TestHarvest_UsageError_UnknownFlag(t *testing.T) {
	out, _, _ := capture(false)
	err := runHarvest(context.Background(), []string{"--unknown-flag"}, out)
	if err == nil {
		t.Fatal("expected UsageError, got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

// ── empty motive (no sandboxes) ───────────────────────────────────────────────

// TestHarvestWith_EmptyMotive verifies that an unknown motive ID results in a
// clean success with 0 outcomes (the store returns an empty slice, not an error).
func TestHarvestWith_EmptyMotive(t *testing.T) {
	svc := newTestService(t)

	out, stdout, _ := capture(true)
	err := runHarvestWith(context.Background(), "no-such-motive", "/guest/path", t.TempDir(), out, svc)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	var env struct {
		SchemaVersion int             `json:"schema_version"`
		Kind          string          `json:"kind"`
		Data          harvestDoneJSON `json:"data"`
	}
	decodeOne(t, stdout, &env)
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: got %d, want 1", env.SchemaVersion)
	}
	if env.Kind != "harvest.done" {
		t.Errorf("kind: got %q, want %q", env.Kind, "harvest.done")
	}
	if env.Data.MotiveID != "no-such-motive" {
		t.Errorf("motive_id: got %q, want %q", env.Data.MotiveID, "no-such-motive")
	}
	if len(env.Data.Outcomes) != 0 {
		t.Errorf("outcomes: got %d, want 0", len(env.Data.Outcomes))
	}
}

// ── partial failure ───────────────────────────────────────────────────────────

// TestHarvestWith_PartialFailure verifies that when HarvestMotive cannot pull
// from any sandbox (agent unreachable because there is no real VM), the CLI:
//   - emits a harvest.done event listing each sandbox's error
//   - returns a *CodedError with code "harvest_partial_failure"
//   - exits non-zero (verified via the CodedError, which root.go maps to exit 1)
//
// This test does NOT require KVM. The fake driver implements GuestDialer but
// there is no gRPC server on the guest side of the in-memory pipe, so the
// Copy RPC fails — producing the partial failure the CLI must surface.
func TestHarvestWith_PartialFailure(t *testing.T) {
	const motiveID = "motive-partial"
	svc := newHarvestSvc(t, motiveID, 2)
	hostDest := t.TempDir()

	out, stdout, _ := capture(true)
	err := runHarvestWith(context.Background(), motiveID, "/guest/result.txt", hostDest, out, svc)
	if err == nil {
		t.Fatal("expected non-nil error for partial failure, got nil")
	}

	// Error must be a *CodedError with the harvest_partial_failure code.
	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if coded.Code != harvestErrCodePartialFailure {
		t.Errorf("error code: got %q, want %q", coded.Code, harvestErrCodePartialFailure)
	}
	if !strings.Contains(coded.Msg, motiveID) {
		t.Errorf("error message does not mention motive ID %q: %q", motiveID, coded.Msg)
	}

	// harvest.done must still be emitted even on partial failure so the caller
	// can see per-sandbox outcomes (decision D-DC-11 write-back honesty).
	var env struct {
		SchemaVersion int             `json:"schema_version"`
		Kind          string          `json:"kind"`
		Data          harvestDoneJSON `json:"data"`
	}

	// Use a json.Decoder directly: on partial failure root.go also emits an
	// error envelope, so stdout has two JSON objects. Decode only the first.
	dec := json.NewDecoder(stdout)
	if decErr := dec.Decode(&env); decErr != nil {
		t.Fatalf("decode harvest.done: %v", decErr)
	}

	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: got %d, want 1", env.SchemaVersion)
	}
	if env.Kind != "harvest.done" {
		t.Errorf("kind: got %q, want %q", env.Kind, "harvest.done")
	}
	if env.Data.MotiveID != motiveID {
		t.Errorf("motive_id: got %q, want %q", env.Data.MotiveID, motiveID)
	}
	if len(env.Data.Outcomes) != 2 {
		t.Errorf("outcomes: got %d, want 2", len(env.Data.Outcomes))
	}
	for _, o := range env.Data.Outcomes {
		if o.SandboxID == "" {
			t.Error("outcome.sandbox_id is empty")
		}
		if o.Error == "" {
			t.Errorf("sandbox %s: expected error field, got empty (should have failed)", o.SandboxID)
		}
		if o.HostPath != "" {
			t.Errorf("sandbox %s: expected empty host_path on failure, got %q", o.SandboxID, o.HostPath)
		}
	}
}

// TestHarvestWith_PartialFailure_NonZeroExit verifies that the CodedError
// returned by runHarvestWith causes Run() in root.go to produce exit code 1.
// We simulate this by checking that the error is not a UsageError (which would
// produce exit 2) and is not an ExitCodeError, so root.go takes the default
// branch: EmitError + return 1.
func TestHarvestWith_PartialFailure_NonZeroExit(t *testing.T) {
	const motiveID = "motive-exit-code"
	svc := newHarvestSvc(t, motiveID, 1)

	out, _, _ := capture(false)
	err := runHarvestWith(context.Background(), motiveID, "/guest/out.txt", t.TempDir(), out, svc)
	if err == nil {
		t.Fatal("expected non-nil error, got nil")
	}
	var ue *UsageError
	if errors.As(err, &ue) {
		t.Error("error must not be a *UsageError (would exit 2, not 1)")
	}
	var ec *ExitCodeError
	if errors.As(err, &ec) {
		t.Error("error must not be an *ExitCodeError (must go through the default CodedError branch)")
	}
	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Errorf("expected *CodedError, got %T", err)
	}
}
