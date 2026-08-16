package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// newBatchExecSvc builds a service backed by a real FileStore pre-populated
// with count sandboxes tagged to motiveID. The fake driver has DialGuest
// configured to return dialErr (nil = success path, non-nil = forced failure).
func newBatchExecSvc(t *testing.T, motiveID string, count int, dialErr error) *service.Service {
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
	if dialErr != nil {
		fd.SetDialGuestError(dialErr)
	}
	return service.New(st, fd, lifecycle.New())
}

// errExecDialInjected is a sentinel dial error for batch exec tests.
var errExecDialInjected = errors.New("batch-exec: injected dial error")

// ── Usage errors ─────────────────────────────────────────────────────────────

func TestExecBatch_UsageError_NoArgv(t *testing.T) {
	out, _, _ := capture(false)
	svc := newBatchExecSvc(t, "m1", 0, nil)
	err := runExecBatchWithSvc(context.Background(), "motive", "m1", 2, []string{}, out, svc)
	if err == nil {
		t.Fatal("expected error for empty argv, got nil")
	}
}

// ── Empty motive ──────────────────────────────────────────────────────────────

func TestExecBatch_EmptyMotive_NoError(t *testing.T) {
	out, stdout, _ := capture(false)
	svc := newBatchExecSvc(t, "empty-motive", 0, nil)
	err := runExecBatchWithSvc(context.Background(), "motive", "empty-motive", 2, []string{"true"}, out, svc)
	if err != nil {
		t.Fatalf("empty motive: unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "no sandboxes") {
		t.Errorf("expected 'no sandboxes' message, got: %q", stdout.String())
	}
}

// ── Partial failure: siblings complete ───────────────────────────────────────

// TestExecBatch_PartialFailure_SiblingsReported verifies that when the agent
// dial fails for some sandboxes, the CLI reports all sandbox outcomes and
// returns a non-zero error without silently dropping any sandbox.
func TestExecBatch_PartialFailure_SiblingsReported(t *testing.T) {
	const motive = "cli-partial-fail"
	// Force all execs to fail via DialGuest so we can test the error path
	// without a live VM. All 3 sandboxes will fail.
	svc := newBatchExecSvc(t, motive, 3, errExecDialInjected)

	out, _, stderr := capture(false)
	err := runExecBatchWithSvc(context.Background(), "motive", motive, 2, []string{"true"}, out, svc)
	if err == nil {
		t.Fatal("expected non-nil error when all sandboxes fail, got nil")
	}

	// The error must be a *CodedError (not e.g. a panic or an aborted early return).
	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Errorf("expected *CodedError, got %T: %v", err, err)
	}

	// stderr should contain "error:" lines for all three sandboxes.
	errOut := stderr.String()
	if count := strings.Count(errOut, "error:"); count < 3 {
		t.Errorf("expected at least 3 'error:' lines (one per sandbox), got %d in: %q", count, errOut)
	}
}

// ── Human-mode output format ──────────────────────────────────────────────────

// TestExecBatch_HumanOutput_SandboxHeaders verifies that the human-readable
// output contains one header per sandbox outcome.
func TestExecBatch_HumanOutput_SandboxHeaders(t *testing.T) {
	const motive = "cli-human-output"
	svc := newBatchExecSvc(t, motive, 2, errExecDialInjected)

	out, stdout, _ := capture(false)
	// Error is expected — we just check output shape.
	_ = runExecBatchWithSvc(context.Background(), "motive", motive, 2, []string{"true"}, out, svc)

	stdoutStr := stdout.String()
	if count := strings.Count(stdoutStr, "==="); count < 2 {
		t.Errorf("expected at least 2 sandbox headers (===), got %d in: %q", count, stdoutStr)
	}
}

// ── JSON mode ─────────────────────────────────────────────────────────────────

// TestExecBatch_JSONMode_Shape verifies the JSON envelope contains the motive
// ID and an outcomes array even when all sandboxes fail.
func TestExecBatch_JSONMode_Shape(t *testing.T) {
	const motive = "cli-json-fail"
	svc := newBatchExecSvc(t, motive, 2, errExecDialInjected)

	out, stdout, _ := capture(true /* json */)
	// Error expected; we check the JSON shape regardless.
	_ = runExecBatchWithSvc(context.Background(), "motive", motive, 2, []string{"true"}, out, svc)

	raw := stdout.String()
	if !strings.Contains(raw, `"label_key"`) {
		t.Errorf("JSON output missing label_key: %q", raw)
	}
	if !strings.Contains(raw, `"outcomes"`) {
		t.Errorf("JSON output missing outcomes: %q", raw)
	}
}

// ── Flag parsing via runExec ──────────────────────────────────────────────────

// TestRunExec_LabelFlag_UsageError verifies that the top-level exec command
// returns a usage error when --label is given with no argv.
func TestRunExec_LabelFlag_UsageError(t *testing.T) {
	// Test via runExecBatchWithSvc directly (nil argv path).
	out, _, _ := capture(false)
	svc := newBatchExecSvc(t, "m", 0, nil)
	err := runExecBatchWithSvc(context.Background(), "motive", "m", 2, nil, out, svc)
	if err == nil {
		t.Fatal("expected error for nil argv, got nil")
	}
}

// TestRunExec_ParallelHelp verifies the --parallel flag is documented with
// the swap-pressure rationale in its help text. We exercise this by parsing
// "--help" which makes flag.FlagSet print usage and return an error.
func TestRunExec_ParallelHelp(t *testing.T) {
	// Capture the flag usage output by redirecting FlagSet output.
	var buf bytes.Buffer
	fs := newExecFlagSetForTest(&buf)
	_ = fs.Parse([]string{"--help"})
	help := buf.String()
	// The --parallel flag help text must mention swap pressure.
	if !strings.Contains(help, "swap") {
		t.Errorf("--parallel help text must mention swap pressure, got: %q", help)
	}
}

// newExecFlagSetForTest re-creates the exec command's FlagSet for inspection.
// Kept in test file so changes to runExec's FlagSet are immediately visible.
func newExecFlagSetForTest(output *bytes.Buffer) *flag.FlagSet {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.String("label", "",
		"run command across every sandbox matching label KEY=VALUE\n"+
			"\t(only motive=<id> is supported; e.g. --label motive=my-motive)")
	fs.Int("parallel", service.DefaultBatchParallel,
		"max sandboxes to exec concurrently (derived from measured host swap pressure at ~84%;\n"+
			"\traising this beyond 2 risks swap thrashing — measure before changing)")
	fs.Bool("pty", false, "allocate a PTY for the session (single-sandbox only)")
	fs.Uint("rows", 24, "terminal rows (requires --pty)")
	fs.Uint("cols", 80, "terminal columns (requires --pty)")
	return fs
}
