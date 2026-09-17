package clientagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/portfwd"
)

func TestRemoteStateReadCommand_SurvivesSSHArgvJoin(t *testing.T) {
	argv := ExecArgv("host", "/tmp/x.ctl", RemoteStateReadCommand())
	remote := strings.Join(argv[len(argv)-1:], " ")
	if strings.HasPrefix(remote, "sh -c") {
		t.Fatalf("remote command must not be a split sh -c: %q", remote)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	out, err := exec.Command("sh", "-c", remote).Output()
	if err != nil {
		t.Fatalf("remote command failed: %v", err)
	}
	combined, err := parseRemoteCombinedState(string(out))
	if err != nil {
		t.Fatalf("parseRemoteCombinedState: %v", err)
	}
	if len(combined.ForwardsState.Forwards) != 0 {
		t.Errorf("absent forwards.state must read as empty, got %v", combined.ForwardsState.Forwards)
	}
	if !combined.FocusMissing {
		t.Error("absent focus.state must result in FocusMissing=true")
	}
}

func TestHerdrMachine_DecodesSession(t *testing.T) {
	with := []byte(`[{"id":"m1","target":"host","enabled":true,"session":"s1"}]`)
	var ms []HerdrMachine
	if err := json.Unmarshal(with, &ms); err != nil {
		t.Fatal(err)
	}
	if ms[0].Session != "s1" {
		t.Errorf("session field: got %q, want %q", ms[0].Session, "s1")
	}

	without := []byte(`[{"id":"m2","target":"host","enabled":true}]`)
	var ms2 []HerdrMachine
	if err := json.Unmarshal(without, &ms2); err != nil {
		t.Fatal(err)
	}
	if ms2[0].Session != "" {
		t.Errorf("missing session must decode as empty string, got %q", ms2[0].Session)
	}
}

func TestFilterToFocused(t *testing.T) {
	rows := []RemoteForwardEntry{
		{Port: 3000, Sandbox: "sandbox-A", Status: "live"},
		{Port: 4000, Sandbox: "sandbox-B", Status: "live"},
	}

	got := filterToFocused(rows, "sandbox-A")
	if len(got) != 1 || got[0].Port != 3000 {
		t.Errorf("focused on A: got %v", got)
	}

	got = filterToFocused(rows, "")
	if len(got) != 0 {
		t.Errorf("no focused workspace: want nil/empty, got %v", got)
	}
}


type fakeLn struct {
	ch      chan struct{}
	onClose func()
	once    sync.Once
}

func (fl *fakeLn) Accept() (net.Conn, error) { <-fl.ch; return nil, fmt.Errorf("closed") }
func (fl *fakeLn) Close() error {
	fl.once.Do(func() {
		if fl.onClose != nil {
			fl.onClose()
		}
		close(fl.ch)
	})
	return nil
}
func (fl *fakeLn) Addr() net.Addr { return &net.TCPAddr{} }

func makeFakeRun(applied, cancelled *[]uint16) portfwd.Runner {
	return func(_ context.Context, argv []string) (string, string, int, error) {
		if len(argv) >= 3 && argv[1] == "-O" {
			switch argv[2] {
			case "check":
				return "", "", 0, nil
			case "forward", "cancel":
				for i, a := range argv {
					if a == "-L" && i+1 < len(argv) {
						portStr, _, _ := strings.Cut(argv[i+1], ":")
						p, _ := strconv.ParseUint(portStr, 10, 16)
						if argv[2] == "forward" {
							*applied = append(*applied, uint16(p))
						} else {
							*cancelled = append(*cancelled, uint16(p))
						}
					}
				}
				return "", "", 0, nil
			}
		}
		if len(argv) >= 2 && argv[1] == "-ltn" {
			return "", "", 0, nil
		}
		return "", "", 0, nil
	}
}

func TestTick_FocusScoping(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()

	machineJSON := `[{"id":"m1","target":"host1","enabled":true,"session":"s1"}]`
	origExec := ExecCommandContext
	ExecCommandContext = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.Command("printf", "%s", machineJSON)
	}
	t.Cleanup(func() { ExecCommandContext = origExec })

	tick := 0
	fwds := RemoteForwardsState{Forwards: []RemoteForwardEntry{
		{Port: 3000, Sandbox: "sandbox-A", Status: "live"},
		{Port: 4000, Sandbox: "sandbox-B", Status: "live"},
	}}
	origReader := RemoteStateReader
	RemoteStateReader = func(_ context.Context, _, _ string) (*RemoteCombinedState, error) {
		tick++
		sb := "sandbox-A"
		if tick > 1 {
			sb = "sandbox-B"
		}
		return &RemoteCombinedState{
			ForwardsState: fwds,
			FocusState:    portfwd.FocusState{SandboxID: sb},
		}, nil
	}
	t.Cleanup(func() { RemoteStateReader = origReader })

	var applied, cancelled []uint16
	origListen := ForwarderListenFunc
	ForwarderListenFunc = func(_, addr string) (net.Listener, error) {
		_, portStr, _ := net.SplitHostPort(addr)
		p, _ := strconv.ParseUint(portStr, 10, 16)
		applied = append(applied, uint16(p))
		return &fakeLn{ch: make(chan struct{}), onClose: func() { cancelled = append(cancelled, uint16(p)) }}, nil
	}
	t.Cleanup(func() { ForwarderListenFunc = origListen })

	var ssApplied, ssCancelled []uint16
	origRunner := ForwarderRunner
	ForwarderRunner = makeFakeRun(&ssApplied, &ssCancelled)
	t.Cleanup(func() { ForwarderRunner = origRunner })

	managers := make(map[string]*portfwd.Manager)

	if err := Tick(ctx, stateDir, managers); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if len(applied) != 1 || applied[0] != 3000 {
		t.Errorf("tick 1: want [3000] applied, got %v", applied)
	}
	if len(cancelled) != 0 {
		t.Errorf("tick 1: want no cancels, got %v", cancelled)
	}

	applied = nil
	if err := Tick(ctx, stateDir, managers); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(cancelled) != 1 || cancelled[0] != 3000 {
		t.Errorf("tick 2: want [3000] cancelled, got %v", cancelled)
	}
	if len(applied) != 1 || applied[0] != 4000 {
		t.Errorf("tick 2: want [4000] applied, got %v", applied)
	}
}

// F12-AC1: tick 1 focus sb-A applies A rows; tick 2 sandbox_id="" cancels A rows
// and applies nothing; tick 3 focus.state absent likewise.
func TestTick_UnboundFocusForwardsNothing(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()

	machineJSON := `[{"id":"m1","target":"host1","enabled":true,"session":"s1"}]`
	origExec := ExecCommandContext
	ExecCommandContext = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.Command("printf", "%s", machineJSON)
	}
	t.Cleanup(func() { ExecCommandContext = origExec })

	tick := 0
	fwds := RemoteForwardsState{Forwards: []RemoteForwardEntry{
		{Port: 3000, Sandbox: "sandbox-A", Status: "live"},
		{Port: 4000, Sandbox: "sandbox-B", Status: "live"},
	}}
	origReader := RemoteStateReader
	RemoteStateReader = func(_ context.Context, _, _ string) (*RemoteCombinedState, error) {
		tick++
		switch tick {
		case 1:
			return &RemoteCombinedState{ForwardsState: fwds, FocusState: portfwd.FocusState{SandboxID: "sandbox-A"}}, nil
		case 2:
			return &RemoteCombinedState{ForwardsState: fwds, FocusState: portfwd.FocusState{SandboxID: ""}}, nil
		default:
			return &RemoteCombinedState{ForwardsState: fwds, FocusMissing: true}, nil
		}
	}
	t.Cleanup(func() { RemoteStateReader = origReader })

	var applied, cancelled []uint16
	origListen := ForwarderListenFunc
	ForwarderListenFunc = func(_, addr string) (net.Listener, error) {
		_, portStr, _ := net.SplitHostPort(addr)
		p, _ := strconv.ParseUint(portStr, 10, 16)
		applied = append(applied, uint16(p))
		return &fakeLn{ch: make(chan struct{}), onClose: func() { cancelled = append(cancelled, uint16(p)) }}, nil
	}
	t.Cleanup(func() { ForwarderListenFunc = origListen })

	var ssApplied, ssCancelled []uint16
	origRunner := ForwarderRunner
	ForwarderRunner = makeFakeRun(&ssApplied, &ssCancelled)
	t.Cleanup(func() { ForwarderRunner = origRunner })

	managers := make(map[string]*portfwd.Manager)

	if err := Tick(ctx, stateDir, managers); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if len(applied) != 1 || applied[0] != 3000 {
		t.Errorf("tick 1: want [3000] applied, got %v", applied)
	}
	if len(cancelled) != 0 {
		t.Errorf("tick 1: want no cancels, got %v", cancelled)
	}

	applied, cancelled = nil, nil
	if err := Tick(ctx, stateDir, managers); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(cancelled) != 1 || cancelled[0] != 3000 {
		t.Errorf("tick 2: want [3000] cancelled, got %v", cancelled)
	}
	if len(applied) != 0 {
		t.Errorf("tick 2: want nothing applied, got %v", applied)
	}

	applied, cancelled = nil, nil
	if err := Tick(ctx, stateDir, managers); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("tick 3: want nothing applied, got %v", applied)
	}
	if len(cancelled) != 0 {
		t.Errorf("tick 3: want no cancels, got %v", cancelled)
	}
}

// F2-AC1: one tick issues exactly ONE ssh argv through the ControlMaster
// that names both forwards.state and focus.state.
func TestReadRemoteCombinedState_OneArgvNamesBothFiles(t *testing.T) {
	var calls [][]string
	fakeRunner := func(_ context.Context, argv []string) (string, string, int, error) {
		calls = append(calls, argv)
		return "{\"forwards\":[]}\n---nexus3-focus---\n", "", 0, nil
	}
	_, err := readRemoteCombinedStateWithRunner(context.Background(), "/fake.ctl", "myhost", fakeRunner)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 ssh call, got %d", len(calls))
	}
	last := calls[0][len(calls[0])-1]
	if !strings.Contains(last, "forwards.state") {
		t.Errorf("argv %q does not name forwards.state", last)
	}
	if !strings.Contains(last, "focus.state") {
		t.Errorf("argv %q does not name focus.state", last)
	}
}

// D-16: absent focus.state and sandbox_id=="" both forward nothing.
func TestFocusFromCombined_FallbackCases(t *testing.T) {
	cases := []struct {
		name     string
		combined RemoteCombinedState
	}{
		{
			name:     "missing focus.state",
			combined: RemoteCombinedState{FocusMissing: true},
		},
		{
			name:     "sandbox_id empty",
			combined: RemoteCombinedState{FocusState: portfwd.FocusState{SandboxID: ""}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandboxID := focusFromCombined(&tc.combined)
			if sandboxID != "" {
				t.Errorf("%s: got %q, want \"\" (forward nothing)", tc.name, sandboxID)
			}
		})
	}
}

// MUTATION TARGET: remove remoteFocusStateFileShell() from RemoteStateReadCommand
// → "focus.state" absent from lastArg → RED.
func TestReadRemoteCombinedState_ArgvContainsFocusStatePath(t *testing.T) {
	var capturedArgv []string
	fakeRunner := func(_ context.Context, argv []string) (string, string, int, error) {
		capturedArgv = argv
		return "{\"forwards\":[]}\n---nexus3-focus---\n", "", 0, nil
	}
	_, err := readRemoteCombinedStateWithRunner(context.Background(), "/fake.ctl", "myhost", fakeRunner)
	if err != nil {
		t.Fatal(err)
	}
	if len(capturedArgv) == 0 {
		t.Fatal("runner was not called")
	}
	if capturedArgv[0] != "ssh" {
		t.Errorf("argv[0]=%q, want ssh", capturedArgv[0])
	}
	last := capturedArgv[len(capturedArgv)-1]
	if !strings.Contains(last, "focus.state") {
		t.Errorf("remote command %q does not name focus.state", last)
	}
	if !strings.Contains(last, "forwards.state") {
		t.Errorf("remote command %q does not name forwards.state", last)
	}
}

func TestParseRemoteCombinedState_FocusFilters(t *testing.T) {
	fwdJSON := `{"forwards":[{"port":3000,"sandbox":"sb1","status":"live"},{"port":4000,"sandbox":"sb2","status":"live"}]}`
	focusJSON := `{"workspace_id":"w1","sandbox_id":"sb1","session":"","updated_at":"` + time.Now().Format(time.RFC3339) + `"}`
	input := fwdJSON + "\n---nexus3-focus---\n" + focusJSON

	combined, err := parseRemoteCombinedState(input)
	if err != nil {
		t.Fatal(err)
	}
	if combined.FocusMissing {
		t.Error("focus.state present: FocusMissing must be false")
	}
	if combined.FocusState.SandboxID != "sb1" {
		t.Errorf("FocusState.SandboxID: got %q, want sb1", combined.FocusState.SandboxID)
	}

	sandboxID := focusFromCombined(combined)
	if sandboxID != "sb1" {
		t.Errorf("focusFromCombined: got %q, want sb1", sandboxID)
	}

	filtered := filterToFocused(combined.ForwardsState.Forwards, sandboxID)
	if len(filtered) != 1 || filtered[0].Port != 3000 {
		t.Errorf("filter to sb1: got %v, want [{3000 sb1 live}]", filtered)
	}
}

// F10 live scenario (Mac minion 2026-09-17): the client restarts while the
// ControlMaster (pid 40518) survives with `-L 3000` from the previous client
// and no applied.json exists. Focus A then B must cancel :3000.
func TestTick_RestartAdoptsForwardLeftOnSurvivingMaster(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()

	machineJSON := `[{"id":"m1","target":"host1","enabled":true,"session":"s1"}]`
	origExec := ExecCommandContext
	ExecCommandContext = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.Command("printf", "%s", machineJSON)
	}
	t.Cleanup(func() { ExecCommandContext = origExec })

	tick := 0
	fwds := RemoteForwardsState{Forwards: []RemoteForwardEntry{
		{Port: 3000, Sandbox: "sandbox-A", Status: "live"},
		{Port: 4000, Sandbox: "sandbox-B", Status: "live"},
	}}
	origReader := RemoteStateReader
	RemoteStateReader = func(_ context.Context, _, _ string) (*RemoteCombinedState, error) {
		tick++
		sb := "sandbox-A"
		if tick > 1 {
			sb = "sandbox-B"
		}
		return &RemoteCombinedState{ForwardsState: fwds, FocusState: portfwd.FocusState{SandboxID: sb}}, nil
	}
	t.Cleanup(func() { RemoteStateReader = origReader })

	var applied, cancelled []uint16
	inner := makeFakeRun(&applied, &cancelled)
	origRunner := ForwarderRunner
	ForwarderRunner = func(ctx context.Context, argv []string) (string, string, int, error) {
		if len(argv) >= 3 && argv[1] == "-O" && argv[2] == "check" {
			return "", "Master running (pid=40518)\n", 0, nil
		}
		if argv[0] == "ss" {
			return "LISTEN 0 128 127.0.0.1:3000 0.0.0.0:* users:((\"ssh\",pid=40518,fd=5))\n", "", 0, nil
		}
		return inner(ctx, argv)
	}
	t.Cleanup(func() { ForwarderRunner = origRunner })

	managers := make(map[string]*portfwd.Manager)
	if err := Tick(ctx, stateDir, managers); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("tick 1: 3000 already on master must not be re-forwarded, got %v", applied)
	}
	if len(cancelled) != 0 {
		t.Errorf("tick 1: want no cancels, got %v", cancelled)
	}
	if err := Tick(ctx, stateDir, managers); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(cancelled) != 1 || cancelled[0] != 3000 {
		t.Errorf("tick 2: stale 3000 from the previous client must be cancelled on focus B, got %v", cancelled)
	}
}

func TestF18AC4_TickInterval1s(t *testing.T) {
	if TickInterval != time.Second {
		t.Fatalf("F18-AC4: TickInterval must be 1s, got %v", TickInterval)
	}
}
