package herdr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
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
func herdrListLine(wsID, paneID string) string {
	return fmt.Sprintf("label=test\tworkspace_id=%s\thandle=test-h\tsandbox_id=sb-test\tpane_id=%s\n", wsID, paneID)
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
			})
			n := newFakeCmd(map[string]fakeReply{
				"herdr worktree-sandbox": {out: ""},
				"herdr list":             {out: herdrListLine("w2", "w2:p1")},
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
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w3", "w3:p1")},
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
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w4","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w4", "w4:p1")},
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

func TestAnswerDigitUsesSendKeys(t *testing.T) {
	h := newFakeCmd(map[string]fakeReply{
		"workspace list":  {out: `{"result":{"workspaces":[{"workspace_id":"w1","worktree":{"checkout_path":"/repo"}}]}}`},
		"worktree create": {out: `{"result":{"workspace":{"workspace_id":"w5"}}}`},
		"agent start":     {out: ""},
		"agent send-keys": {out: ""},
		"agent prompt":    {out: ""},
		"agent get":       {out: `{"result":{"agent":{"agent":"ctrl-w5","agent_status":"idle","state_change_seq":1}}}`},
		"agent wait":      {out: ""},
	})
	n := newFakeCmd(map[string]fakeReply{
		"herdr worktree-sandbox": {out: ""},
		"herdr list":             {out: herdrListLine("w5", "w5:p1")},
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
