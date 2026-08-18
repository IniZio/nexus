package acceptance

import (
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/recovery"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	testharness "github.com/IniZio/nexus3/internal/test/harness"
)

// harness holds wired-together components for a single acceptance test.
// Each test that needs a fresh substrate must call newHarness — never share
// a harness across subtests that mutate state.
type harness struct {
	st  *store.FileStore
	drv *fake.FakeDriver
	svc *service.Service
	rec *recovery.Recoverer
}

// newHarness returns a harness backed by a fresh temporary directory.
// The store root is isolated from the real state directory.
// Component wiring is delegated to the shared internal/test/harness seam.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h, err := testharness.New(t.TempDir())
	if err != nil {
		t.Fatalf("newHarness: %v", err)
	}
	return &harness{
		st:  h.St,
		drv: h.Drv,
		svc: h.Svc,
		rec: h.Rec,
	}
}

// sandboxConfig collects optional sandbox creation parameters.
type sandboxConfig struct {
	removeOnExit bool
}

// sandboxOption is a functional option for createSandbox.
type sandboxOption func(*sandboxConfig)

// withRemoveOnExit sets RemoveOnExit=true on the created sandbox.
func withRemoveOnExit() sandboxOption {
	return func(c *sandboxConfig) { c.removeOnExit = true }
}

// createSandbox persists a sandbox directly into the store in the given state.
// This bypasses the service layer so tests can set up sandboxes in any valid
// state — including states that the service does not produce directly (Paused,
// Stopped) — without a running driver.
func (h *harness) createSandbox(t *testing.T, state domain.State, opts ...sandboxOption) domain.Sandbox {
	t.Helper()
	cfg := sandboxConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	sb := domain.Sandbox{
		ID:           domain.NewSandboxID(),
		Name:         "test",
		Project:      "acceptance",
		State:        state,
		Envelope:     domain.Envelope{ImageDigest: "sha256:acceptance"},
		InstanceID:   "inst-accept-0",
		RemoveOnExit: cfg.removeOnExit,
	}
	if err := h.st.Create(context.Background(), sb); err != nil {
		t.Fatalf("createSandbox: %v", err)
	}
	return sb
}

// setRemovalMarker writes the WAL removal marker for sb, simulating the state
// left by a crash that occurred after SetRemovalMarker but before Delete.
func (h *harness) setRemovalMarker(t *testing.T, sb domain.Sandbox) {
	t.Helper()
	if err := h.st.SetRemovalMarker(context.Background(), sb.ID); err != nil {
		t.Fatalf("setRemovalMarker: %v", err)
	}
}

// simulateCrash makes a VM appear absent as if the VMM process crashed.
// Only the fake driver's table is affected; the store is unchanged.
func (h *harness) simulateCrash(id domain.SandboxID) {
	h.drv.SimulateCrash(id)
}

// simulateHostReboot makes all Paused VMs absent and leaves Running VMs
// unaffected, matching the ACPI shutdown semantics of a real host reboot.
func (h *harness) simulateHostReboot() {
	h.drv.SimulateHostReboot()
}

// runRecovery runs h.rec.Recover and fatally fails the test if the store
// cannot be listed. Individual sandbox errors surface in Report.Outcomes.
func (h *harness) runRecovery(t *testing.T) recovery.Report {
	t.Helper()
	report, err := h.rec.Recover(context.Background())
	if err != nil {
		t.Fatalf("runRecovery: %v", err)
	}
	return report
}

// findOutcome returns the SandboxOutcome for id in report, or fatally fails
// the test if none is found.
func findOutcome(t *testing.T, report recovery.Report, id domain.SandboxID) recovery.SandboxOutcome {
	t.Helper()
	for _, o := range report.Outcomes {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("findOutcome: no outcome for sandbox %s in report", id)
	panic("unreachable")
}
