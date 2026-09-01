package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/recovery"
	"github.com/IniZio/nexus3/internal/core/store"
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
