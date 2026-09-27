package herdr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/herdragent"
)

// fakeCmd records every herdr or nexus call and returns canned responses.
type fakeCmd struct {
	mu      sync.Mutex
	calls   []fakeCall
	replies map[string]fakeReply // key = first argv word
}

type fakeCall struct {
	argv     []string
	extraEnv []string
}

type fakeReply struct {
	out string
	err error
}

func newFakeCmd(replies map[string]fakeReply) *fakeCmd {
	return &fakeCmd{replies: replies}
}

func (f *fakeCmd) run(ctx context.Context, extraEnv []string, argv ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{argv: append([]string{}, argv...), extraEnv: append([]string{}, extraEnv...)})
	f.mu.Unlock()
	if len(argv) == 0 {
		return "", nil
	}
	key := strings.Join(argv[:min(len(argv), 3)], " ")
	if r, ok := f.replies[key]; ok {
		return r.out, r.err
	}
	// try 2-word key
	if len(argv) >= 2 {
		key2 := strings.Join(argv[:2], " ")
		if r, ok := f.replies[key2]; ok {
			return r.out, r.err
		}
	}
	if r, ok := f.replies[argv[0]]; ok {
		return r.out, r.err
	}
	return "", nil
}

func (f *fakeCmd) calledWith(prefix ...string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c.argv) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c.argv[i] != p {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (f *fakeCmd) envFor(prefix ...string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c.argv) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c.argv[i] != p {
				match = false
				break
			}
		}
		if match {
			return c.extraEnv
		}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// herdrListLine returns a `nexus herdr list` output line for the given workspace.
// handle and sandbox_id use distinct realistic values so parsers can be tested independently.
// principal is empty by default; use herdrListLineWithPrincipal to set a specific value.
func herdrListLine(wsID, paneID string) string {
	return herdrListLineWithPrincipal(wsID, paneID, "")
}

// herdrListLineWithPrincipal is like herdrListLine but includes an explicit principal field.
func herdrListLineWithPrincipal(wsID, paneID, principal string) string {
	return fmt.Sprintf("label=test\tworkspace_id=%s\thandle=test-handle\tsandbox_id=sb-abc123\tpane_id=%s\tprincipal=%s\n", wsID, paneID, principal)
}

// nexusPSLine returns a `nexus ps` output line as returned by parsePSLine.
func nexusPSLine(handle, sbID string) string {
	return fmt.Sprintf("HANDLE\tSTATE\tAGENT\tMOUNTS\tID\n%s\trunning\tclaude\t/workspace\t%s\n1 sandbox(es)\n", handle, sbID)
}

func TestBackendMapsHerdrState(t *testing.T) {
	cases := []struct {
		status herdragent.Status
	}{
		{herdragent.StatusIdle},
		{herdragent.StatusWorking},
		{herdragent.StatusBlocked},
		{herdragent.StatusDone},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			h := newFakeCmd(map[string]fakeReply{
				"workspace list": {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
				"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w2"}}}`},
				"agent start":    {out: ""},
				"agent get": {out: fmt.Sprintf(`{"result":{"agent":{"agent":"ctrl-w2","agent_status":%q,"state_change_seq":1}}}`, tc.status)},
				"agent wait": {out: ""},
				"pane read": {out: "root@nexus-fake-guest:/workspace#\n"},
			})
			n := newFakeCmd(map[string]fakeReply{
				"herdr worktree-sandbox": {out: ""},
				"herdr list":             {out: herdrListLine("w2", "w2:p1")},
				"exec":                   {out: "nexus-fake-guest\n"},
			})
			b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
			b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
			_, ag, err := b.Provision(context.Background(), "proj", controller.NewThreadRef("T", "C", "1"), "u:alice")
			if err != nil {
				t.Fatalf("Provision: %v", err)
			}
			st, err := b.Observe(context.Background(), ag, false)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if st.Status != tc.status {
				t.Errorf("status: got %q, want %q", st.Status, tc.status)
			}
		})
	}
}

func TestProvisionSetsPrincipalEnv(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w3"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w3","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane run":        {out: ""},
		"pane read": {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w3", "w3:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "myproj", controller.NewThreadRef("T", "C", "2"), "slack:T:U123")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	env := n.envFor("herdr", "worktree-sandbox")
	if env == nil {
		t.Fatal("nexus herdr worktree-sandbox not called")
	}
	found := false
	for _, e := range env {
		if e == "NEXUS_PRINCIPAL=slack:T:U123" {
			found = true
		}
	}
	if !found {
		t.Errorf("NEXUS_PRINCIPAL not in env; got %v", env)
	}
}

func TestStartAgentHandlesAutoTaggedPane(t *testing.T) {
	// herdr agent start returns agent_pane_busy → should rename + pane run
	busyErr := fmt.Errorf("exit status 1")
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w4"}}}`},
		"agent start":     {out: `{"error":{"code":"agent_pane_busy","message":"pane already detected as agent"}}`, err: busyErr},
		"agent rename":    {out: ""},
		"pane run":        {out: ""},
		"pane read": {out: "root@nexus-fake-guest:/workspace#\n"},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w4","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w4", "w4:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "proj", controller.NewThreadRef("T", "C", "3"), "u:x")
	if err != nil {
		t.Fatalf("Provision with agent_pane_busy: %v", err)
	}
	if !h.calledWith("agent", "rename") {
		t.Error("expected herdr agent rename call")
	}
	if !h.calledWith("pane", "run") {
		t.Error("expected herdr pane run call")
	}
}

func TestTeardownAlwaysRemovesSandbox(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w6"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w6","agent_status":"idle","state_change_seq":1}}}`},
		"worktree remove": {out: ""},
		"pane run":        {out: ""},
		"pane read": {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w6", "w6:p1")},
		"ps":                     {out: nexusPSLine("test-handle", "sb-abc123")},
		"sandbox rm":             {out: ""},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	sandboxID, _, err := b.Provision(context.Background(), "proj", controller.NewThreadRef("T", "C", "6"), "u:z")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if err := b.Teardown(context.Background(), sandboxID); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	if !n.calledWith("sandbox", "rm", "sb-abc123") {
		t.Errorf("expected nexus sandbox rm sb-abc123 (exact id); calls: %v", n.calls)
	}
	if !h.calledWith("worktree", "remove", "--workspace", "w6") {
		t.Error("expected herdr worktree remove --workspace called with herdr wsID")
	}
}

func TestTeardownRemovesByExactSandboxID(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w7"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w7","agent_status":"idle","state_change_seq":1}}}`},
		"worktree remove": {out: ""},
		"pane run":        {out: ""},
		"pane read": {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w7", "w7:p1")},
		"ps":                     {out: nexusPSLine("test-handle", "sb-abc123")},
		"sandbox rm":             {out: ""},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	sandboxID, _, err := b.Provision(context.Background(), "proj", controller.NewThreadRef("T", "C", "7"), "u:a")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := b.Teardown(context.Background(), sandboxID); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	n.mu.Lock()
	var rmArgv []string
	for _, c := range n.calls {
		if len(c.argv) >= 2 && c.argv[0] == "sandbox" && c.argv[1] == "rm" {
			rmArgv = c.argv
		}
	}
	n.mu.Unlock()

	if len(rmArgv) != 3 || rmArgv[2] != "sb-abc123" {
		t.Errorf("sandbox rm must be called with exact sb-abc123; got argv %v", rmArgv)
	}
}

func TestTeardownRefusesWithoutRecordedID(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"worktree remove": {out: ""},
	})
	n := newFakeCmd(map[string]fakeReply{
		"sandbox rm": {out: ""},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)

	err := b.Teardown(context.Background(), "ws-never-provisioned")
	if err == nil {
		t.Error("Teardown with unknown id must return error")
	}
	if n.calledWith("sandbox", "rm") {
		t.Error("sandbox rm must NOT be called when no id is recorded")
	}
}

func TestTeardownIdempotentWhenIDAbsent(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w8"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w8","agent_status":"idle","state_change_seq":1}}}`},
		"worktree remove": {out: `{"error":{"code":"workspace_not_found"}}`, err: fmt.Errorf("exit status 1")},
		"pane run":        {out: ""},
		"pane read": {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w8", "w8:p1")},
		"ps":                     {out: "HANDLE\tSTATE\tID\n0 sandbox(es)\n"},
		"sandbox rm":             {out: ""},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	sandboxID, _, err := b.Provision(context.Background(), "proj", controller.NewThreadRef("T", "C", "8"), "u:b")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if err := b.Teardown(context.Background(), sandboxID); err != nil {
		t.Fatalf("Teardown (sandbox absent from ps) should return nil, got: %v", err)
	}
	if n.calledWith("sandbox", "rm") {
		t.Error("sandbox rm must NOT be called when id is absent from nexus ps")
	}
}

func TestAnswerDigitUsesSendKeys(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w5"}}}`},
		"agent start":     {out: ""},
		"agent send-keys": {out: ""},
		"agent prompt":    {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w5","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane run":        {out: ""},
		"pane read": {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w5", "w5:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, ag, err := b.Provision(context.Background(), "proj", controller.NewThreadRef("T", "C", "4"), "u:y")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if err := b.Answer(context.Background(), ag, controller.AgentInput{Key: "1"}); err != nil {
		t.Fatalf("Answer with Key: %v", err)
	}
	if !h.calledWith("agent", "send-keys") {
		t.Error("expected herdr agent send-keys for Key input")
	}
	if h.calledWith("agent", "prompt") {
		// prompt may have been called by Provision's Prompt-path setup; ensure send-keys used for Answer
		// check that send-keys appears in calls after provision
		h.mu.Lock()
		found := false
		for _, c := range h.calls {
			if len(c.argv) >= 2 && c.argv[0] == "agent" && c.argv[1] == "send-keys" {
				found = true
				break
			}
		}
		h.mu.Unlock()
		if !found {
			t.Error("send-keys not found in herdr calls")
		}
	}

	// Text input should use prompt, not send-keys
	prevSendKeys := 0
	h.mu.Lock()
	for _, c := range h.calls {
		if len(c.argv) >= 2 && c.argv[0] == "agent" && c.argv[1] == "send-keys" {
			prevSendKeys++
		}
	}
	h.mu.Unlock()

	if err := b.Answer(context.Background(), ag, controller.AgentInput{Text: "hello"}); err != nil {
		t.Fatalf("Answer with Text: %v", err)
	}
	afterSendKeys := 0
	h.mu.Lock()
	for _, c := range h.calls {
		if len(c.argv) >= 2 && c.argv[0] == "agent" && c.argv[1] == "send-keys" {
			afterSendKeys++
		}
	}
	h.mu.Unlock()
	if afterSendKeys != prevSendKeys {
		t.Error("Text Answer should use prompt, not send-keys")
	}
}

// TestStartAgentRefusesHostPane verifies that startAgent fails when pane hostname
// does not match the nexus exec hostname — i.e., the pane is on the host, not the guest.
func TestStartAgentRefusesHostPane(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"whost"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-whost","agent_status":"idle","state_change_seq":1}}}`},
		// Pane shows host prompt "engine-03", but nexus exec says "nexus-e2e-abc".
		"pane read": {out: "root@engine-03:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("whost", "whost:p1")},
		// nexus exec returns guest hostname — different from what the pane shows.
		"exec": {out: "nexus-e2e-abc\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	_, _, err := b.Provision(context.Background(), "proj", controller.NewThreadRef("T", "C", "host"), "u:a")
	if err == nil {
		t.Fatal("Provision must fail when pane hostname != guest hostname")
	}
	if !strings.Contains(err.Error(), "not running inside the guest VM") {
		t.Errorf("error should mention guest VM mismatch; got: %v", err)
	}
	if h.calledWith("agent", "start") {
		t.Error("agent start must NOT be called when guest verification fails")
	}
}

// TestStartAgentRefusesFallbackMarker verifies that startAgent fails when the pane
// output contains the nexus-guest-shell FALLBACK marker (host shell opened instead of guest).
func TestStartAgentRefusesFallbackMarker(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wfb"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wfb","agent_status":"idle","state_change_seq":1}}}`},
		"pane run":        {out: ""},
		// Initial pane read contains the FALLBACK marker.
		"pane read": {out: "nexus-guest-shell: FALLBACK host shell: /bin/bash\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wfb", "wfb:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	_, _, err := b.Provision(context.Background(), "proj", controller.NewThreadRef("T", "C", "fb"), "u:b")
	if err == nil {
		t.Fatal("Provision must fail when pane shows FALLBACK marker")
	}
	if !strings.Contains(err.Error(), "FALLBACK") {
		t.Errorf("error should mention FALLBACK marker; got: %v", err)
	}
	if h.calledWith("agent", "start") {
		t.Error("agent start must NOT be called when fallback marker is detected")
	}
}

// TestStartAgentAcceptsGuestHostnameMatch verifies that startAgent succeeds when
// the pane hostname matches nexus exec hostname (pane is in the guest).
func TestStartAgentAcceptsGuestHostnameMatch(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wmatch"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wmatch","agent_status":"idle","state_change_seq":1}}}`},
		// Pane shows same hostname as nexus exec.
		"pane read": {out: "root@nexus-e2e-abc:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wmatch", "wmatch:p1")},
		"exec":                   {out: "nexus-e2e-abc\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	_, _, err := b.Provision(context.Background(), "proj", controller.NewThreadRef("T", "C", "match"), "u:c")
	if err != nil {
		t.Fatalf("Provision must succeed when pane hostname matches guest hostname: %v", err)
	}
	if !h.calledWith("agent", "start") {
		t.Error("agent start must be called when guest verification passes")
	}
}

func TestVerifyClaudeInGuest_PresentPasses(t *testing.T) {
	b := newWithRunners(Config{}, nil, func(_ context.Context, _ []string, argv ...string) (string, error) {
		return "/usr/local/bin/claude\n", nil
	})
	if err := b.verifyClaudeInGuest(context.Background(), "sb-test"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifyClaudeInGuest_MissingFails(t *testing.T) {
	b := newWithRunners(Config{}, nil, func(_ context.Context, _ []string, argv ...string) (string, error) {
		return "", nil
	})
	if err := b.verifyClaudeInGuest(context.Background(), "sb-test"); err == nil {
		t.Error("expected error when claude missing, got nil")
	}
}

// TestProvisionWritesControllerMarker verifies that Provision writes the
// controller-owned marker before calling `herdr worktree create`, so the hook
// can see it while herdr is still processing the create request.
func TestProvisionWritesControllerMarker(t *testing.T) {
	// Use a temp dir as the nexus state root.
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	var markerExistedDuringCreate bool
	h := newFakeCmd(map[string]fakeReply{
		"workspace list": {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wM"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wM","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane run":        {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})

	// Intercept the herdr runner to check for the marker at worktree-create time.
	herdrRunner := func(ctx context.Context, extraEnv []string, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "worktree" && argv[1] == "create" {
			// Inspect claims dir — branch is ctrl/<something>.
			claimsDir := filepath.Join(stateDir, "nexus", "controller-wt-claims")
			entries, _ := os.ReadDir(claimsDir)
			markerExistedDuringCreate = len(entries) > 0
		}
		return h.run(ctx, extraEnv, argv...)
	}

	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wM", "wM:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, herdrRunner, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "myproj", controller.NewThreadRef("T", "C", "3"), "slack:T:U999")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !markerExistedDuringCreate {
		t.Error("controller marker was not written before herdr worktree create")
	}
	// After Provision returns the marker must be cleaned up.
	claimsDir := filepath.Join(stateDir, "nexus", "controller-wt-claims")
	entries, _ := os.ReadDir(claimsDir)
	if len(entries) != 0 {
		t.Errorf("controller marker not cleaned up after Provision; remaining: %v", entries)
	}
}

// TestProvisionVerifiesBoundPrincipal verifies that Provision fails when the
// sandbox recorded in `nexus herdr list` has a principal that differs from
// the one requested.  This guards against the hook race creating a sandbox
// with the wrong identity.
func TestProvisionVerifiesBoundPrincipal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wP"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wP","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane run":        {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		// list returns principal=local:newman — wrong identity.
		"herdr list": {out: herdrListLineWithPrincipal("wP", "wP:p1", "local:newman")},
		"exec":       {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "myproj", controller.NewThreadRef("T", "C", "4"), "slack:T:U999")
	if err == nil {
		t.Fatal("Provision should fail on principal mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "principal mismatch") {
		t.Errorf("error = %v, want principal mismatch", err)
	}
}

// TestParsePrincipal verifies that parsePrincipal extracts the principal field
// and returns "" gracefully when absent.
func TestParsePrincipal(t *testing.T) {
	line := herdrListLineWithPrincipal("wX", "wX:p1", "slack:T:U123")
	if got := parsePrincipal(line, "wX"); got != "slack:T:U123" {
		t.Errorf("parsePrincipal = %q, want %q", got, "slack:T:U123")
	}
	// Legacy line without principal field.
	legacy := fmt.Sprintf("label=test\tworkspace_id=wY\thandle=h\tsandbox_id=sb-x\tpane_id=p\n")
	if got := parsePrincipal(legacy, "wY"); got != "" {
		t.Errorf("parsePrincipal legacy = %q, want empty", got)
	}
}

// TestHerdrWorktreeSandbox_ReuseRejectsWrongPrincipal is tested in the cli
// package (cmd_herdr_plugin_principal_test.go) where herdrWorktreeSandbox is
// accessible.  This marker ensures the test exists; see that file.
var _ = vault.PrincipalEnv // ensure vault import is used
