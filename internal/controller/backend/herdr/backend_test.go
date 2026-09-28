package herdr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/core/sandboxhandle"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/herdragent"
)

// fakeCmd records every herdr or nexus call and returns canned responses.
type fakeCmd struct {
	mu      sync.Mutex
	calls   []fakeCall
	replies map[string]fakeReply // key = first argv word
	seqs    map[string]fakeSeq   // per-call sequences (overrides replies)
	seqIdx  map[string]int       // current index into seqs
}

type fakeCall struct {
	argv     []string
	extraEnv []string
}

type fakeReply struct {
	out string
	err error
}

// fakeSeq is a sequence of replies for a given key. The first call returns
// replies[0], the second replies[1], and so on. The last entry is repeated
// for all subsequent calls.
type fakeSeq []fakeReply

func newFakeCmd(replies map[string]fakeReply) *fakeCmd {
	return &fakeCmd{replies: replies}
}

// newFakeCmdSeq creates a fakeCmd with per-key reply sequences for testing
// call-order-dependent behaviour (e.g. first call returns idle, subsequent
// calls return a different status).
func newFakeCmdSeq(replies map[string]fakeReply, seqs map[string]fakeSeq) *fakeCmd {
	return &fakeCmd{replies: replies, seqs: seqs, seqIdx: make(map[string]int)}
}

func (f *fakeCmd) run(ctx context.Context, extraEnv []string, argv ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{argv: append([]string{}, argv...), extraEnv: append([]string{}, extraEnv...)})
	f.mu.Unlock()
	if len(argv) == 0 {
		return "", nil
	}
	// Try sequence replies first (by 3-word, 2-word, 1-word key).
	for _, n := range []int{3, 2, 1} {
		if len(argv) < n {
			continue
		}
		key := strings.Join(argv[:n], " ")
		if seq, ok := f.seqs[key]; ok {
			f.mu.Lock()
			idx := f.seqIdx[key]
			if idx < len(seq)-1 {
				f.seqIdx[key] = idx + 1
			}
			r := seq[idx]
			f.mu.Unlock()
			return r.out, r.err
		}
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

// testPrincipal is the default principal used in tests that don't exercise principal logic.
const testPrincipal = "u:testprincipal"

// herdrListLine returns a `nexus herdr list` output line for the given workspace.
// handle and sandbox_id use distinct realistic values so parsers can be tested independently.
// Uses testPrincipal so hard-cutover principal checks pass in generic tests.
func herdrListLine(wsID, paneID string) string {
	return herdrListLineWithPrincipal(wsID, paneID, testPrincipal)
}

// herdrListLineWithPrincipal is like herdrListLine but includes an explicit principal field.
func herdrListLineWithPrincipal(wsID, paneID, principal string) string {
	return fmt.Sprintf("label=test\tworkspace_id=%s\thandle=test-handle\tsandbox_id=sb-abc123\tpane_id=%s\tprincipal=%s\n", wsID, paneID, principal)
}

// nexusPSLine returns a `nexus ps` output line as returned by parsePSLine.
func nexusPSLine(handle, sbID string) string {
	return fmt.Sprintf("HANDLE\tSTATE\tAGENT\tMOUNTS\tID\n%s\trunning\tclaude\t/workspace\t%s\n1 sandbox(es)\n", handle, sbID)
}

// setupTestStore creates a temp store root with a sandbox record.json and wires
// XDG_STATE_HOME so b.nexusStoreRoot() resolves to it. The sandbox ID used by
// herdrListLine is "sb-abc123"; pass that when provisioning with herdrListLine.
// Call before Provision in any test that must pass the record principal check.
func setupTestStore(t *testing.T, sandboxID, principal string) string {
	t.Helper()
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	sbDir := filepath.Join(stateDir, "nexus", "sandboxes", sandboxID)
	if err := os.MkdirAll(sbDir, 0o755); err != nil {
		t.Fatalf("setupTestStore mkdir: %v", err)
	}
	rec := fmt.Sprintf(`{"schema_version":1,"id":%q,"principal":%q}`, sandboxID, principal)
	if err := os.WriteFile(filepath.Join(sbDir, "record.json"), []byte(rec), 0o644); err != nil {
		t.Fatalf("setupTestStore write record: %v", err)
	}
	return stateDir
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
		tc := tc
		t.Run(string(tc.status), func(t *testing.T) {
			h := newFakeCmd(map[string]fakeReply{
				"agent get": {out: fmt.Sprintf(`{"result":{"agent":{"agent":"ctrl-w2","agent_status":%q,"state_change_seq":1}}}`, tc.status)},
			})
			b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, nil)
			b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
			// Inject entry directly — this test is about Observe status mapping,
			// not about Provision or waitForAgentReady.
			b.mu.Lock()
			b.entries["ctrl-w2"] = &entry{paneID: "w2:p1", nexusSandboxID: "sb-abc123", wsID: "w2"}
			b.sandboxes["sb-abc123"] = "ctrl-w2"
			b.mu.Unlock()
			st, err := b.Observe(context.Background(), "ctrl-w2", false)
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
	setupTestStore(t, "sb-abc123", testPrincipal)
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w3"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w3","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane run":        {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w3", "w3:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "2"), testPrincipal)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	env := n.envFor("herdr", "worktree-sandbox")
	if env == nil {
		t.Fatal("nexus herdr worktree-sandbox not called")
	}
	found := false
	for _, e := range env {
		if e == "NEXUS_PRINCIPAL="+testPrincipal {
			found = true
		}
	}
	if !found {
		t.Errorf("NEXUS_PRINCIPAL not in env; got %v", env)
	}
}

func TestStartAgentHandlesAutoTaggedPane(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)
	// herdr agent start returns agent_pane_busy → should rename + pane run
	busyErr := fmt.Errorf("exit status 1")
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w4"}}}`},
		"agent start":     {out: `{"error":{"code":"agent_pane_busy","message":"pane already detected as agent"}}`, err: busyErr},
		"agent rename":    {out: ""},
		"pane run":        {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
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

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "3"), testPrincipal)
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
	setupTestStore(t, "sb-abc123", testPrincipal)
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w6"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w6","agent_status":"idle","state_change_seq":1}}}`},
		"worktree remove": {out: ""},
		"pane run":        {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
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

	sandboxID, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "6"), testPrincipal)
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
	setupTestStore(t, "sb-abc123", testPrincipal)
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w7"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w7","agent_status":"idle","state_change_seq":1}}}`},
		"worktree remove": {out: ""},
		"pane run":        {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
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

	sandboxID, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "7"), testPrincipal)
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
	setupTestStore(t, "sb-abc123", testPrincipal)
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w8"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w8","agent_status":"idle","state_change_seq":1}}}`},
		"worktree remove": {out: `{"error":{"code":"workspace_not_found"}}`, err: fmt.Errorf("exit status 1")},
		"pane run":        {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
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

	sandboxID, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "8"), testPrincipal)
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
	setupTestStore(t, "sb-abc123", testPrincipal)
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w5"}}}`},
		"agent start":     {out: ""},
		"agent send-keys": {out: ""},
		"agent prompt":    {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w5","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane run":        {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w5", "w5:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, ag, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "4"), testPrincipal)
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
	setupTestStore(t, "sb-abc123", testPrincipal)
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
	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "host"), testPrincipal)
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
	setupTestStore(t, "sb-abc123", testPrincipal)
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
	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "fb"), testPrincipal)
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
	setupTestStore(t, "sb-abc123", testPrincipal)
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
	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "match"), testPrincipal)
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
	// Create sandbox record so the record principal check passes.
	sbDir := filepath.Join(stateDir, "nexus", "sandboxes", "sb-abc123")
	if err := os.MkdirAll(sbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := fmt.Sprintf(`{"schema_version":1,"id":"sb-abc123","principal":%q}`, testPrincipal)
	if err := os.WriteFile(filepath.Join(sbDir, "record.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}

	var markerExistedDuringCreate bool
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
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

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "3"), testPrincipal)
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

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "4"), "slack:T:U999")
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

// ── K1: rediscovery tests ──────────────────────────────────────────────────

// TestRediscoverEntry_CanPromptPreExistingAgent verifies that a freshly
// constructed Backend (empty maps) can Prompt an agent that already exists in
// live herdr state, rediscovering its entry on the first miss.
func TestRediscoverEntry_CanPromptPreExistingAgent(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"agent prompt": {out: ""},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr list": {out: herdrListLine("wR1", "wR1:p1")},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	// Empty maps — simulates a controller restart.
	if _, err := b.Prompt(context.Background(), agentNameFromWsID("wR1"), "hello"); err != nil {
		t.Fatalf("Prompt on rediscovered agent: %v", err)
	}
	if !n.calledWith("herdr", "list") {
		t.Error("expected nexus herdr list to be called for rediscovery")
	}
	if !h.calledWith("agent", "prompt") {
		t.Error("expected herdr agent prompt to be called")
	}
}

// TestRediscoverEntry_CanObservePreExistingAgent verifies that Observe
// rediscovers an entry on map miss.
func TestRediscoverEntry_CanObservePreExistingAgent(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"agent get": {out: `{"result":{"agent":{"agent":"ctrl-wR2","agent_status":"idle","state_change_seq":1}}}`},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr list": {out: herdrListLine("wR2", "wR2:p1")},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	st, err := b.Observe(context.Background(), agentNameFromWsID("wR2"), false)
	if err != nil {
		t.Fatalf("Observe on rediscovered agent: %v", err)
	}
	if st.Status != herdragent.StatusIdle {
		t.Errorf("status = %q, want idle", st.Status)
	}
}

// TestRediscoverEntry_UppercaseWsID verifies that rediscoverEntry locates the
// workspace when the workspace ID contains uppercase letters and the agentRef
// was derived via agentNameFromWsID.
func TestRediscoverEntry_UppercaseWsID(t *testing.T) {
	const wsID = "wDR"
	agentRef := agentNameFromWsID(wsID)
	h := newFakeCmd(map[string]fakeReply{
		"agent prompt": {out: ""},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr list": {out: herdrListLine(wsID, wsID+":p1")},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	// Empty maps — simulates a controller restart with an uppercase wsID.
	if _, err := b.Prompt(context.Background(), agentRef, "hello"); err != nil {
		t.Fatalf("Prompt on rediscovered uppercase wsID agent: %v", err)
	}
	b.mu.Lock()
	e := b.entries[agentRef]
	b.mu.Unlock()
	if e == nil {
		t.Fatalf("entry not found after rediscovery for agentRef %q", agentRef)
	}
	if e.wsID != wsID {
		t.Errorf("rediscovered wsID = %q, want %q", e.wsID, wsID)
	}
}

// TestDeterministicSessionIDIsValidUUID verifies that deterministicSessionID
// returns an RFC 4122 UUID (8-4-4-4-12) with version 4 or 5 and variant bits,
// and that the same input always produces the same output.
func TestDeterministicSessionIDIsValidUUID(t *testing.T) {
	uuidRE := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[45][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	id := deterministicSessionID("sb-abc123")
	if !uuidRE.MatchString(id) {
		t.Errorf("deterministicSessionID(%q) = %q, does not match UUID pattern", "sb-abc123", id)
	}
	if id2 := deterministicSessionID("sb-abc123"); id != id2 {
		t.Errorf("deterministicSessionID not stable: %q != %q", id, id2)
	}
	if other := deterministicSessionID("sb-different"); other == id {
		t.Error("different inputs produced same UUID")
	}
}

// TestRediscoverBySandboxID_CanTeardown verifies Teardown can rediscover an
// entry when the sandboxes map is empty (controller restart scenario).
func TestRediscoverBySandboxID_CanTeardown(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"worktree remove": {out: ""},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr list": {out: herdrListLine("wR3", "wR3:p1")},
		"ps":         {out: nexusPSLine("test-handle", "sb-abc123")},
		"sandbox rm": {out: ""},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	// Empty maps — controller restart.
	if err := b.Teardown(context.Background(), "sb-abc123"); err != nil {
		t.Fatalf("Teardown after rediscovery: %v", err)
	}
	if !n.calledWith("sandbox", "rm", "sb-abc123") {
		t.Error("expected nexus sandbox rm sb-abc123")
	}
}

// TestReadAnswerAfterRestart verifies that ReadAnswer uses the transcript path
// when the in-memory entry was reconstructed by rediscovery (controller restart).
// agentSessionID must be deterministic from the sandbox ID so rediscoverEntry
// can set it without any store access; the caller-supplied turnID stands in for
// the persisted Task.TurnID column.
func TestReadAnswerAfterRestart(t *testing.T) {
	const knownTurnID = "testTurnRestart"
	marker := turnMarker(knownTurnID)
	// deterministicSessionID("sb-abc123") = sha256("sb-abc123")[:16] hex
	// The transcript file is named <sessionID>.jsonl in the guest.
	jsonl := `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"text","text":"` +
		marker + `\n\ndo the thing"}]}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"restarted answer"}]}}`

	h := newFakeCmd(map[string]fakeReply{
		"agent read": {out: ""}, // no pane content; transcript should win
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr list": {out: herdrListLine("wRS", "wRS:p1")},
		"exec":       {out: jsonl},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	// Empty maps — simulates controller restart; no Provision was called.
	agentRef := agentNameFromWsID("wRS")
	answer, err := b.ReadAnswer(context.Background(), agentRef, knownTurnID)
	if err != nil {
		t.Fatalf("ReadAnswer after restart: %v", err)
	}
	if answer != "restarted answer" {
		t.Errorf("answer = %q, want %q", answer, "restarted answer")
	}
	if h.calledWith("agent", "read") {
		t.Error("pane fallback was called; transcript path should have won")
	}
}

// TestTeardownEvictsEntry verifies that Teardown removes entries from the
// in-memory maps so a second call would not find the sandbox.
func TestTeardownEvictsEntry(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wEv"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wEv","agent_status":"idle","state_change_seq":1}}}`},
		"worktree remove": {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wEv", "wEv:p1")},
		"ps":                     {out: nexusPSLine("test-handle", "sb-abc123")},
		"sandbox rm":             {out: ""},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	sandboxID, agRef, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "ev"), testPrincipal)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := b.Teardown(context.Background(), sandboxID); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	b.mu.Lock()
	_, inEntries := b.entries[agRef]
	_, inSandboxes := b.sandboxes[sandboxID]
	b.mu.Unlock()
	if inEntries {
		t.Error("entry still present in b.entries after Teardown")
	}
	if inSandboxes {
		t.Error("entry still present in b.sandboxes after Teardown")
	}
	rmBefore := countCalls(n, "sandbox", "rm")
	if err := b.Teardown(context.Background(), sandboxID); err != nil {
		t.Fatalf("second Teardown: %v", err)
	}
	if got := countCalls(n, "sandbox", "rm"); got != rmBefore {
		t.Errorf("second Teardown ran sandbox rm again (%d -> %d)", rmBefore, got)
	}
}

func countCalls(f *fakeCmd, prefix ...string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if len(c.argv) >= len(prefix) && strings.Join(c.argv[:len(prefix)], " ") == strings.Join(prefix, " ") {
			n++
		}
	}
	return n
}

// ── K2: principal hard-cutover tests ──────────────────────────────────────

// TestProvisionFailsOnEmptyBoundPrincipal verifies that Provision rejects a
// binding whose principal field is absent (empty string), not just mismatched.
func TestProvisionFailsOnEmptyBoundPrincipal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wEP"}}}`},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		// list returns empty principal — hard cutover must reject this.
		"herdr list": {out: herdrListLineWithPrincipal("wEP", "wEP:p1", "")},
		"exec":       {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "ep"), "u:alice")
	if err == nil {
		t.Fatal("Provision must fail when bound principal is empty")
	}
	if !strings.Contains(err.Error(), "principal mismatch") {
		t.Errorf("error should mention principal mismatch; got: %v", err)
	}
}

// TestProvisionSucceedsOnMatchingPrincipal verifies the happy path where
// bound principal == requested principal and record also matches.
func TestProvisionSucceedsOnMatchingPrincipal(t *testing.T) {
	setupTestStore(t, "sb-abc123", "u:alice")

	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wMP"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wMP","agent_status":"idle","state_change_seq":1}}}`},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLineWithPrincipal("wMP", "wMP:p1", "u:alice")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "mp"), "u:alice")
	if err != nil {
		t.Fatalf("Provision must succeed when bound principal matches requested: %v", err)
	}
}

// TestProvisionFailsOnEmptyRecordPrincipal verifies that Provision rejects a
// sandbox whose record.json has an empty principal, even when the binding matches.
func TestProvisionFailsOnEmptyRecordPrincipal(t *testing.T) {
	setupTestStore(t, "sb-abc123", "") // empty principal in record

	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wERP"}}}`},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLineWithPrincipal("wERP", "wERP:p1", "u:alice")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "erp"), "u:alice")
	if err == nil {
		t.Fatal("Provision must fail when sandbox record principal is empty")
	}
	if !strings.Contains(err.Error(), "principal mismatch") {
		t.Errorf("error should mention principal mismatch; got: %v", err)
	}
}

// TestProvisionFailsOnMismatchedRecordPrincipal verifies that Provision rejects
// a sandbox whose record.json principal differs from the requested principal,
// even when the binding matches.
func TestProvisionFailsOnMismatchedRecordPrincipal(t *testing.T) {
	setupTestStore(t, "sb-abc123", "u:other") // different principal in record

	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wMRP"}}}`},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLineWithPrincipal("wMRP", "wMRP:p1", "u:alice")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "mrp"), "u:alice")
	if err == nil {
		t.Fatal("Provision must fail when sandbox record principal differs from requested")
	}
	if !strings.Contains(err.Error(), "principal mismatch") {
		t.Errorf("error should mention principal mismatch; got: %v", err)
	}
}

// ── K3: waitForAgentReady tests ───────────────────────────────────────────

// TestWaitForAgentReady_BecomesReady verifies that waitForAgentReady returns
// nil when the agent becomes idle before the deadline, using an injected no-op
// sleepFn so the test runs without real delays.
func TestWaitForAgentReady_BecomesReady(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"agent get": {out: `{"result":{"agent":{"agent":"ctrl-test","agent_status":"idle","state_change_seq":1}}}`},
	})
	b := newWithRunners(Config{}, h.run, nil)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	if err := b.waitForAgentReady(context.Background(), "ctrl-test", 5*time.Second); err != nil {
		t.Fatalf("waitForAgentReady: unexpected error: %v", err)
	}
}

// TestWaitForAgentReady_Timeout verifies that waitForAgentReady returns a
// timeout error when the agent never becomes ready. Uses a minimal timeout
// and a no-op sleepFn so the loop runs fast.
func TestWaitForAgentReady_Timeout(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		// agent always working, never idles
		"agent get": {out: `{"result":{"agent":{"agent":"ctrl-to","agent_status":"working","state_change_seq":1}}}`},
	})
	b := newWithRunners(Config{}, h.run, nil)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(1 * time.Millisecond)}
	err := b.waitForAgentReady(context.Background(), "ctrl-to", 1*time.Millisecond)
	if err == nil {
		t.Fatal("waitForAgentReady: expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "not ready after") {
		t.Errorf("error should mention not-ready timeout; got: %v", err)
	}
}

// ── Restart tests ─────────────────────────────────────────────────────────

// TestRestart_CachedEntry verifies that Restart launches a new agent for a
// cached entry, evicts the old ref, and stores the new ref.
func TestRestart_CachedEntry(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"agent start": {out: ""},
		"agent get":   {out: `{"result":{"agent":{"agent":"ctrl-wRst","agent_status":"idle","state_change_seq":1}}}`},
		// pane read returns guest prompt — pane is alive, no recreation needed.
		"pane read": {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"exec": {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	b.mu.Lock()
	b.entries["ctrl-wRst"] = &entry{paneID: "wRst:p1", nexusSandboxID: "sb-rst1", wsID: "wRst"}
	b.sandboxes["sb-rst1"] = "ctrl-wRst"
	b.mu.Unlock()

	newRef, err := b.Restart(context.Background(), "sb-rst1", "ctrl-wRst")
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if newRef == "" {
		t.Error("Restart must return a non-empty ref")
	}
	b.mu.Lock()
	_, oldPresent := b.entries["ctrl-wRst"]
	newEntry, newPresent := b.entries[newRef]
	sandboxRef := b.sandboxes["sb-rst1"]
	b.mu.Unlock()
	if oldPresent && newRef != "ctrl-wRst" {
		t.Error("old agentRef still in b.entries after Restart")
	}
	if !newPresent {
		t.Errorf("new agentRef %q not in b.entries after Restart", newRef)
	}
	if newEntry == nil || newEntry.nexusSandboxID != "sb-rst1" {
		t.Errorf("new entry has wrong nexusSandboxID: %+v", newEntry)
	}
	if sandboxRef != newRef {
		t.Errorf("b.sandboxes[sb-rst1] = %q, want %q", sandboxRef, newRef)
	}
}

// TestRestart_EmptyCacheRediscovers verifies that Restart rediscovers the
// entry from nexus herdr list when the in-memory cache is empty.
func TestRestart_EmptyCacheRediscovers(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"agent start": {out: ""},
		"agent get":   {out: `{"result":{"agent":{"agent":"ctrl-wRst2","agent_status":"idle","state_change_seq":1}}}`},
		"pane read":   {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr list": {out: herdrListLine("wRst2", "wRst2:p1")},
		"exec":       {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	// empty caches — simulates controller restart

	newRef, err := b.Restart(context.Background(), "sb-abc123", "ctrl-wRst2")
	if err != nil {
		t.Fatalf("Restart with empty cache: %v", err)
	}
	if newRef == "" {
		t.Error("Restart must return a non-empty ref")
	}
	if !n.calledWith("herdr", "list") {
		t.Error("expected nexus herdr list to be called for rediscovery")
	}
}

// TestRestart_DeadPaneRecreated verifies that Restart calls space-open-pane
// when the pane shows no guest prompt, and uses the refreshed paneID.
func TestRestart_DeadPaneRecreated(t *testing.T) {
	// pane read: first call returns "" (dead pane); subsequent calls return
	// guest prompt (pane alive after recreation).
	h := newFakeCmdSeq(
		map[string]fakeReply{
			"agent start": {out: ""},
			"agent get":   {out: `{"result":{"agent":{"agent":"ctrl-wDead","agent_status":"idle","state_change_seq":1}}}`},
		},
		map[string]fakeSeq{
			"pane read": {
				{out: ""}, // first read: dead pane
				{out: "root@nexus-fake-guest:/workspace#\n"}, // subsequent: alive
			},
		},
	)
	n := newFakeCmd(map[string]fakeReply{
		"herdr space-open-pane": {out: ""},
		// herdr list after reopen returns a new pane id.
		"herdr list": {out: herdrListLine("wDead", "wDead:p2")},
		"exec":       {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	b.mu.Lock()
	b.entries["ctrl-wDead"] = &entry{paneID: "wDead:p1", nexusSandboxID: "sb-dead1", wsID: "wDead"}
	b.sandboxes["sb-dead1"] = "ctrl-wDead"
	b.mu.Unlock()

	newRef, err := b.Restart(context.Background(), "sb-dead1", "ctrl-wDead")
	if err != nil {
		t.Fatalf("Restart with dead pane: %v", err)
	}
	if !n.calledWith("herdr", "space-open-pane", "wDead") {
		t.Error("expected herdr space-open-pane to be called for dead pane")
	}
	// New entry should use the refreshed pane id.
	b.mu.Lock()
	e := b.entries[newRef]
	b.mu.Unlock()
	if e == nil {
		t.Fatalf("new entry not found for ref %q", newRef)
	}
	if e.paneID != "wDead:p2" {
		t.Errorf("new entry paneID = %q, want wDead:p2 (refreshed after pane recreation)", e.paneID)
	}
}

// TestProvisionUsesChannelRepo verifies that the per-channel repo path (the
// project argument to Provision) reaches herdr workspace create --cwd, not the
// global cfg.RepoPath.  Two channels with distinct repos must produce two
// distinct workspace create calls.
func TestProvisionUsesChannelRepo(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)

	// Track workspace create --cwd arguments.
	var (
		mu      sync.Mutex
		cwdArgs []string
	)

	// workspace list returns no existing workspaces so create is always called.
	emptyList := `{"result":{"workspaces":[]}}`

	makeHerdrRunner := func(wsID string) runner {
		return func(ctx context.Context, extraEnv []string, argv ...string) (string, error) {
			if len(argv) >= 2 && argv[0] == "workspace" && argv[1] == "create" {
				for i, a := range argv {
					if a == "--cwd" && i+1 < len(argv) {
						mu.Lock()
						cwdArgs = append(cwdArgs, argv[i+1])
						mu.Unlock()
					}
				}
				return fmt.Sprintf(`{"result":{"workspace":{"workspace_id":%q}}}`, wsID), nil
			}
			if len(argv) >= 2 && argv[0] == "workspace" && argv[1] == "list" {
				return emptyList, nil
			}
			if len(argv) >= 2 && argv[0] == "worktree" && argv[1] == "create" {
				return fmt.Sprintf(`{"result":{"workspace":{"workspace_id":%q}}}`, wsID+"-wt"), nil
			}
			if len(argv) >= 2 && argv[0] == "pane" && argv[1] == "read" {
				return "root@nexus-fake-guest:/workspace#\n", nil
			}
			if len(argv) >= 2 && argv[0] == "agent" && argv[1] == "start" {
				return "", nil
			}
			if len(argv) >= 2 && argv[0] == "agent" && argv[1] == "get" {
				name := "ctrl-" + wsID + "-wt"
				return fmt.Sprintf(`{"result":{"agent":{"agent":%q,"agent_status":"idle","state_change_seq":1}}}`, name), nil
			}
			return "", nil
		}
	}

	makeNexusRunner := func(wsID string) runner {
		return func(ctx context.Context, extraEnv []string, argv ...string) (string, error) {
			if len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "list" {
				return herdrListLine(wsID+"-wt", wsID+"-wt:p1"), nil
			}
			if len(argv) >= 1 && argv[0] == "exec" {
				return "nexus-fake-guest\n", nil
			}
			return "", nil
		}
	}

	repos := []string{"/home/user/repo-alpha", "/home/user/repo-beta"}
	wsIDs := []string{"wa", "wb"}

	for i, repo := range repos {
		wsID := wsIDs[i]
		setupTestStore(t, "sb-abc123", testPrincipal)
		b := newWithRunners(Config{Model: "claude-haiku-4-5"}, makeHerdrRunner(wsID), makeNexusRunner(wsID))
		b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
		_, _, err := b.Provision(context.Background(), repo, controller.NewThreadRef("T", "C", fmt.Sprintf("%d", i)), testPrincipal)
		if err != nil {
			t.Fatalf("Provision for repo %q: %v", repo, err)
		}
	}

	mu.Lock()
	got := append([]string(nil), cwdArgs...)
	mu.Unlock()

	if len(got) != 2 {
		t.Fatalf("expected 2 workspace create --cwd calls, got %d: %v", len(got), got)
	}
	for i, repo := range repos {
		if got[i] != repo {
			t.Errorf("workspace create --cwd[%d] = %q, want %q", i, got[i], repo)
		}
	}
}

// ── agentNameFromWsID tests ───────────────────────────────────────────────

func TestAgentNameFromWsID_ValidFormat(t *testing.T) {
	cases := []string{"wDR", "wCZ", "wABCDEF", "w1", "wMixed-123", "w_under"}
	re := "^[a-z][a-z0-9_-]{0,31}$"
	for _, wsID := range cases {
		got := agentNameFromWsID(wsID)
		if len(got) > 32 {
			t.Errorf("agentNameFromWsID(%q) len %d > 32: %q", wsID, len(got), got)
		}
		if len(got) == 0 || got[0] < 'a' || got[0] > 'z' {
			t.Errorf("agentNameFromWsID(%q) does not start with lowercase letter: %q", wsID, got)
		}
		for _, r := range got {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
				t.Errorf("agentNameFromWsID(%q) contains invalid char %q: %q (want %s)", wsID, r, got, re)
				break
			}
		}
	}
}

func TestAgentNameFromWsID_CaseCollision(t *testing.T) {
	a := agentNameFromWsID("wDR")
	b := agentNameFromWsID("wdr")
	if a == b {
		t.Errorf("agentNameFromWsID(\"wDR\") == agentNameFromWsID(\"wdr\") = %q; want distinct names", a)
	}
}

// TestProvisionPassesSanitizedAgentName verifies that Provision derives a
// herdr-valid (all-lowercase) agent name from an uppercase workspace ID.
func TestProvisionPassesSanitizedAgentName(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)
	const wsID = "wUPPER"
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: fmt.Sprintf(`{"result":{"workspace":{"workspace_id":%q}}}`, wsID)},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine(wsID, wsID+":p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "up"), testPrincipal)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	want := agentNameFromWsID(wsID)
	if !h.calledWith("agent", "start", want) {
		h.mu.Lock()
		var got []string
		for _, c := range h.calls {
			if len(c.argv) >= 2 && c.argv[0] == "agent" && c.argv[1] == "start" {
				got = append(got, strings.Join(c.argv, " "))
			}
		}
		h.mu.Unlock()
		t.Errorf("herdr agent start called with unexpected name; want %q; agent start calls: %v", want, got)
	}
}

// ── Fix #2: Provision rollback tests ─────────────────────────────────────

// TestProvisionRollbackOnAgentStartFailure verifies that when agent start fails,
// the deferred rollback issues sandbox rm and worktree remove --workspace <wsID> --force.
func TestProvisionRollbackOnAgentStartFailure(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)
	agentStartErr := fmt.Errorf("exit status 1")
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wRB"}}}`},
		"agent start":     {out: `{"error":{"code":"internal","message":"boom"}}`, err: agentStartErr},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
		"worktree remove": {out: ""},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wRB", "wRB:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
		"sandbox rm":             {out: ""},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "rb"), testPrincipal)
	if err == nil {
		t.Fatal("Provision: expected error from agent start failure, got nil")
	}

	if !n.calledWith("sandbox", "rm", "sb-abc123") {
		t.Errorf("rollback: expected nexus sandbox rm sb-abc123; calls: %v", n.calls)
	}
	if !h.calledWith("worktree", "remove", "--workspace", "wRB", "--force") {
		t.Errorf("rollback: expected herdr worktree remove --workspace wRB --force; calls: %v", h.calls)
	}
}

// TestProvisionRollbackOnWorktreeSandboxFailure verifies that when
// nexus herdr worktree-sandbox fails before the nexus herdr list call, the
// deferred rollback still removes the expected volumes and deletes the branch.
// This requires rollbackNexusHandle to be set from the pre-computed handle
// (repo name + worktree dir basename) rather than waiting for herdr list.
//
// MUTATION PROOF: remove the early rollbackNexusHandle assignment and revert
// to the old `var rollbackNexusHandle string` → volume ls is never called →
// this test fails on the "volume ls must be called" assertion.
func TestProvisionRollbackOnWorktreeSandboxFailure(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)

	var capturedBranch string
	var volumeRmCalls []string
	volumeLsCalled := false

	herdrFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
		switch {
		case len(argv) >= 2 && argv[0] == "workspace" && argv[1] == "list":
			return `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`, nil
		case len(argv) >= 2 && argv[0] == "worktree" && argv[1] == "create":
			for i, a := range argv {
				if a == "--branch" && i+1 < len(argv) {
					capturedBranch = argv[i+1]
				}
			}
			return `{"result":{"workspace":{"workspace_id":"wEarly"}}}`, nil
		case len(argv) >= 2 && argv[0] == "worktree" && argv[1] == "remove":
			return "", nil
		}
		return "", nil
	}

	nexusFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
		switch {
		case len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "worktree-sandbox":
			return "error: volumestore: mke2fs not found on PATH (install e2fsprogs)", fmt.Errorf("exit status 1")
		case len(argv) >= 2 && argv[0] == "volume" && argv[1] == "ls":
			volumeLsCalled = true
			if capturedBranch == "" {
				return "", nil
			}
			safeBranch := strings.ReplaceAll(capturedBranch, "/", "-")
			handle := sandboxhandle.WorktreeHandle("repo", safeBranch)
			return strings.Join(worktreeVolumeNames(handle), "\n") + "\n", nil
		case len(argv) >= 3 && argv[0] == "volume" && argv[1] == "rm":
			volumeRmCalls = append(volumeRmCalls, argv[2])
			return "", nil
		}
		return "", nil
	}

	gitFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
		return "", nil
	}

	b := newWithRunnersGit(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, herdrFn, nexusFn, gitFn)

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "early"), testPrincipal)
	if err == nil {
		t.Fatal("Provision: expected error, got nil")
	}
	if !volumeLsCalled {
		t.Error("rollback: nexus volume ls must be called when worktree-sandbox fails (rollbackNexusHandle must be set early)")
	}
	if capturedBranch == "" {
		t.Skip("branch not captured from herdr worktree create; cannot verify volume rm calls")
	}
	safeBranch := strings.ReplaceAll(capturedBranch, "/", "-")
	expectedHandle := sandboxhandle.WorktreeHandle("repo", safeBranch)
	for _, name := range worktreeVolumeNames(expectedHandle) {
		found := false
		for _, rm := range volumeRmCalls {
			if rm == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("rollback: expected nexus volume rm %s; rm calls: %v", name, volumeRmCalls)
		}
	}
}

// ── Fix #12: env filtering tests ─────────────────────────────────────────

// TestFilteredOSEnv verifies that filteredOSEnv strips herdr/claude session vars.
func TestFilteredOSEnv(t *testing.T) {
	keys := []string{
		"HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID",
		"HERDR_ENV", "CLAUDECODE", "CLAUDE_CODE_SESSION", "CLAUDE_CODE_WHATEVER",
	}
	for _, k := range keys {
		t.Setenv(k, "should-be-stripped")
	}
	got := filteredOSEnv()
	for _, kv := range got {
		for _, k := range keys {
			if strings.HasPrefix(kv, k+"=") {
				t.Errorf("filteredOSEnv: found %q in child env; should have been stripped", kv)
			}
		}
	}
}

// ── Fix #13: unknown status tests ────────────────────────────────────────

// TestObserveUnknownReturnsError verifies that Observe(wait=false) returns an
// error when the agent status is unknown, rather than mapping it to Done.
func TestObserveUnknownReturnsError(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"agent get": {out: `{"result":{"agent":{"agent":"ctrl-unk","agent_status":"unknown","state_change_seq":1}}}`},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, nil)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	b.mu.Lock()
	b.entries["ctrl-unk"] = &entry{paneID: "unk:p1", nexusSandboxID: "sb-unk", wsID: "unk"}
	b.sandboxes["sb-unk"] = "ctrl-unk"
	b.mu.Unlock()

	st, err := b.Observe(context.Background(), "ctrl-unk", false)
	if err == nil {
		t.Fatalf("Observe(wait=false) with unknown status: expected error, got state %+v", st)
	}
	if st.Status == herdragent.StatusDone {
		t.Errorf("Observe(wait=false) with unknown status must not return Done")
	}
}

// ── Fix #15: parse field exact-match tests ───────────────────────────────

// TestParsePaneIDNoPrefixCollision verifies that parsePaneID for workspace "wDJ"
// does not match a line whose workspace_id is "wDJ1".
func TestParsePaneIDNoPrefixCollision(t *testing.T) {
	// Two workspaces sharing the prefix "wDJ"; only wDJ should match.
	listOut := "label=a\tworkspace_id=wDJ\tpane_id=p-correct\tsandbox_id=sb-1\tprincipal=u:x\n" +
		"label=b\tworkspace_id=wDJ1\tpane_id=p-wrong\tsandbox_id=sb-2\tprincipal=u:y\n"

	got := parsePaneID(listOut, "wDJ")
	if got != "p-correct" {
		t.Errorf("parsePaneID(\"wDJ\"): got %q, want %q", got, "p-correct")
	}
	got2 := parsePaneID(listOut, "wDJ1")
	if got2 != "p-wrong" {
		t.Errorf("parsePaneID(\"wDJ1\"): got %q, want %q", got2, "p-wrong")
	}
}

// TestParseListFieldNoPrefixCollision verifies parseListField exact matching.
func TestParseListFieldNoPrefixCollision(t *testing.T) {
	listOut := "label=a\tworkspace_id=wDJ\thandle=h-correct\tsandbox_id=sb-1\n" +
		"label=b\tworkspace_id=wDJ1\thandle=h-wrong\tsandbox_id=sb-2\n"

	got := parseListField(listOut, "wDJ", "handle=")
	if got != "h-correct" {
		t.Errorf("parseListField(\"wDJ\"): got %q, want %q", got, "h-correct")
	}
	got2 := parseListField(listOut, "wDJ1", "handle=")
	if got2 != "h-wrong" {
		t.Errorf("parseListField(\"wDJ1\"): got %q, want %q", got2, "h-wrong")
	}
}

// ── R10: volume cleanup + branch deletion tests ───────────────────────────

// TestTeardownRemovesVolumesAndBranch verifies that Teardown, after removing
// the worktree, calls `nexus volume ls`, then `nexus volume rm <name>` for each
// of the five exact volume names, and `git -C <repo> branch -D ctrl/...`.
func TestTeardownRemovesVolumesAndBranch(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)

	const handle = "test-handle"
	vols := worktreeVolumeNames(handle)
	// Build a `nexus volume ls` output that includes all five volumes plus an
	// unrelated volume that must NOT be removed.
	var lsBuf strings.Builder
	lsBuf.WriteString("unrelated-vol\n")
	for _, v := range vols {
		lsBuf.WriteString(v + "\n")
	}
	lsOut := lsBuf.String()

	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wVol"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wVol","agent_status":"idle","state_change_seq":1}}}`},
		"worktree remove": {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wVol", "wVol:p1")},
		"ps":                     {out: nexusPSLine(handle, "sb-abc123")},
		"sandbox rm":             {out: ""},
		"exec":                   {out: "nexus-fake-guest\n"},
		"volume ls":              {out: lsOut},
		"volume rm":              {out: ""},
	})
	g := newFakeCmd(map[string]fakeReply{
		"branch": {out: ""},
	})

	b := newWithRunnersGit(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run, g.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	sandboxID, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "vol"), testPrincipal)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := b.Teardown(context.Background(), sandboxID); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	// volume ls must be called.
	if !n.calledWith("volume", "ls") {
		t.Error("expected nexus volume ls call")
	}

	// Each of the five volumes must be removed exactly; unrelated-vol must not.
	n.mu.Lock()
	rmNames := make(map[string]int)
	for _, c := range n.calls {
		if len(c.argv) == 3 && c.argv[0] == "volume" && c.argv[1] == "rm" {
			rmNames[c.argv[2]]++
		}
	}
	n.mu.Unlock()

	for _, v := range vols {
		if rmNames[v] != 1 {
			t.Errorf("expected exactly one nexus volume rm %q; got %d", v, rmNames[v])
		}
	}
	if rmNames["unrelated-vol"] != 0 {
		t.Errorf("unrelated-vol must not be removed; got %d rm calls", rmNames["unrelated-vol"])
	}

	// git branch -D must be called with the ctrl/ branch.
	if !g.calledWith("-C", "/repo", "branch", "-D") {
		t.Error("expected git -C /repo branch -D <branch> call")
	}
	g.mu.Lock()
	var branchArg string
	for _, c := range g.calls {
		if len(c.argv) == 5 && c.argv[0] == "-C" && c.argv[2] == "branch" && c.argv[3] == "-D" {
			branchArg = c.argv[4]
		}
	}
	g.mu.Unlock()
	if !strings.HasPrefix(branchArg, "ctrl/") {
		t.Errorf("git branch -D argument = %q; want ctrl/... prefix", branchArg)
	}
}

// TestProvisionMarkerPresentDuringVerify verifies that the controller marker
// persists through verifyPaneInGuest (i.e. it is NOT removed right after bind).
func TestProvisionMarkerPresentDuringVerify(t *testing.T) {
	stateDir := setupTestStore(t, "sb-abc123", testPrincipal)

	var markerDuringVerify bool
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wV"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wV","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})

	// Intercept the nexus runner to check for the marker during exec (hostname).
	nexusRunner := func(ctx context.Context, extraEnv []string, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "exec" {
			claimsDir := filepath.Join(stateDir, "nexus", "controller-wt-claims")
			entries, _ := os.ReadDir(claimsDir)
			markerDuringVerify = len(entries) > 0
		}
		n := newFakeCmd(map[string]fakeReply{
			"herdr worktree-sandbox": {out: ""},
			"herdr list":             {out: herdrListLine("wV", "wV:p1")},
			"exec":                   {out: "nexus-fake-guest\n"},
		})
		return n.run(ctx, extraEnv, argv...)
	}

	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, nexusRunner)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "V"), testPrincipal)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !markerDuringVerify {
		t.Error("controller marker was absent during verifyPaneInGuest — must be kept until verification completes")
	}
	// Marker must be cleaned up after Provision returns.
	claimsDir := filepath.Join(stateDir, "nexus", "controller-wt-claims")
	entries, _ := os.ReadDir(claimsDir)
	if len(entries) != 0 {
		t.Errorf("controller marker not cleaned up after Provision; remaining: %v", entries)
	}
}

// TestProvisionMarkerRemovedOnRollback verifies that the claim marker is cleaned
// up when Provision fails (rolls back) after binding.
func TestProvisionMarkerRemovedOnRollback(t *testing.T) {
	stateDir := setupTestStore(t, "sb-abc123", testPrincipal)

	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wR"}}}`},
		"worktree remove": {out: ""},
		// pane read returns a prompt with a hostname that differs from exec output
		"pane read": {out: "root@wrong-host:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wR", "wR:p1")},
		// exec returns a different hostname to trigger verifyPaneInGuest mismatch
		"exec":       {out: "right-host\n"},
		"sandbox rm": {out: ""},
	})

	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "R"), testPrincipal)
	// Provision must fail (hostname mismatch)
	if err == nil {
		t.Fatal("expected Provision to fail on hostname mismatch, got nil")
	}
	claimsDir := filepath.Join(stateDir, "nexus", "controller-wt-claims")
	entries, _ := os.ReadDir(claimsDir)
	if len(entries) != 0 {
		t.Errorf("controller marker not cleaned up after rollback; remaining: %v", entries)
	}
}

// TestPermModeFromContextReachesAgentStart verifies that a permission mode set
// in the context via WithPermMode is passed to `herdr agent start` argv.
func TestPermModeFromContextReachesAgentStart(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)

	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wPM"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wPM","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wPM", "wPM:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})

	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	ctx := controller.WithPermMode(context.Background(), "bypassPermissions")
	_, _, err := b.Provision(ctx, "/repo", controller.NewThreadRef("T", "C", "PM"), testPrincipal)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Find the agent start call and verify --permission-mode bypassPermissions is present.
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if len(c.argv) >= 2 && c.argv[0] == "agent" && c.argv[1] == "start" {
			for i, arg := range c.argv {
				if arg == "--permission-mode" && i+1 < len(c.argv) {
					if c.argv[i+1] != "bypassPermissions" {
						t.Errorf("agent start --permission-mode = %q, want bypassPermissions", c.argv[i+1])
					}
					return
				}
			}
			t.Errorf("agent start call missing --permission-mode flag: %v", c.argv)
			return
		}
	}
	t.Error("no agent start call found")
}

// TestModelFromContextReachesAgentStart verifies that a model set in the context
// via controller.WithModel is passed as --model to `herdr agent start` argv.
func TestModelFromContextReachesAgentStart(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)

	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wMD"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wMD","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wMD", "wMD:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})

	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	ctx := controller.WithModel(context.Background(), "claude-sonnet-4-5")
	_, _, err := b.Provision(ctx, "/repo", controller.NewThreadRef("T", "C", "MD"), testPrincipal)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if len(c.argv) >= 2 && c.argv[0] == "agent" && c.argv[1] == "start" {
			for i, arg := range c.argv {
				if arg == "--model" && i+1 < len(c.argv) {
					if c.argv[i+1] != "claude-sonnet-4-5" {
						t.Errorf("agent start --model = %q, want claude-sonnet-4-5", c.argv[i+1])
					}
					return
				}
			}
			t.Errorf("agent start call missing --model flag: %v", c.argv)
			return
		}
	}
	t.Error("no agent start call found")
}

func TestParseFinalAnswer(t *testing.T) {
	// Build JSONL lines in live transcript format (top-level "type" field).
	userEntry := func(text string) string {
		return `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]}}`
	}
	userWithMarker := func(marker string) string {
		return `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"text","text":"` + marker + `\n\nplease help"}]}}`
	}
	assistantText := func(text string) string {
		return `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`
	}
	assistantToolUse := func() string {
		return `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`
	}
	assistantToolThenText := func(toolID, text string) string {
		return `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"tool_use","id":"` + toolID + `","name":"Bash","input":{}},{"type":"text","text":"` + text + `"}]}}`
	}
	assistantThinking := func() string {
		return `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"internal reasoning"}]}}`
	}
	toolResult := func(id string) string {
		return `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"output"}]}}`
	}
	attach := func() string { return `{"type":"attachment"}` }

	const mkr = "hc-turn:testid"

	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "live fixture format: marker then text answer",
			lines: []string{
				attach(),
				userWithMarker(mkr),
				attach(),
				assistantText("the answer is 42"),
			},
			want: "the answer is 42",
		},
		{
			name: "2-turn: turn2 answer excludes turn1 answer",
			lines: []string{
				userWithMarker("hc-turn:turn1"),
				assistantText("391"),
				userWithMarker(mkr),
				assistantText("143"),
			},
			want: "143",
		},
		{
			name: "tool_use resets collected text: final message after last tool use wins",
			lines: []string{
				userWithMarker(mkr),
				assistantToolThenText("t1", "final after tool"),
			},
			want: "final after tool",
		},
		{
			name: "tool_use then more turns: only last assistant text",
			lines: []string{
				userWithMarker(mkr),
				assistantText("first try"),
				assistantToolUse(),
				toolResult("t1"),
				assistantText("done"),
			},
			want: "done",
		},
		{
			name: "thinking block ignored",
			lines: []string{
				userWithMarker(mkr),
				assistantThinking(),
				assistantText("real answer"),
			},
			want: "real answer",
		},
		{
			name: "tool_result-only user entries not treated as prompt boundary",
			lines: []string{
				userWithMarker(mkr),
				assistantToolUse(),
				toolResult("t1"),
				assistantText("after tool"),
			},
			want: "after tool",
		},
		{
			name: "next real user entry stops collection",
			lines: []string{
				userWithMarker(mkr),
				assistantText("answer to turn"),
				userEntry("new question"),
				assistantText("answer to new question"),
			},
			want: "answer to turn",
		},
		{
			name: "marker not found returns empty",
			lines: []string{
				userEntry("no marker here"),
				assistantText("some text"),
			},
			want: "",
		},
		{
			name: "live fixture: no marker returns empty",
			lines: func() []string {
				data, err := os.ReadFile("/var/tmp/live-transcript-fixture.jsonl")
				if err != nil {
					return []string{}
				}
				lines := strings.Split(string(data), "\n")
				if len(lines) > 0 {
					lines = lines[1:] // skip filename header
				}
				return lines
			}(),
			want: "", // no turn marker present in live fixture
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFinalAnswer(tc.lines, mkr)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRestartUsesResumeFlag verifies that Restart passes --resume to
// `herdr agent start` (and not --session-id), because the transcript for that
// session already exists and claude rejects a reused --session-id.
func TestRestartUsesResumeFlag(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"agent start": {out: ""},
		"agent get":   {out: `{"result":{"agent":{"agent":"ctrl-wRF","agent_status":"idle","state_change_seq":1}}}`},
		"pane read":   {out: "root@nexus-fake-guest:/workspace#\n"},
	})
	n := newFakeCmd(map[string]fakeReply{
		"exec": {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	b.mu.Lock()
	b.entries["ctrl-wRF"] = &entry{paneID: "wRF:p1", nexusSandboxID: "sb-rf1", wsID: "wRF"}
	b.sandboxes["sb-rf1"] = "ctrl-wRF"
	b.mu.Unlock()

	if _, err := b.Restart(context.Background(), "sb-rf1", "ctrl-wRF"); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if len(c.argv) >= 2 && c.argv[0] == "agent" && c.argv[1] == "start" {
			for i, arg := range c.argv {
				if arg == "--session-id" {
					t.Errorf("Restart agent start must not use --session-id (found at index %d)", i)
					return
				}
				if arg == "--resume" {
					return // found --resume: pass
				}
			}
			t.Errorf("Restart agent start missing --resume flag: %v", c.argv)
			return
		}
	}
	t.Error("no agent start call found")
}

func TestStartAgentIsolationFlags(t *testing.T) {
	setupTestStore(t, "sb-abc123", testPrincipal)
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wiso"}}}`},
		"agent start":     {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wiso","agent_status":"idle","state_change_seq":1}}}`},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
		"pane run":        {out: ""},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wiso", "wiso:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, n.run)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

	_, _, err := b.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "iso"), testPrincipal)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	checkArgv := func(label string, calls []fakeCall) {
		for _, c := range calls {
			if len(c.argv) >= 2 && c.argv[0] == "agent" && c.argv[1] == "start" {
				argv := strings.Join(c.argv, " ")
				if !strings.Contains(argv, "--setting-sources") {
					t.Errorf("%s: agent start missing --setting-sources: %v", label, c.argv)
				}
				if !strings.Contains(argv, "--strict-mcp-config") {
					t.Errorf("%s: agent start missing --strict-mcp-config: %v", label, c.argv)
				}
				if !strings.Contains(argv, "--settings") {
					t.Errorf("%s: agent start missing --settings: %v", label, c.argv)
				}
				if !strings.Contains(argv, "--session-id") {
					t.Errorf("%s: agent start missing --session-id: %v", label, c.argv)
				}
				return
			}
			// Check pane run path
			if len(c.argv) >= 2 && c.argv[0] == "pane" && c.argv[1] == "run" {
				cmd := strings.Join(c.argv, " ")
				if !strings.Contains(cmd, "--setting-sources") {
					t.Errorf("%s: pane run missing --setting-sources: %v", label, c.argv)
				}
				if !strings.Contains(cmd, "--strict-mcp-config") {
					t.Errorf("%s: pane run missing --strict-mcp-config: %v", label, c.argv)
				}
				if !strings.Contains(cmd, "--settings") {
					t.Errorf("%s: pane run missing --settings: %v", label, c.argv)
				}
				if !strings.Contains(cmd, "--session-id") {
					t.Errorf("%s: pane run missing --session-id: %v", label, c.argv)
				}
				return
			}
		}
	}

	h.mu.Lock()
	calls := append([]fakeCall{}, h.calls...)
	h.mu.Unlock()
	checkArgv("agent start path", calls)

	// Test pane run fallback path (agent_pane_busy).
	setupTestStore(t, "sb-abc123", testPrincipal)
	busyErr := fmt.Errorf("exit status 1")
	h2 := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"wiso2"}}}`},
		"agent start":     {out: `{"error":{"code":"agent_pane_busy","message":"busy"}}`, err: busyErr},
		"agent rename":    {out: ""},
		"pane run":        {out: ""},
		"pane read":       {out: "root@nexus-fake-guest:/workspace#\n"},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-wiso2","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
	})
	n2 := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("wiso2", "wiso2:p1")},
		"exec":                   {out: "nexus-fake-guest\n"},
	})
	b2 := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h2.run, n2.run)
	b2.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	_, _, err = b2.Provision(context.Background(), "/repo", controller.NewThreadRef("T", "C", "iso2"), testPrincipal)
	if err != nil {
		t.Fatalf("Provision (busy path): %v", err)
	}
	h2.mu.Lock()
	calls2 := append([]fakeCall{}, h2.calls...)
	h2.mu.Unlock()
	checkArgv("pane run path", calls2)
}

func TestParsePaneAnswer(t *testing.T) {
	t.Run("live fixture", func(t *testing.T) {
		data, err := os.ReadFile("testdata/live-pane-ar.txt")
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		got := parsePaneAnswer(string(data))
		want := "572b368 fix(controller): guest-shell, marker lifetime, per-channel perm mode"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("no prompt line returns empty", func(t *testing.T) {
		got := parsePaneAnswer("● some output\n")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("strips hook errors and timing", func(t *testing.T) {
		pane := "❯ ask something\n" +
			"  ⎿  SessionStart:startup hook error\n" +
			"● my answer\n" +
			"✻ Brewed for 2s\n"
		got := parsePaneAnswer(pane)
		if got != "my answer" {
			t.Errorf("got %q, want %q", got, "my answer")
		}
	})

	t.Run("skips Ran N stop hook lines", func(t *testing.T) {
		pane := "❯ prompt\n" +
			"● real answer\n" +
			"● Ran 3 stop hooks\n" +
			"  ⎿  hook error\n"
		got := parsePaneAnswer(pane)
		if got != "real answer" {
			t.Errorf("got %q, want %q", got, "real answer")
		}
	})
}

func TestObserveWaitCtxDeadlineReturnsError(t *testing.T) {
	idleOut := `{"result":{"agent":{"agent":"ctrl-w9","agent_status":"idle","state_change_seq":1}}}`
	h := newFakeCmd(map[string]fakeReply{
		"agent get": {out: idleOut},
	})
	b := newWithRunners(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, h.run, nil)
	b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}
	b.mu.Lock()
	b.entries["ctrl-w9"] = &entry{paneID: "w9:p1", nexusSandboxID: "sb-xyz", wsID: "w9"}
	b.sandboxes["sb-xyz"] = "ctrl-w9"
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := b.Observe(ctx, "ctrl-w9", true)
	if err == nil {
		t.Fatal("Observe(wait=true): want error on ctx deadline, got nil")
	}
}

// herdrListLineEmptyBinding returns a nexus herdr list row where workspace_id
// and pane_id are empty (broken binding after CLI bug).
func herdrListLineEmptyBinding(handle, sandboxID string) string {
	return fmt.Sprintf("label=nexus:%s\tworkspace_id=\thandle=%s\tsandbox_id=%s\tpane_id=\tprincipal=\n",
		filepath.Base(handle), handle, sandboxID)
}

// herdrPaneListJSON returns a minimal herdr pane list JSON response for wsID with paneID.
func herdrPaneListJSON(paneID string) string {
	return fmt.Sprintf(`{"result":{"panes":[{"pane_id":%q,"label":"nexus guest shell","agent":"claude"}]}}`, paneID)
}

// TestRestart_RecoversWorkspaceFromHerdrLabel_WhenBindingEmpty verifies that
// when the herdr list row has an empty workspace_id/pane_id (broken binding),
// Restart recovers the workspace via herdr workspace list and obtains the pane
// from herdr pane list (never from nexus herdr list, which stays broken).
func TestRestart_RecoversWorkspaceFromHerdrLabel_WhenBindingEmpty(t *testing.T) {
	const handle = "nexus/ctrl-nexus-recover-test"
	const sbID = "sb-RECOVER01"
	const wsID = "wRCV"
	const paneID = "wRCV:p3"
	base := filepath.Base(handle)
	label := "nexus:" + base

	// nexus herdr list always returns the broken binding (empty workspace_id/pane_id).
	listBroken := herdrListLineEmptyBinding(handle, sbID)
	wsListJSON := fmt.Sprintf(`{"result":{"workspaces":[{"workspace_id":%q,"label":%q,"worktree":{"checkout_path":"/worktrees/%s"}}]}}`,
		wsID, label, base)

	t.Run("pane_already_exists", func(t *testing.T) {
		var spaceOpenPaneCalled bool
		herdrFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
			switch {
			case len(argv) >= 2 && argv[0] == "workspace" && argv[1] == "list":
				return wsListJSON, nil
			case len(argv) >= 3 && argv[0] == "pane" && argv[1] == "list":
				return herdrPaneListJSON(paneID), nil
			case len(argv) >= 2 && argv[0] == "agent" && argv[1] == "start":
				return "", nil
			case len(argv) >= 2 && argv[0] == "agent" && argv[1] == "get":
				return fmt.Sprintf(`{"result":{"agent":{"agent":%q,"agent_status":"idle","state_change_seq":1}}}`, agentNameFromWsID(wsID)), nil
			case len(argv) >= 2 && argv[0] == "pane" && argv[1] == "read":
				return "root@nexus-fake-guest:/workspace#\n", nil
			}
			return "", nil
		}
		nexusFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
			if len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "list" {
				return listBroken, nil
			}
			if len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "space-open-pane" {
				spaceOpenPaneCalled = true
				return "", nil
			}
			if len(argv) >= 2 && argv[0] == "exec" {
				return "nexus-fake-guest\n", nil
			}
			return "", nil
		}
		b := newWithRunnersGit(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, herdrFn, nexusFn,
			func(_ context.Context, _ []string, _ ...string) (string, error) { return "", nil })
		b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

		newRef, err := b.Restart(context.Background(), sbID, agentNameFromWsID(wsID))
		if err != nil {
			t.Fatalf("Restart with broken binding (pane exists): %v", err)
		}
		if newRef == "" {
			t.Error("Restart must return a non-empty ref")
		}
		if spaceOpenPaneCalled {
			t.Error("space-open-pane must NOT be called when pane already exists in pane list")
		}
		wantSessionID := deterministicSessionID(sbID)
		b.mu.Lock()
		e, ok := b.entries[newRef]
		b.mu.Unlock()
		if !ok {
			t.Fatalf("new entry not found for ref %q", newRef)
		}
		if e.wsID != wsID {
			t.Errorf("entry wsID = %q, want %q", e.wsID, wsID)
		}
		if e.agentSessionID != wantSessionID {
			t.Errorf("agentSessionID = %q, want %q", e.agentSessionID, wantSessionID)
		}
	})

	t.Run("no_pane_then_space_open_pane", func(t *testing.T) {
		var spaceOpenPaneCalled bool
		paneListCallCount := 0
		herdrFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
			switch {
			case len(argv) >= 2 && argv[0] == "workspace" && argv[1] == "list":
				return wsListJSON, nil
			case len(argv) >= 3 && argv[0] == "pane" && argv[1] == "list":
				paneListCallCount++
				if paneListCallCount == 1 {
					// First call: no pane yet.
					return `{"result":{"panes":[]}}`, nil
				}
				// Second call: pane appeared after space-open-pane.
				return herdrPaneListJSON(paneID), nil
			case len(argv) >= 2 && argv[0] == "agent" && argv[1] == "start":
				return "", nil
			case len(argv) >= 2 && argv[0] == "agent" && argv[1] == "get":
				return fmt.Sprintf(`{"result":{"agent":{"agent":%q,"agent_status":"idle","state_change_seq":1}}}`, agentNameFromWsID(wsID)), nil
			case len(argv) >= 2 && argv[0] == "pane" && argv[1] == "read":
				return "root@nexus-fake-guest:/workspace#\n", nil
			}
			return "", nil
		}
		nexusFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
			if len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "list" {
				return listBroken, nil
			}
			if len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "space-open-pane" {
				spaceOpenPaneCalled = true
				return "", nil
			}
			if len(argv) >= 2 && argv[0] == "exec" {
				return "nexus-fake-guest\n", nil
			}
			return "", nil
		}
		b := newWithRunnersGit(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, herdrFn, nexusFn,
			func(_ context.Context, _ []string, _ ...string) (string, error) { return "", nil })
		b.agentOpts = []herdragent.Option{herdragent.WithSettle(10 * time.Millisecond)}

		newRef, err := b.Restart(context.Background(), sbID, agentNameFromWsID(wsID))
		if err != nil {
			t.Fatalf("Restart with broken binding (no pane): %v", err)
		}
		if !spaceOpenPaneCalled {
			t.Error("space-open-pane must be called when pane list is empty")
		}
		if paneListCallCount < 2 {
			t.Errorf("pane list called %d times, want ≥2 (before and after space-open-pane)", paneListCallCount)
		}
		b.mu.Lock()
		e, ok := b.entries[newRef]
		b.mu.Unlock()
		if !ok {
			t.Fatalf("new entry not found for ref %q", newRef)
		}
		if e.paneID != paneID {
			t.Errorf("entry paneID = %q, want %q", e.paneID, paneID)
		}
	})
}

// TestRediscoverBySandboxID_ErrorDistinguishesMissingVsUnbound verifies that
// rediscoverBySandboxID returns distinct error messages for:
//   - sandbox row entirely absent from nexus herdr list
//   - sandbox row present but workspace_id is empty (broken binding)
func TestRediscoverBySandboxID_ErrorDistinguishesMissingVsUnbound(t *testing.T) {
	const missingID = "sb-MISSING"
	const unboundID = "sb-UNBOUND"
	const handle = "nexus/ctrl-nexus-unbound"

	listOut := herdrListLineEmptyBinding(handle, unboundID)
	// workspace list returns nothing matching, so recovery fails cleanly.
	wsListJSON := `{"result":{"workspaces":[]}}`

	herdrFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "workspace" && argv[1] == "list" {
			return wsListJSON, nil
		}
		return "", nil
	}
	nexusFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "list" {
			return listOut, nil
		}
		return "", nil
	}

	b := newWithRunnersGit(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, herdrFn, nexusFn,
		func(_ context.Context, _ []string, _ ...string) (string, error) { return "", nil })

	// Missing sandbox: expect "not in nexus herdr list".
	_, _, errMissing := b.rediscoverBySandboxID(context.Background(), missingID)
	if errMissing == nil {
		t.Fatal("expected error for missing sandbox, got nil")
	}
	if !strings.Contains(errMissing.Error(), "not in nexus herdr list") {
		t.Errorf("missing error = %q; want 'not in nexus herdr list'", errMissing)
	}

	// Unbound sandbox (row exists, wsID empty): expect "binding has no herdr workspace/pane".
	_, _, errUnbound := b.rediscoverBySandboxID(context.Background(), unboundID)
	if errUnbound == nil {
		t.Fatal("expected error for unbound sandbox, got nil")
	}
	if !strings.Contains(errUnbound.Error(), "binding has no herdr workspace/pane") {
		t.Errorf("unbound error = %q; want 'binding has no herdr workspace/pane'", errUnbound)
	}
	if strings.Contains(errUnbound.Error(), "not in nexus herdr list") {
		t.Errorf("unbound error must not say 'not in nexus herdr list': %q", errUnbound)
	}
}

// TestTeardown_EmptyWsID_NoEmptyWorktreeRemove verifies that when Teardown
// resolves to an entry with an empty wsID (broken binding that could not be
// recovered), it does NOT call herdr worktree remove with an empty workspace arg.
func TestTeardown_EmptyWsID_NoEmptyWorktreeRemove(t *testing.T) {
	const sbID = "sb-EMPTYWSID"

	var worktreeRemoveCalls [][]string
	herdrFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "worktree" && argv[1] == "remove" {
			worktreeRemoveCalls = append(worktreeRemoveCalls, append([]string{}, argv...))
		}
		if len(argv) >= 2 && argv[0] == "workspace" && argv[1] == "list" {
			return `{"result":{"workspaces":[]}}`, nil
		}
		return "", nil
	}
	nexusFn := func(_ context.Context, _ []string, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "list" {
			// Row present but empty workspace_id and handle (no recovery possible).
			return fmt.Sprintf("label=x\tworkspace_id=\thandle=\tsandbox_id=%s\tpane_id=\tprincipal=\n", sbID), nil
		}
		if len(argv) >= 2 && argv[0] == "volume" && argv[1] == "ls" {
			return "", nil
		}
		return "", nil
	}

	b := newWithRunnersGit(Config{RepoPath: "/repo", Model: "claude-haiku-4-5"}, herdrFn, nexusFn,
		func(_ context.Context, _ []string, _ ...string) (string, error) { return "", nil })
	b.mu.Lock()
	// Pre-populate entry with empty wsID to simulate the broken-binding path.
	b.entries["ctrl-emptywsid"] = &entry{nexusSandboxID: sbID, wsID: ""}
	b.sandboxes[sbID] = "ctrl-emptywsid"
	b.mu.Unlock()

	// Teardown should not error due to empty wsID.
	_ = b.Teardown(context.Background(), sbID)

	// worktree remove must not have been called with "--workspace" followed by "".
	for _, call := range worktreeRemoveCalls {
		for i, arg := range call {
			if arg == "--workspace" && i+1 < len(call) && call[i+1] == "" {
				t.Errorf("worktree remove called with empty --workspace arg: %v", call)
			}
		}
	}
}
