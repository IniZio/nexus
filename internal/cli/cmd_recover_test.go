package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/recovery"
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// newAdoptableTestFixture builds a store + fake driver with one sandbox
// recorded as Running with a SupervisorPID that is a real, now-exited OS
// process (so supervisor.CheckAndReconcile's real liveness probe — not a
// fake — genuinely finds it dead). Shared by the JSON- and human-mode
// production-wiring tests below.
func newAdoptableTestFixture(t *testing.T) (store.Store, driver.Driver, domain.Sandbox) {
	t.Helper()
	ctx := context.Background()

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := fake.New()

	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "prod-wiring",
		Project: "ac8",
		State:   domain.Running,
	}

	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn short-lived process: %v", err)
	}
	sb.SupervisorPID = cmd.Process.Pid
	sb.SupervisorSock = ""

	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create sandbox: %v", err)
	}
	drv.SetRunning(sb.ID)

	return st, drv, sb
}

// TestRunRecoverWith_ProductionWiring_SurfacesAdoptable calls the REAL
// production entry point (runRecoverWith, the function runRecover delegates
// to after resolving the real substrate and store root) and asserts it
// classifies a live-VM/dead-supervisor sandbox as adoptable.
//
// This is deliberately NOT a test of recovery.Recoverer in isolation (that is
// internal/core/recovery/recover_adoptable_test.go's job) and it deliberately
// does NOT call WithSupervisorCheck itself. It exists to catch the exact
// defect class this motive has shipped before — Payload.CA went unpopulated
// for the whole motive because every test built its own payload instead of
// invoking the real builder. Here: if a future edit removes
// ".WithSupervisorCheck(supervisor.CheckAndReconcile)" from runRecoverWith
// (the production wiring), this test must fail — not because it asserts the
// call exists in source, but because it asserts the STDOUT the real function
// produces changes: a dead supervisor stops being reported as adoptable and
// reads as plain "adopted" instead, exactly the silent regression AC-8 exists
// to prevent.
func TestRunRecoverWith_ProductionWiring_SurfacesAdoptable(t *testing.T) {
	ctx := context.Background()
	st, drv, sb := newAdoptableTestFixture(t)

	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, true) // JSON mode

	if err := runRecoverWith(ctx, st, drv, out); err != nil {
		t.Fatalf("runRecoverWith: %v", err)
	}

	var envelope struct {
		Data recovery.Report `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("decode output %s: %v", buf.String(), err)
	}

	var found *recovery.SandboxOutcome
	for i := range envelope.Data.Outcomes {
		if envelope.Data.Outcomes[i].ID == sb.ID {
			found = &envelope.Data.Outcomes[i]
		}
	}
	if found == nil {
		t.Fatalf("no outcome for sandbox %s in output %s", sb.ID, buf.String())
	}
	if found.Kind != recovery.OutcomeAdoptable {
		t.Fatalf("production runRecoverWith did not surface the dead supervisor: want %s, got %s (reason: %q) — "+
			"the supervisor-liveness cross-check is not wired, or is not reaching this path",
			recovery.OutcomeAdoptable, found.Kind, found.Reason)
	}
}

// TestRunRecoverWith_HumanMode_ReportsAdoptable is AC-8's actual acceptance
// bar: "reported as needing adoption rather than as plainly running" — on
// the surface an operator actually sees. `nexus3 recover` defaults to human
// mode (--json is opt-in), and EmitSuccess's human-mode branch
// (output.go:88-99) prints only the bare "recovery complete: examined N
// sandbox(es)" summary — the exact symptom line quoted in the ticket for the
// 25-hour-stale sandbox this ticket exists to fix. A correct classification
// that only ever reaches --json output does not meet AC-8: it is invisible
// to a person running the plain command.
//
// This asserts on human-mode stdout, not the JSON envelope: the sandbox id
// and the literal string "adoptable" must both appear.
func TestRunRecoverWith_HumanMode_ReportsAdoptable(t *testing.T) {
	ctx := context.Background()
	st, drv, sb := newAdoptableTestFixture(t)

	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, false) // human mode

	if err := runRecoverWith(ctx, st, drv, out); err != nil {
		t.Fatalf("runRecoverWith: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, sb.ID.String()) {
		t.Errorf("human-mode output does not mention sandbox id %s; got:\n%s", sb.ID, got)
	}
	if !strings.Contains(got, string(recovery.OutcomeAdoptable)) {
		t.Errorf("human-mode output does not mention %q; an operator running plain `nexus3 recover` "+
			"would see only the bare summary count, not that this sandbox needs adoption; got:\n%s",
			recovery.OutcomeAdoptable, got)
	}
}

// newReacquirableTestFixture is newAdoptableTestFixture plus the netns
// identity a crash-path re-acquisition requires — in particular a non-empty
// NetnsControlSocket, which is what distinguishes a sandbox that CAN have its
// perimeter rebuilt from one that predates the mechanism.
func newReacquirableTestFixture(t *testing.T) (store.Store, driver.Driver, domain.Sandbox) {
	t.Helper()
	ctx := context.Background()

	st, drv, sb := newAdoptableTestFixture(t)
	if err := st.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		rec.NetnsChildPID = 4242
		rec.NetnsChildPGID = 4242
		rec.NetnsChildStartTime = 987654
		rec.GuestTapName = "nx3h-0102030405"
		rec.CHAPISocket = "/tmp/nexus3/x.sock"
		rec.NetnsControlSocket = "/tmp/nexus3/netns-control/x.sock"
		rec.NetnsControlToken = "/tmp/nexus3/netns-control/x.token"
		return nil
	}); err != nil {
		t.Fatalf("Update sandbox with netns identity: %v", err)
	}
	updated, err := st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get sandbox: %v", err)
	}
	return st, drv, updated
}

// withStubAdoptSpawner swaps the production adopt-spawn callback for the
// duration of a test and restores it afterwards.
func withStubAdoptSpawner(t *testing.T, fn func(sb domain.Sandbox) (recovery.CAOutcome, error)) {
	t.Helper()
	prev := recoverAdoptSpawner
	recoverAdoptSpawner = fn
	t.Cleanup(func() { recoverAdoptSpawner = prev })
}

// TestRunRecoverWith_ProductionWiring_SpawnsReplacement is the guard for the
// defect this slice exists to fix: ReacquirePerimeterForSandbox had ZERO
// non-test callers, so no operator could ever trigger it.
//
// It calls the REAL production entry point and asserts the adopt-spawn
// callback is REACHED for a live-VM/dead-supervisor sandbox that carries a
// netns control socket. A test that constructed RunReacquire's inputs itself
// would recreate the original bug at one remove — the mechanism would work
// and still be unreachable. Deleting ".WithAdoptSpawner(recoverAdoptSpawner)"
// from runRecoverWith must turn this RED.
func TestRunRecoverWith_ProductionWiring_SpawnsReplacement(t *testing.T) {
	ctx := context.Background()
	st, drv, sb := newReacquirableTestFixture(t)

	var spawnedFor []domain.SandboxID
	withStubAdoptSpawner(t, func(got domain.Sandbox) (recovery.CAOutcome, error) {
		spawnedFor = append(spawnedFor, got.ID)
		return recovery.CALost, nil // the replacement reported a lost CA
	})

	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, false) // human mode: the surface an operator sees
	if err := runRecoverWith(ctx, st, drv, out); err != nil {
		t.Fatalf("runRecoverWith: %v", err)
	}

	if len(spawnedFor) != 1 || spawnedFor[0] != sb.ID {
		t.Fatalf("production recover did NOT spawn a replacement supervisor for the adoptable sandbox "+
			"(spawned=%v, want exactly [%s]) — the spawn half is not wired, so the re-acquisition "+
			"mechanism is unreachable by any operator and AC-1b stays unmet", spawnedFor, sb.ID)
	}

	got := buf.String()
	if !strings.Contains(got, "replacement supervisor was started") {
		t.Errorf("operator output does not report that a replacement was started; got:\n%s", got)
	}
	// The CA loss must be reported, not papered over: an operator who believes
	// TLS survived will diagnose the resulting failures as a network fault.
	if !strings.Contains(got, "TLS") {
		t.Errorf("operator output does not warn that in-guest TLS is broken after a crash-path "+
			"re-acquisition; got:\n%s", got)
	}
}

// TestRunRecoverWith_NoControlSocket_ReportedButNotSpawned asserts the
// NEGATIVE direction: a sandbox booted before the control-socket mechanism
// has no way to have its perimeter rebuilt, so it must be REPORTED and NOT
// spawned against. Attempting the spawn anyway is what would produce a
// partial perimeter — working-looking but bypassing egress policy.
func TestRunRecoverWith_NoControlSocket_ReportedButNotSpawned(t *testing.T) {
	ctx := context.Background()
	// newAdoptableTestFixture leaves NetnsControlSocket empty: a pre-mechanism VM.
	st, drv, sb := newAdoptableTestFixture(t)

	spawnCalls := 0
	withStubAdoptSpawner(t, func(domain.Sandbox) (recovery.CAOutcome, error) {
		spawnCalls++
		return recovery.CARecovered, nil
	})

	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, false)
	if err := runRecoverWith(ctx, st, drv, out); err != nil {
		t.Fatalf("runRecoverWith: %v", err)
	}

	if spawnCalls != 0 {
		t.Fatalf("recover attempted a spawn for a sandbox with no netns control socket (%d calls); "+
			"its netns child has no socket to answer on, so the attempt could only half-succeed", spawnCalls)
	}
	got := buf.String()
	if !strings.Contains(got, sb.ID.String()) || !strings.Contains(got, string(recovery.OutcomeAdoptable)) {
		t.Errorf("a sandbox that cannot be adopted must still be REPORTED as adoptable so the operator "+
			"knows it needs a manual restart; got:\n%s", got)
	}
}

// TestRunRecoverWith_HealthySandbox_NotSpawnedAgainst asserts the other
// negative: a sandbox with a LIVE supervisor is neither classified adoptable
// nor spawned against. Spawning a second supervisor over a live one creates
// two owners for the same VM — worse than the bug this slice fixes.
func TestRunRecoverWith_HealthySandbox_NotSpawnedAgainst(t *testing.T) {
	ctx := context.Background()

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := fake.New()

	// SupervisorPID 0 = no supervisor recorded, which applySupervisorLiveness
	// treats as "nothing to cross-check" and leaves as plainly adopted. This
	// is the healthy-VM shape: recovery must not spawn against it.
	sb := domain.Sandbox{
		ID: domain.NewSandboxID(), Name: "healthy", Project: "ac8",
		State:              domain.Running,
		NetnsControlSocket: "/tmp/nexus3/netns-control/x.sock",
		NetnsControlToken:  "/tmp/nexus3/netns-control/x.token",
	}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	drv.SetRunning(sb.ID)

	spawnCalls := 0
	withStubAdoptSpawner(t, func(domain.Sandbox) (recovery.CAOutcome, error) {
		spawnCalls++
		return recovery.CALost, nil
	})

	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, true)
	if err := runRecoverWith(ctx, st, drv, out); err != nil {
		t.Fatalf("runRecoverWith: %v", err)
	}

	if spawnCalls != 0 {
		t.Fatalf("recover spawned a replacement supervisor for a sandbox that does not need one "+
			"(%d calls) — two supervisors would own the same VM", spawnCalls)
	}
	if strings.Contains(buf.String(), string(recovery.OutcomeAdoptable)) {
		t.Errorf("a healthy sandbox was classified adoptable; got:\n%s", buf.String())
	}
}

// TestRunRecoverWith_CAOutcome_OperatorWording is the guard for the reporting
// defect proven live on sb-06G5RNMW0NWMD2P5RZ8NEMYGE0: `recover` told the
// operator the MITM CA had been lost while the replacement supervisor's own
// log said ca_recovered and in-guest TLS never broke.
//
// The wording IS the deliverable, so this asserts on the operator-visible text
// emitted by the real runRecoverWith — not on the CAOutcome value, which would
// pass just as happily with the message hardcoded. All three states are
// covered, because reporting a definite answer in the third (unknown) case is
// how the same class of defect returns.
func TestRunRecoverWith_CAOutcome_OperatorWording(t *testing.T) {
	cases := []struct {
		name    string
		ca      recovery.CAOutcome
		want    []string
		notWant []string
	}{
		{
			name:    "recovered: no loss warning",
			ca:      recovery.CARecovered,
			want:    []string{"MITM CA was re-seeded", "TLS sessions continue uninterrupted"},
			notWant: []string{"could not be recovered", "could not determine"},
		},
		{
			name:    "lost: the loss warning stands",
			ca:      recovery.CALost,
			want:    []string{"could not be recovered", "will FAIL until the guest re-imports"},
			notWant: []string{"re-seeded", "could not determine"},
		},
		{
			name: "unknown: honest undetermined, never coerced either way",
			ca:   recovery.CAUnknown,
			want: []string{"could not determine whether the MITM CA survived"},
			// Neither definite claim may appear: "recovered" would tell the
			// operator TLS survived when it may not have, and "could not be
			// recovered" is the false-loss report being fixed.
			notWant: []string{"re-seeded", "could not be recovered"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, drv, _ := newReacquirableTestFixture(t)
			withStubAdoptSpawner(t, func(domain.Sandbox) (recovery.CAOutcome, error) {
				return tc.ca, nil
			})

			var buf bytes.Buffer
			out := NewOutput(&buf, &buf, false) // human mode: the operator surface
			if err := runRecoverWith(ctx, st, drv, out); err != nil {
				t.Fatalf("runRecoverWith: %v", err)
			}
			got := buf.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("operator output is missing %q for CA outcome %v; got:\n%s", want, tc.ca, got)
				}
			}
			for _, bad := range tc.notWant {
				if strings.Contains(got, bad) {
					t.Errorf("operator output wrongly contains %q for CA outcome %v; got:\n%s", bad, tc.ca, got)
				}
			}
		})
	}
}

// TestCAOutcomeFromSupervisor_UnknownIsNeverCoerced pins the seam the CLI owns
// between the two enums. The default arm matters most: an unrecorded or
// unrecognised outcome must degrade to CAUnknown, never to CARecovered — the
// latter would report that in-guest TLS survived on the strength of a file
// that was never written.
func TestCAOutcomeFromSupervisor_UnknownIsNeverCoerced(t *testing.T) {
	cases := map[supervisor.CAOutcome]recovery.CAOutcome{
		supervisor.CAOutcomeRecovered: recovery.CARecovered,
		supervisor.CAOutcomeLost:      recovery.CALost,
		supervisor.CAOutcomeUnknown:   recovery.CAUnknown,
		supervisor.CAOutcome("bogus"): recovery.CAUnknown,
		supervisor.CAOutcome(""):      recovery.CAUnknown,
	}
	for in, want := range cases {
		if got := caOutcomeFromSupervisor(in); got != want {
			t.Errorf("caOutcomeFromSupervisor(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestSpawnReplacementSupervisor_ReadsOutcomeItDidNotDerive asserts that the
// PRODUCTION adopt-spawn callback obtains the CA outcome from the state dir
// rather than asserting one of its own. It drives the real function; the spawn
// fails (no spawn.json for a synthetic sandbox), and the contract on that path
// is CAUnknown plus an error — the fail-closed shape, since a spawn that never
// happened cannot have recovered anything.
func TestSpawnReplacementSupervisor_NoSpawnSpec_UnknownAndError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sb := domain.Sandbox{ID: domain.NewSandboxID(), Name: "nospec", Project: "hsh"}

	ca, err := spawnReplacementSupervisor(sb)
	if err == nil {
		t.Fatalf("spawnReplacementSupervisor succeeded with no spawn.json; want a refusal")
	}
	if ca != recovery.CAUnknown {
		t.Errorf("a spawn that never happened reported CA outcome %v; want CAUnknown — "+
			"any definite answer here is invented, not observed", ca)
	}
}

// TestSpawnReplacementSupervisor_GovBoundsPassedToSpawn is the
// mutation-bearing proof for AC-2 of s40-govbounds-across-swap: the
// re-acquire supervisor spawned by the recover-adopt path must receive EXACTLY
// the GovBounds, MemoryMiB, and BootVCPUs from the sandbox's spawn.json.
//
// The risk is identical to the supervisor-upgrade case: if GovBounds is dropped
// at the SpawnReacquireDetached call site (or lost in the spawn.json round-trip)
// the replacement supervisor runs with a passive governor, silently, with no
// error visible in the sandbox record.
//
// This test drives the REAL production call site (doSpawnReacquireDetached is
// the actual variable used in production). Mutation: zeroing Config.GovBounds
// at the call site in spawnReplacementSupervisor turns this RED.
func TestSpawnReplacementSupervisor_GovBoundsPassedToSpawn(t *testing.T) {
	// AF_UNIX path limit: use a short temp root.
	tmpRoot, err := os.MkdirTemp("/tmp", "n3rec")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpRoot) })
	t.Setenv("XDG_STATE_HOME", tmpRoot)

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("store.DefaultRoot: %v", err)
	}

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Name: "govbounds", Project: "hsh"}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll stateDir: %v", err)
	}

	wantBounds := resize.Bounds{
		MemMinBytes:  512 << 20,
		MemMaxBytes:  4096 << 20,
		VCPUMin:      2,
		VCPUMax:      8,
		DiskMaxBytes: 100 << 30,
	}
	wantMemory := uint32(2048)
	wantVCPUs := uint32(4)

	if err := supervisor.WriteSpawnSpec(stateDir, supervisor.Config{
		SandboxRef: sb.ID.String(),
		StateDir:   stateDir,
		GovBounds:  wantBounds,
		MemoryMiB:  wantMemory,
		BootVCPUs:  wantVCPUs,
	}); err != nil {
		t.Fatalf("WriteSpawnSpec: %v", err)
	}

	var capturedCfg supervisor.SpawnConfig
	origSpawn := doSpawnReacquireDetached
	t.Cleanup(func() { doSpawnReacquireDetached = origSpawn })
	doSpawnReacquireDetached = func(cfg supervisor.SpawnConfig) (int, error) {
		capturedCfg = cfg
		return 0, nil
	}

	if _, err := spawnReplacementSupervisor(sb); err != nil {
		t.Fatalf("spawnReplacementSupervisor: %v", err)
	}

	if capturedCfg.Config.GovBounds != wantBounds {
		t.Errorf("GovBounds mismatch:\n  got  %+v\n  want %+v", capturedCfg.Config.GovBounds, wantBounds)
	}
	if capturedCfg.Config.MemoryMiB != wantMemory {
		t.Errorf("MemoryMiB: got %d, want %d", capturedCfg.Config.MemoryMiB, wantMemory)
	}
	if capturedCfg.Config.BootVCPUs != wantVCPUs {
		t.Errorf("BootVCPUs: got %d, want %d", capturedCfg.Config.BootVCPUs, wantVCPUs)
	}
}
