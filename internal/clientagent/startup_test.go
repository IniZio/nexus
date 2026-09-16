package clientagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

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
	if strings.TrimSpace(string(out)) != `{"forwards":[]}` {
		t.Errorf("absent state must read as empty forwards, got %q", out)
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

	got := filterToFocused(rows, "sandbox-A", false)
	if len(got) != 1 || got[0].Port != 3000 {
		t.Errorf("focused on A: got %v", got)
	}

	got = filterToFocused(rows, "", false)
	if len(got) != 0 {
		t.Errorf("no focused workspace: want nil/empty, got %v", got)
	}

	got = filterToFocused(rows, "", true)
	if len(got) != 2 {
		t.Errorf("fallback: want all 2 rows, got %v", got)
	}

	got = filterToFocused(rows, "sandbox-A", true)
	if len(got) != 2 {
		t.Errorf("fallback overrides handle: want all 2 rows, got %v", got)
	}
}

func TestParseFocusedWorkspaceID(t *testing.T) {
	data := []byte(`{"result":{"workspaces":[
		{"workspace_id":"w1","focused":false},
		{"workspace_id":"w2","focused":true},
		{"workspace_id":"w3","focused":false}
	]}}`)
	id, err := parseFocusedWorkspaceID(data)
	if err != nil {
		t.Fatal(err)
	}
	if id != "w2" {
		t.Errorf("got %q, want %q", id, "w2")
	}

	none := []byte(`{"result":{"workspaces":[{"workspace_id":"w1","focused":false}]}}`)
	id, err = parseFocusedWorkspaceID(none)
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Errorf("no focused workspace: got %q, want empty", id)
	}
}

func TestParseHandleFromSpaceList(t *testing.T) {
	output := "label=foo\tworkspace_id=w1\thandle=worktree/main\tsandbox_id=abc\tpane_id=p1\n" +
		"label=bar\tworkspace_id=w2\thandle=worktree/dev\tsandbox_id=def\tpane_id=p2\n"

	if got := parseHandleFromSpaceList(output, "w1"); got != "worktree/main" {
		t.Errorf("w1: got %q, want %q", got, "worktree/main")
	}
	if got := parseHandleFromSpaceList(output, "w2"); got != "worktree/dev" {
		t.Errorf("w2: got %q, want %q", got, "worktree/dev")
	}
	if got := parseHandleFromSpaceList(output, "w99"); got != "" {
		t.Errorf("missing workspace: got %q, want empty", got)
	}
	if got := parseHandleFromSpaceList("(no herdr space bindings)\n", "w1"); got != "" {
		t.Errorf("no bindings line: got %q, want empty", got)
	}
}

func TestTick_FocusScoping(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()

	machineJSON := `[{"id":"m1","target":"host1","enabled":true,"session":"s1"}]`
	origExec := ExecCommandContext
	ExecCommandContext = func(_ context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("printf", "%s", machineJSON)
	}
	t.Cleanup(func() { ExecCommandContext = origExec })

	origReader := RemoteStateReader
	RemoteStateReader = func(_ context.Context, _, _ string) (*RemoteForwardsState, error) {
		return &RemoteForwardsState{Forwards: []RemoteForwardEntry{
			{Port: 3000, Sandbox: "sandbox-A", Status: "live"},
			{Port: 4000, Sandbox: "sandbox-B", Status: "live"},
		}}, nil
	}
	t.Cleanup(func() { RemoteStateReader = origReader })

	tick := 0
	origResolver := DefaultFocusResolver
	DefaultFocusResolver = func(_ context.Context, _, _, _, _ string, _ portfwd.Runner) (string, bool) {
		tick++
		if tick == 1 {
			return "sandbox-A", false
		}
		return "sandbox-B", false
	}
	t.Cleanup(func() { DefaultFocusResolver = origResolver })

	var applied, cancelled []uint16
	fakeRun := func(_ context.Context, argv []string) (string, string, int, error) {
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
							applied = append(applied, uint16(p))
						} else {
							cancelled = append(cancelled, uint16(p))
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
	origRunner := ForwarderRunner
	ForwarderRunner = fakeRun
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

func TestTick_FocusFallback_UsesAllRows(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()

	machineJSON := `[{"id":"m1","target":"host1","enabled":true,"session":"s1"}]`
	origExec := ExecCommandContext
	ExecCommandContext = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.Command("printf", "%s", machineJSON)
	}
	t.Cleanup(func() { ExecCommandContext = origExec })

	origReader := RemoteStateReader
	RemoteStateReader = func(_ context.Context, _, _ string) (*RemoteForwardsState, error) {
		return &RemoteForwardsState{Forwards: []RemoteForwardEntry{
			{Port: 3000, Sandbox: "sandbox-A", Status: "live"},
			{Port: 4000, Sandbox: "sandbox-B", Status: "live"},
		}}, nil
	}
	t.Cleanup(func() { RemoteStateReader = origReader })

	origResolver := DefaultFocusResolver
	DefaultFocusResolver = func(_ context.Context, _, _, _, _ string, _ portfwd.Runner) (string, bool) {
		return "", true // fallback
	}
	t.Cleanup(func() { DefaultFocusResolver = origResolver })

	var applied []uint16
	fakeRun := func(_ context.Context, argv []string) (string, string, int, error) {
		if len(argv) >= 3 && argv[1] == "-O" && argv[2] == "check" {
			return "", "", 0, nil
		}
		if len(argv) >= 3 && argv[1] == "-O" && argv[2] == "forward" {
			for i, a := range argv {
				if a == "-L" && i+1 < len(argv) {
					portStr, _, _ := strings.Cut(argv[i+1], ":")
					p, _ := strconv.ParseUint(portStr, 10, 16)
					applied = append(applied, uint16(p))
				}
			}
			return "", "", 0, nil
		}
		if len(argv) >= 2 && argv[1] == "-ltn" {
			return "", "", 0, nil
		}
		return "", "", 0, nil
	}
	origRunner := ForwarderRunner
	ForwarderRunner = fakeRun
	t.Cleanup(func() { ForwarderRunner = origRunner })

	managers := make(map[string]*portfwd.Manager)
	if err := Tick(ctx, stateDir, managers); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(applied) != 2 {
		t.Errorf("fallback: want both rows applied, got %v", applied)
	}
}

func TestResolveFocusedWorkspaceID_SessionArgs(t *testing.T) {
	var capturedArgs []string
	origExec := ExecCommandContext
	ExecCommandContext = func(_ context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = args
		resp := `{"result":{"workspaces":[{"workspace_id":"w1","focused":true}]}}`
		return exec.Command("printf", "%s", resp)
	}
	t.Cleanup(func() { ExecCommandContext = origExec })

	ctx := context.Background()
	id, err := resolveFocusedWorkspaceID(ctx, "herdr", "mysession")
	if err != nil {
		t.Fatal(err)
	}
	if id != "w1" {
		t.Errorf("got %q, want w1", id)
	}
	wantPrefix := fmt.Sprintf("--session %s workspace list", "mysession")
	if !strings.Contains(strings.Join(capturedArgs, " "), wantPrefix) {
		t.Errorf("args %v missing --session prefix", capturedArgs)
	}

	id, err = resolveFocusedWorkspaceID(ctx, "herdr", "")
	if err != nil {
		t.Fatal(err)
	}
	if id != "w1" {
		t.Errorf("got %q, want w1", id)
	}
	for _, a := range capturedArgs {
		if a == "--session" {
			t.Errorf("empty session: --session must not appear in args %v", capturedArgs)
		}
	}
}

func TestRemoteNexus3HerdrListCmd_PathFallback(t *testing.T) {
	cmd := remoteNexus3HerdrListCmd()
	if !strings.Contains(cmd, `"$HOME/.local/bin/nexus3"`) {
		t.Errorf("command does not try $HOME/.local/bin/nexus3 first: %q", cmd)
	}
	if !strings.Contains(cmd, "|| nexus3 herdr list") {
		t.Errorf("command has no bare-name fallback: %q", cmd)
	}
}

func TestParseHandleFromSpaceList_ContractFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/herdr-list.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := parseHandleFromSpaceList(string(data), "ws-abc"); got != "project/worktree-feature" {
		t.Errorf("contract ws-abc: got %q, want %q", got, "project/worktree-feature")
	}
	if got := parseHandleFromSpaceList(string(data), "ws-def"); got != "other/worktree-main" {
		t.Errorf("contract ws-def: got %q, want %q", got, "other/worktree-main")
	}
	if got := parseHandleFromSpaceList(string(data), "ws-missing"); got != "" {
		t.Errorf("missing workspace: got %q, want empty", got)
	}
}

func TestParseFocusedWorkspaceID_ContractFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/workspace-list.json")
	if err != nil {
		t.Fatal(err)
	}
	id, err := parseFocusedWorkspaceID(data)
	if err != nil {
		t.Fatal(err)
	}
	if id != "ws-def" {
		t.Errorf("contract: got %q, want %q", id, "ws-def")
	}
}

func TestWarnFallbackOnce_FiresOnce(t *testing.T) {
	var warned int
	target, kind := t.Name(), "workspace"
	fallbackWarned.Delete(target + "\x00" + kind)

	for i := 0; i < 3; i++ {
		warnFallbackOnce(target, kind, "test warn", "i", i)
	}

	if _, ok := fallbackWarned.Load(target + "\x00" + kind); !ok {
		t.Error("fallbackWarned key should be set after first warn")
	}

	clearFallback(target, kind)
	if _, ok := fallbackWarned.Load(target + "\x00" + kind); ok {
		t.Error("fallbackWarned key should be cleared after clearFallback")
	}
	_ = warned
}
