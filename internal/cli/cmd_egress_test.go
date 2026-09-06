package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// newEgressTestSandbox mirrors the newLogTestSandbox pattern: sets up an
// isolated XDG_STATE_HOME, creates a sandbox via the production constructor,
// and returns the service, sandbox, and supervisor state dir.
func newEgressTestSandbox(t *testing.T) (*service.Service, domain.Sandbox, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	svc, err := newSandboxService()
	if err != nil {
		t.Fatalf("newSandboxService: %v", err)
	}
	sb, err := svc.Create(context.Background(), "proj", "egress-cmd", service.CreateOptions{})
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

// writeDecisionsFixture writes a slice of EgressEvent JSON lines to
// egress-decisions.jsonl in stateDir.
func writeDecisionsFixture(t *testing.T, stateDir string, events []perimeter.EgressEvent) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("encode EgressEvent: %v", err)
		}
	}
	path := filepath.Join(stateDir, "egress-decisions.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write decisions file: %v", err)
	}
}

// T4-AC1: egress log prints each EgressEvent's host, verdict, reason, and ts.
func TestEgressLog_PrintsDecisionRecords(t *testing.T) {
	svc, sb, stateDir := newEgressTestSandbox(t)

	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	writeDecisionsFixture(t, stateDir, []perimeter.EgressEvent{
		{Host: "api.github.com", Verdict: perimeter.Allow, Reason: "allowed by domain policy", Timestamp: now},
		{Host: "evil.example.com", Verdict: perimeter.Deny, Reason: "not in allow list", Timestamp: now.Add(time.Second)},
	})

	out, stdout, _ := capture(false)
	if err := runEgressLogWith(context.Background(), sb.Handle(), false, out, svc); err != nil {
		t.Fatalf("runEgressLogWith: %v", err)
	}
	got := stdout.String()

	if !strings.Contains(got, "api.github.com") {
		t.Errorf("output missing allowed host: %q", got)
	}
	if !strings.Contains(got, "ALLOW") {
		t.Errorf("output missing ALLOW verdict: %q", got)
	}
	if !strings.Contains(got, "evil.example.com") {
		t.Errorf("output missing denied host: %q", got)
	}
	if !strings.Contains(got, "DENY") {
		t.Errorf("output missing DENY verdict: %q", got)
	}
	// T4-AC1: reason is present
	if !strings.Contains(got, "allowed by domain policy") {
		t.Errorf("output missing allow reason: %q", got)
	}
	if !strings.Contains(got, "not in allow list") {
		t.Errorf("output missing deny reason: %q", got)
	}
	// timestamp is present (RFC3339 format contains "2024")
	if !strings.Contains(got, "2024") {
		t.Errorf("output missing timestamp: %q", got)
	}
}

// T4-AC3: a denied event must carry a non-empty reason, not a bare boolean.
func TestEgressLog_DenyCarriesReason(t *testing.T) {
	svc, sb, stateDir := newEgressTestSandbox(t)

	writeDecisionsFixture(t, stateDir, []perimeter.EgressEvent{
		{Host: "blocked.io", Verdict: perimeter.Deny, Reason: "not in allow list", Timestamp: time.Now()},
	})

	out, stdout, _ := capture(false)
	if err := runEgressLogWith(context.Background(), sb.Handle(), false, out, svc); err != nil {
		t.Fatalf("runEgressLogWith: %v", err)
	}
	got := stdout.String()

	if !strings.Contains(got, "not in allow list") {
		t.Errorf("deny record missing reason: %q", got)
	}
	// Must not expose a raw boolean
	if strings.Contains(got, " true") || strings.Contains(got, " false") {
		t.Errorf("deny record must not expose bare boolean: %q", got)
	}
}

// TestEgressLog_NoFile returns the coded error when no decisions file exists.
func TestEgressLog_NoFile(t *testing.T) {
	svc, sb, _ := newEgressTestSandbox(t)

	out, _, _ := capture(false)
	err := runEgressLogWith(context.Background(), sb.Handle(), false, out, svc)
	if err == nil {
		t.Fatal("expected error for missing decisions file")
	}
	ce, ok := err.(*CodedError)
	if !ok {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if ce.Code != egressDecisionsNotFoundCode {
		t.Errorf("wrong error code: got %q, want %q", ce.Code, egressDecisionsNotFoundCode)
	}
}

// TestEgressLog_JSONOutput emits egress.decision envelope in JSON mode.
func TestEgressLog_JSONOutput(t *testing.T) {
	svc, sb, stateDir := newEgressTestSandbox(t)

	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	writeDecisionsFixture(t, stateDir, []perimeter.EgressEvent{
		{Host: "api.example.com", Verdict: perimeter.Allow, Reason: "allowed by domain policy", Timestamp: now},
	})

	out, stdout, _ := capture(true /* json */)
	if err := runEgressLogWith(context.Background(), sb.Handle(), false, out, svc); err != nil {
		t.Fatalf("runEgressLogWith: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "egress.decision") {
		t.Errorf("JSON output missing 'egress.decision' kind: %q", got)
	}
	if !strings.Contains(got, "api.example.com") {
		t.Errorf("JSON output missing host: %q", got)
	}
}

// ── T6 tests ─────────────────────────────────────────────────────────────────

// fakeEgressAllow records the sockPath+host it was called with, then returns
// fakeErr (nil means success).
type fakeEgressAllow struct {
	calledSockPath string
	calledHost     string
	fakeErr        error
}

func (f *fakeEgressAllow) call(_ context.Context, sockPath, host string) error {
	f.calledSockPath = sockPath
	f.calledHost = host
	return f.fakeErr
}

// newEgressAllowTestSandbox builds on newEgressTestSandbox and additionally
// creates a placeholder sock file so runEgressAllowWith treats the sandbox as
// running (the live-running guard os.Stat(sockPath) succeeds).
func newEgressAllowTestSandbox(t *testing.T) (*service.Service, domain.Sandbox, string) {
	t.Helper()
	svc, sb, stateDir := newEgressTestSandbox(t)
	sockPath := filepath.Join(stateDir, "supervisor.sock")
	f, err := os.Create(sockPath)
	if err != nil {
		t.Fatalf("create fake sock: %v", err)
	}
	f.Close()
	return svc, sb, sockPath
}

// T6-AC1: egress allow calls the IPC verb for a running sandbox.
func TestEgressAllow_CallsIPC(t *testing.T) {
	svc, sb, _ := newEgressAllowTestSandbox(t)

	fake := &fakeEgressAllow{}
	out, _, _ := capture(false)
	if err := runEgressAllowWith(context.Background(), sb.Handle(), "registry.npmjs.org", out, svc, fake.call); err != nil {
		t.Fatalf("runEgressAllowWith: %v", err)
	}
	if fake.calledHost != "registry.npmjs.org" {
		t.Errorf("IPC host: got %q, want %q", fake.calledHost, "registry.npmjs.org")
	}
	if fake.calledSockPath == "" {
		t.Error("IPC sockPath was not forwarded")
	}
}

// T6-AC2: the added host is persisted in Envelope.AllowedHosts after egress allow,
// proving a supervisor bounce (service.go:792 reads Envelope.AllowedHosts) re-admits it.
func TestEgressAllow_PersistsHost(t *testing.T) {
	svc, sb, _ := newEgressAllowTestSandbox(t)

	fake := &fakeEgressAllow{}
	out, _, _ := capture(false)
	if err := runEgressAllowWith(context.Background(), sb.Handle(), "pypi.org", out, svc, fake.call); err != nil {
		t.Fatalf("runEgressAllowWith: %v", err)
	}

	// Read the persisted record back and assert the host is present.
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("store.DefaultRoot: %v", err)
	}
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	got, err := st.Get(context.Background(), sb.ID)
	if err != nil {
		t.Fatalf("st.Get: %v", err)
	}
	found := false
	for _, h := range got.Envelope.AllowedHosts {
		if h == "pypi.org" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("AllowedHosts %v does not contain %q; a supervisor bounce would not re-admit it", got.Envelope.AllowedHosts, "pypi.org")
	}
}

// T6-AC2 (idempotency): a second egress allow for the same host must not duplicate the entry.
func TestEgressAllow_Idempotent(t *testing.T) {
	svc, sb, _ := newEgressAllowTestSandbox(t)
	fake := &fakeEgressAllow{}
	out, _, _ := capture(false)
	for i := 0; i < 2; i++ {
		if err := runEgressAllowWith(context.Background(), sb.Handle(), "pypi.org", out, svc, fake.call); err != nil {
			t.Fatalf("runEgressAllowWith (i=%d): %v", i, err)
		}
	}
	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	got, _ := st.Get(context.Background(), sb.ID)
	count := 0
	for _, h := range got.Envelope.AllowedHosts {
		if h == "pypi.org" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 occurrence of %q, got %d; hosts: %v", "pypi.org", count, got.Envelope.AllowedHosts)
	}
}

// T6-AC3: egress allow on a sandbox with no live supervisor returns a clear
// error and does not mutate the store.
func TestEgressAllow_NoSupervisor_ReturnsError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	svc, err := newSandboxService()
	if err != nil {
		t.Fatalf("newSandboxService: %v", err)
	}
	sb, err := svc.Create(context.Background(), "proj", "stopped-sandbox", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No state dir or sock file — sandbox looks stopped.

	called := false
	fakeFn := func(_ context.Context, _, _ string) error { called = true; return nil }
	out, _, _ := capture(false)
	err = runEgressAllowWith(context.Background(), sb.Handle(), "example.com", out, svc, fakeFn)
	if err == nil {
		t.Fatal("expected error for stopped sandbox, got nil")
	}
	ce, ok := err.(*CodedError)
	if !ok {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if ce.Code != egressAllowSockNotFoundCode {
		t.Errorf("wrong code: got %q, want %q", ce.Code, egressAllowSockNotFoundCode)
	}
	if called {
		t.Error("IPC must not be called for a stopped sandbox")
	}

	// Store record must be unchanged (AllowedHosts empty).
	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	rec, _ := st.Get(context.Background(), sb.ID)
	for _, h := range rec.Envelope.AllowedHosts {
		if h == "example.com" {
			t.Errorf("store was mutated despite error: found %q in AllowedHosts", h)
		}
	}
}

// TestEgress_UnknownSubcommand returns a usage error for unknown sub.
func TestEgress_UnknownSubcommand(t *testing.T) {
	out, _, _ := capture(false)
	err := runEgress(context.Background(), []string{"unknownsub"}, out)
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

// TestEgress_NoArgs returns a usage error when no subcommand given.
func TestEgress_NoArgs(t *testing.T) {
	out, _, _ := capture(false)
	err := runEgress(context.Background(), nil, out)
	if err == nil {
		t.Fatal("expected error")
	}
}
