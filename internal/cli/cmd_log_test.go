package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// newLogTestSandbox builds a service whose store root is store.DefaultRoot()
// (via XDG_STATE_HOME), creates one sandbox, and returns the service plus the
// sandbox's supervisor state directory — the same directory runLogWithSvc
// computes internally via supervisor.DefaultStateDir(storeRoot, sb.ID).
//
// This mirrors the ResolveRef -> store.DefaultRoot -> supervisor.DefaultStateDir
// idiom the log command itself uses, so the test's notion of "where the log
// lives" cannot silently diverge from the command's.
func newLogTestSandbox(t *testing.T) (*service.Service, domain.Sandbox, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	svc, err := newSandboxService()
	if err != nil {
		t.Fatalf("newSandboxService: %v", err)
	}
	sb, err := svc.Create(context.Background(), "proj", "log-cmd", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("store.DefaultRoot: %v", err)
	}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll stateDir: %v", err)
	}
	return svc, sb, stateDir
}

func writeLog(t *testing.T, stateDir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, "supervisor.log"), []byte(content), 0o644); err != nil {
		t.Fatalf("write supervisor.log: %v", err)
	}
}

// TestLog_WholeFile_Human is the canary: it fails without the log command's
// core read path (see AGENT-REPORT.md for the observed red output when this
// was verified by temporarily breaking runLogWithSvc).
func TestLog_WholeFile_Human(t *testing.T) {
	svc, sb, stateDir := newLogTestSandbox(t)
	const content = "line one\nline two\nline three\n"
	writeLog(t, stateDir, content)

	out, stdout, _ := capture(false)
	if err := runLogWithSvc(context.Background(), sb.Handle(), 0, false, out, svc); err != nil {
		t.Fatalf("runLogWithSvc: %v", err)
	}
	if stdout.String() != content {
		t.Errorf("stdout = %q, want %q (raw bytes)", stdout.String(), content)
	}
}

func TestLog_Tail_Human(t *testing.T) {
	svc, sb, stateDir := newLogTestSandbox(t)
	writeLog(t, stateDir, "l1\nl2\nl3\nl4\nl5\n")

	out, stdout, _ := capture(false)
	if err := runLogWithSvc(context.Background(), sb.Handle(), 2, false, out, svc); err != nil {
		t.Fatalf("runLogWithSvc: %v", err)
	}
	want := "l4\nl5\n"
	if stdout.String() != want {
		t.Errorf("tailed stdout = %q, want %q", stdout.String(), want)
	}
}

func TestLog_JSONMode(t *testing.T) {
	svc, sb, stateDir := newLogTestSandbox(t)
	writeLog(t, stateDir, "a\nb\nc\n")

	out, stdout, stderr := capture(true)
	if err := runLogWithSvc(context.Background(), sb.Handle(), 2, false, out, svc); err != nil {
		t.Fatalf("runLogWithSvc: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty in JSON mode, got: %q", stderr.String())
	}
	var env map[string]any
	decodeOne(t, stdout, &env)
	if env["kind"] != "log.lines" {
		t.Errorf("kind = %v, want log.lines", env["kind"])
	}
	data := env["data"].(map[string]any)
	lines := data["lines"].([]any)
	if len(lines) != 2 || lines[0] != "b" || lines[1] != "c" {
		t.Errorf("lines = %v, want [b c]", lines)
	}
	// JSON mode must carry nothing but the envelope (TestSandboxList_JSONModeHasNoTable pins the same rule for ps).
	if strings.Count(stdout.String(), "\n") != 0 {
		t.Errorf("stdout has stray output beyond the single JSON line: %q", stdout.String())
	}
}

func TestLog_MissingLogFile(t *testing.T) {
	svc, sb, _ := newLogTestSandbox(t)
	// No supervisor.log written — the sandbox exists but was never started.

	out, _, _ := capture(false)
	err := runLogWithSvc(context.Background(), sb.Handle(), 0, false, out, svc)
	if err == nil {
		t.Fatal("expected an error for a missing log file, got nil")
	}
	coded, ok := err.(*CodedError)
	if !ok {
		t.Fatalf("error is not *CodedError: %T: %v", err, err)
	}
	if coded.Code != logErrCodeNotFound {
		t.Errorf("code = %q, want %q", coded.Code, logErrCodeNotFound)
	}
}

func TestLog_MissingLogFile_SurfacesSupervisorErr(t *testing.T) {
	svc, sb, stateDir := newLogTestSandbox(t)
	if err := os.WriteFile(filepath.Join(stateDir, "supervisor.err"), []byte("cloud-hypervisor exited: no kernel configured\n"), 0o644); err != nil {
		t.Fatalf("write supervisor.err: %v", err)
	}

	out, _, _ := capture(false)
	err := runLogWithSvc(context.Background(), sb.Handle(), 0, false, out, svc)
	if err == nil {
		t.Fatal("expected an error for a missing log file, got nil")
	}
	if !strings.Contains(err.Error(), "no kernel configured") {
		t.Errorf("error message %q does not surface supervisor.err contents", err.Error())
	}
}

func TestLog_SandboxNotFound(t *testing.T) {
	svc, _, _ := newLogTestSandbox(t)

	out, _, _ := capture(false)
	err := runLogWithSvc(context.Background(), "proj/does-not-exist", 0, false, out, svc)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	coded, ok := err.(*CodedError)
	if !ok {
		t.Fatalf("error is not *CodedError: %T: %v", err, err)
	}
	if coded.Code != sandboxErrCodeNotFound {
		t.Errorf("code = %q, want %q", coded.Code, sandboxErrCodeNotFound)
	}
}

// TestLog_FollowStreamsAppendedLines drives the --follow path end to end:
// it starts a follow, appends to the log file mid-stream, then cancels the
// context (mirroring SIGINT/SIGTERM via root.go's signal.NotifyContext) and
// checks both the original and appended content arrived.
func TestLog_FollowStreamsAppendedLines(t *testing.T) {
	svc, sb, stateDir := newLogTestSandbox(t)
	writeLog(t, stateDir, "initial\n")

	out, stdout, _ := capture(false)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- runLogWithSvc(ctx, sb.Handle(), 0, true, out, svc)
	}()

	// Let the follower do its initial read, then append while it is polling.
	time.Sleep(100 * time.Millisecond)
	f, err := os.OpenFile(filepath.Join(stateDir, "supervisor.log"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log for append: %v", err)
	}
	if _, err := f.WriteString("appended\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	// Poll interval inside streamLogFollow is 200ms; give it time to notice.
	time.Sleep(350 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runLogWithSvc (follow): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not return after context cancellation")
	}

	got := stdout.String()
	if !strings.Contains(got, "initial\n") {
		t.Errorf("follow output missing initial content: %q", got)
	}
	if !strings.Contains(got, "appended\n") {
		t.Errorf("follow output missing appended content: %q", got)
	}
}

// TestLog_JSONFollowRejected exercises the flag-parsing entry point (runLog),
// not runLogWithSvc, since the --json/--follow rejection happens in runLog
// before the service is even constructed.
func TestLog_JSONFollowRejected(t *testing.T) {
	out, _, _ := capture(true)
	err := runLog(context.Background(), []string{"--follow", "proj/whatever"}, out)
	if err == nil {
		t.Fatal("expected --follow to be rejected under --json, got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("error is not *UsageError: %T: %v", err, err)
	}
}

// TestLog_TailShortFlag proves -n behaves the same as --tail all the way
// through runLog's flag parsing (which builds its own service internally, so
// this exercises the full entry point rather than runLogWithSvc directly).
func TestLog_TailShortFlag(t *testing.T) {
	_, sb, stateDir := newLogTestSandbox(t)
	writeLog(t, stateDir, "l1\nl2\nl3\n")

	out, stdout, _ := capture(false)
	if err := runLog(context.Background(), []string{"-n", "1", sb.Handle()}, out); err != nil {
		t.Fatalf("runLog: %v", err)
	}
	if stdout.String() != "l3\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "l3\n")
	}
}

// TestLog_RefBeforeFlags is a regression test: the documented (and the task's
// own proof-of-work) invocation shape is "nexus3 log <ref> -n <N>" — the ref
// BEFORE the flags. A naive fs.Parse(args) stops consuming at the first
// non-flag token, so it would treat "-n"/"20" as extra stray positionals once
// the ref came first and fail with a usage error. This was caught by an
// actual binary run (`/tmp/nexus3 log loop/log-cmd -n 20`), not by the
// flags-first-only tests above.
func TestLog_RefBeforeFlags(t *testing.T) {
	_, sb, stateDir := newLogTestSandbox(t)
	writeLog(t, stateDir, "l1\nl2\nl3\n")

	t.Run("short_tail", func(t *testing.T) {
		out, stdout, _ := capture(false)
		if err := runLog(context.Background(), []string{sb.Handle(), "-n", "1"}, out); err != nil {
			t.Fatalf("runLog: %v", err)
		}
		if stdout.String() != "l3\n" {
			t.Errorf("stdout = %q, want %q", stdout.String(), "l3\n")
		}
	})

	t.Run("long_tail", func(t *testing.T) {
		out, stdout, _ := capture(false)
		if err := runLog(context.Background(), []string{sb.Handle(), "--tail", "2"}, out); err != nil {
			t.Fatalf("runLog: %v", err)
		}
		if stdout.String() != "l2\nl3\n" {
			t.Errorf("stdout = %q, want %q", stdout.String(), "l2\nl3\n")
		}
	})

	t.Run("follow_json_still_refused", func(t *testing.T) {
		out, _, _ := capture(true)
		err := runLog(context.Background(), []string{sb.Handle(), "--follow"}, out)
		if _, ok := err.(*UsageError); !ok {
			t.Fatalf("error is not *UsageError: %T: %v", err, err)
		}
	})
}

// TestLog_JSON_Envelope_Marshals is a smoke test that logLinesJSON marshals
// as expected (lines present, empty supervisor_error omitted).
func TestLog_JSON_Envelope_Marshals(t *testing.T) {
	b, err := json.Marshal(logLinesJSON{SandboxID: "abc", Lines: []string{"x"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "supervisor_error") {
		t.Errorf("empty SupervisorError should be omitted: %s", b)
	}
}
