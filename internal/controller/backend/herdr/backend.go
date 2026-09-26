// Package herdr implements controller.AgentBackend backed by herdr + nexus CLIs.
package herdr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/herdragent"
	"github.com/IniZio/nexus/internal/herdrout"
	"github.com/creack/pty"
)

// runner executes a command and returns combined stdout+stderr.
type runner func(ctx context.Context, extraEnv []string, argv ...string) (string, error)

// Config holds deployment parameters for the backend.
type Config struct {
	RepoPath        string
	Model           string
	HerdrSocketPath string
	NexusBin        string
	ExtraEnv        []string
}

type entry struct {
	paneID   string
	tornDown bool
}

// Backend implements controller.AgentBackend.
type Backend struct {
	cfg       Config
	herdrRun  runner
	nexusRun  runner
	agentOpts []herdragent.Option

	mu        sync.Mutex
	entries   map[string]*entry // agentRef → pane/torn state
	sandboxes map[string]string // sandboxID(=wsID) → agentRef
}

// New returns a Backend using real herdr and nexus binaries.
func New(cfg Config) *Backend {
	b := &Backend{
		cfg:       cfg,
		entries:   make(map[string]*entry),
		sandboxes: make(map[string]string),
	}
	b.herdrRun = b.defaultHerdrRun
	b.nexusRun = b.defaultNexusRun
	return b
}

// newWithRunners creates a Backend with injected runners for testing.
func newWithRunners(cfg Config, hr, nr runner, opts ...herdragent.Option) *Backend {
	b := &Backend{
		cfg:       cfg,
		herdrRun:  hr,
		nexusRun:  nr,
		agentOpts: opts,
		entries:   make(map[string]*entry),
		sandboxes: make(map[string]string),
	}
	return b
}

func (b *Backend) defaultHerdrRun(ctx context.Context, extraEnv []string, argv ...string) (string, error) {
	herdrBin := os.Getenv("HERDR_BIN_PATH")
	if herdrBin == "" {
		var err error
		herdrBin, err = exec.LookPath("herdr")
		if err != nil {
			return "", fmt.Errorf("herdr not found (HERDR_BIN_PATH unset, not in PATH)")
		}
	}
	base := b.baseEnv()
	return runCmdPTY(ctx, herdrBin, append(base, extraEnv...), argv...)
}

func (b *Backend) defaultNexusRun(ctx context.Context, extraEnv []string, argv ...string) (string, error) {
	nexusBin := b.cfg.NexusBin
	if nexusBin == "" {
		nexusBin = os.Getenv("NEXUS_BIN")
	}
	if nexusBin == "" {
		if p, err := exec.LookPath("nexus"); err == nil {
			nexusBin = p
		}
	}
	if nexusBin == "" {
		var err error
		nexusBin, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve nexus binary: %w", err)
		}
	}
	base := b.nexusBaseEnv()
	return runCmd(ctx, nexusBin, append(base, extraEnv...), argv...)
}

func (b *Backend) baseEnv() []string {
	env := append([]string(nil), b.cfg.ExtraEnv...)
	if b.cfg.HerdrSocketPath != "" {
		env = append(env, "HERDR_SOCKET_PATH="+b.cfg.HerdrSocketPath)
	}
	return env
}

func (b *Backend) nexusBaseEnv() []string {
	env := b.baseEnv()
	home, _ := os.UserHomeDir()
	localBin := home + "/.local/bin"
	path := os.Getenv("PATH")
	if !strings.Contains(path, localBin) {
		env = append(env, "PATH="+localBin+":"+path)
	}
	if os.Getenv("TMPDIR") == "" {
		env = append(env, "TMPDIR=/var/tmp")
	}
	return env
}

func runCmd(ctx context.Context, bin string, extraEnv []string, argv ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	err := cmd.Run()
	return buf.String(), err
}

func runCmdPTY(ctx context.Context, bin string, extraEnv []string, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, argv...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", fmt.Errorf("pty start %s: %w", bin, err)
	}
	defer ptmx.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, ptmx)
	waitErr := cmd.Wait()
	out := buf.String()
	if waitErr != nil {
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() != 0 {
			return out, waitErr
		}
	}
	return out, nil
}

// Provision creates a herdr worktree for project, binds a sandbox, starts the agent.
// Returns (sandboxID=worktreeWorkspaceID, agentRef=agentName).
func (b *Backend) Provision(ctx context.Context, project string, ref controller.ThreadRef, principal string) (string, string, error) {
	parentWS, err := b.findOrCreateWorkspace(ctx)
	if err != nil {
		return "", "", err
	}

	branch := branchName(project, ref, time.Now().UnixMicro())
	wtOut, err := b.herdrRun(ctx, nil, "worktree", "create", "--workspace", parentWS, "--branch", branch, "--no-focus")
	if err != nil {
		return "", "", fmt.Errorf("herdr worktree create: %w\n%s", err, wtOut)
	}
	wsID := herdrout.WorktreeCreateWorkspaceID(wtOut)
	if wsID == "" {
		return "", "", fmt.Errorf("herdr worktree create: no workspace_id in output: %s", wtOut)
	}

	bindOut, bindErr := b.nexusRun(ctx, []string{vault.PrincipalEnv + "=" + principal}, "herdr", "worktree-sandbox", wsID)
	if bindErr != nil && !strings.Contains(bindOut, "only the pane failed") {
		return "", "", fmt.Errorf("nexus herdr worktree-sandbox: %w\n%s", bindErr, bindOut)
	}
	if bindErr != nil {
		if reopenOut, reopenErr := b.nexusRun(ctx, nil, "herdr", "space-open-pane", wsID); reopenErr != nil {
			return "", "", fmt.Errorf("nexus herdr space-open-pane after pane-only failure: %w\n%s", reopenErr, reopenOut)
		}
	}

	listOut, err := b.nexusRun(ctx, nil, "herdr", "list")
	if err != nil {
		return "", "", fmt.Errorf("nexus herdr list: %w\n%s", err, listOut)
	}
	paneID := parsePaneID(listOut, wsID)
	if paneID == "" {
		return "", "", fmt.Errorf("no pane_id for workspace %s; list output: %s", wsID, listOut)
	}

	agentName := "ctrl-" + wsID
	agentRef, err := b.startAgent(ctx, agentName, paneID)
	if err != nil {
		return "", "", fmt.Errorf("start agent: %w", err)
	}

	b.mu.Lock()
	b.entries[agentRef] = &entry{paneID: paneID}
	b.sandboxes[wsID] = agentRef
	b.mu.Unlock()

	return wsID, agentRef, nil
}

func (b *Backend) findOrCreateWorkspace(ctx context.Context) (string, error) {
	listOut, err := b.herdrRun(ctx, nil, "workspace", "list")
	if err != nil {
		return "", fmt.Errorf("herdr workspace list: %w\n%s", err, listOut)
	}
	if ws := parseWorkspaceForPath(listOut, b.cfg.RepoPath); ws != "" {
		return ws, nil
	}
	createOut, err := b.herdrRun(ctx, nil, "workspace", "create", "--cwd", b.cfg.RepoPath)
	if err != nil {
		return "", fmt.Errorf("herdr workspace create: %w\n%s", err, createOut)
	}
	ws := extractWorkspaceIDFromCreate(createOut)
	if ws == "" {
		return "", fmt.Errorf("herdr workspace create: no workspace_id in output: %s", createOut)
	}
	return ws, nil
}

// startAgent starts the claude agent in paneID and returns the effective agent identifier.
// For untagged panes: uses herdr agent start (returns name).
// For auto-tagged panes (agent_pane_busy): tries rename; falls back to pane run + detect.
func (b *Backend) startAgent(ctx context.Context, name, paneID string) (string, error) {
	out, err := b.herdrRun(ctx, nil, "agent", "start", name, "--kind", "claude", "--pane", paneID,
		"--", "--model", b.cfg.Model, "--permission-mode", "auto", "--max-turns", "1")
	if err == nil {
		b.waitForAgentReady(ctx, name, 90*time.Second)
		return name, nil
	}
	code, _, ok := herdrout.ParseHerdrErrorCode(out)
	if ok && code == "agent_not_ready" {
		return name, nil
	}
	if !ok || code != "agent_pane_busy" {
		return "", fmt.Errorf("herdr agent start: %w\n%s", err, out)
	}
	// Retry rename with backoff: the auto-detection window may be narrow.
	var renameErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
		}
		var renameOut string
		renameOut, renameErr = b.herdrRun(ctx, nil, "agent", "rename", paneID, name)
		_ = renameOut
		if renameErr == nil {
			break
		}
	}
	claudeCmd := "claude --model " + b.cfg.Model + " --permission-mode auto --max-turns 1"
	if renameErr == nil {
		if runOut, runErr := b.herdrRun(ctx, nil, "pane", "run", paneID, claudeCmd); runErr != nil {
			return "", fmt.Errorf("herdr pane run: %w\n%s", runErr, runOut)
		}
		b.waitForAgentReady(ctx, name, 90*time.Second)
		return name, nil
	}
	if runOut, runErr := b.herdrRun(ctx, nil, "pane", "run", paneID, claudeCmd); runErr != nil {
		return "", fmt.Errorf("herdr pane run (fallback): %w\n%s", runErr, runOut)
	}
	agentName, detectErr := b.waitForAgentDetection(ctx, paneID, 30*time.Second)
	if detectErr != nil {
		return paneID, nil
	}
	b.waitForAgentReady(ctx, agentName, 90*time.Second)
	return agentName, nil
}

// waitForAgentReady waits until the agent is idle (ready to accept prompts).
func (b *Backend) waitForAgentReady(ctx context.Context, agentRef string, timeout time.Duration) {
	agentRunner := func(ctx context.Context, argv ...string) (string, error) {
		return b.herdrRun(ctx, nil, argv...)
	}
	client := herdragent.New(agentRunner, b.agentOpts...)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		st := client.Observe(ctx, agentRef, 0)
		if st.Status == herdragent.StatusIdle && st.Settled {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// waitForAgentDetection polls herdr agent get until the pane is registered as a named agent.
func (b *Backend) waitForAgentDetection(ctx context.Context, paneID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := b.herdrRun(ctx, nil, "agent", "get", paneID)
		if err == nil {
			if st := parseAgentGetName(out); st != "" {
				return st, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("agent not detected in pane %s after %s", paneID, timeout)
}

func parseAgentGetName(out string) string {
	var v struct {
		Result struct {
			Agent struct {
				Agent string `json:"agent"`
			} `json:"agent"`
		} `json:"result"`
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &v) == nil && v.Result.Agent.Agent != "" {
			return v.Result.Agent.Agent
		}
	}
	return ""
}

// Prompt delivers text to the agent.
func (b *Backend) Prompt(ctx context.Context, agentRef, text string) error {
	if err := b.checkAgent(agentRef); err != nil {
		return err
	}
	out, err := b.herdrRun(ctx, nil, "agent", "prompt", agentRef, text)
	if err != nil {
		return fmt.Errorf("herdr agent prompt: %w\n%s", err, out)
	}
	return nil
}

// Observe returns the current herdragent.State. wait=true polls until terminal or ctx done.
// agent_not_found for a registered agent is treated as done (process exited).
func (b *Backend) Observe(ctx context.Context, agentRef string, wait bool) (herdragent.State, error) {
	if err := b.checkAgent(agentRef); err != nil {
		return herdragent.State{}, err
	}
	agentRunner := func(ctx context.Context, argv ...string) (string, error) {
		return b.herdrRun(ctx, nil, argv...)
	}
	client := herdragent.New(agentRunner, b.agentOpts...)

	snap := func() herdragent.State {
		st := client.Observe(ctx, agentRef, 0)
		if st.Status == herdragent.StatusUnknown {
			return herdragent.State{Status: herdragent.StatusDone, Settled: true}
		}
		return st
	}

	if !wait {
		return snap(), nil
	}

	var last herdragent.State
	for {
		last = snap()
		if last.Status == herdragent.StatusDone || last.Status == herdragent.StatusBlocked {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// Answer delivers a user response; Key uses send-keys, Text uses prompt.
func (b *Backend) Answer(ctx context.Context, agentRef string, in controller.AgentInput) error {
	if (in.Text == "") == (in.Key == "") {
		return fmt.Errorf("herdr backend: exactly one of Text or Key must be set")
	}
	if err := b.checkAgent(agentRef); err != nil {
		return err
	}
	if in.Key != "" {
		out, err := b.herdrRun(ctx, nil, "agent", "send-keys", agentRef, in.Key)
		if err != nil {
			return fmt.Errorf("herdr agent send-keys: %w\n%s", err, out)
		}
		return nil
	}
	out, err := b.herdrRun(ctx, nil, "agent", "prompt", agentRef, in.Text)
	if err != nil {
		return fmt.Errorf("herdr agent prompt (answer): %w\n%s", err, out)
	}
	return nil
}

// ReadAnswer returns the recent output for agentRef.
// Falls back to pane read when the agent has exited (e.g. after --max-turns 1).
func (b *Backend) ReadAnswer(ctx context.Context, agentRef string) (string, error) {
	if err := b.checkAgent(agentRef); err != nil {
		return "", err
	}
	out, err := b.herdrRun(ctx, nil, "agent", "read", agentRef, "--source", "recent-unwrapped", "--lines", "200")
	if err == nil {
		return out, nil
	}
	b.mu.Lock()
	e := b.entries[agentRef]
	b.mu.Unlock()
	if e == nil {
		return "", fmt.Errorf("herdr agent read: %w\n%s", err, out)
	}
	paneOut, paneErr := b.herdrRun(ctx, nil, "pane", "read", e.paneID, "--source", "recent-unwrapped", "--lines", "200")
	if paneErr != nil {
		return "", fmt.Errorf("herdr agent read: %w\n%s", err, out)
	}
	return paneOut, nil
}

// Teardown removes the worktree sandbox; sandboxID is the workspace ID from Provision.
func (b *Backend) Teardown(ctx context.Context, sandboxID string) error {
	b.mu.Lock()
	agRef, known := b.sandboxes[sandboxID]
	if known {
		delete(b.sandboxes, sandboxID)
		if e, ok := b.entries[agRef]; ok {
			e.tornDown = true
		}
	}
	b.mu.Unlock()

	out, err := b.herdrRun(ctx, nil, "worktree", "remove", "--workspace", sandboxID, "--force")
	if err == nil {
		return nil
	}
	code, _, parsed := herdrout.ParseHerdrErrorCode(out)
	if parsed && (code == "workspace_not_found" || code == "worktree_not_found") {
		return nil
	}
	if !known {
		return nil // unknown + non-fatal error: already gone
	}
	rmOut, rmErr := b.nexusRun(ctx, nil, "sandbox", "rm", sandboxID)
	if rmErr != nil {
		return fmt.Errorf("teardown %s: herdr: %w; sandbox rm: %v\n%s", sandboxID, err, rmErr, rmOut)
	}
	return nil
}

func (b *Backend) checkAgent(agentRef string) error {
	b.mu.Lock()
	e, ok := b.entries[agentRef]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("herdr backend: unknown agentRef %q", agentRef)
	}
	if e.tornDown {
		return fmt.Errorf("herdr backend: torn-down agentRef %q", agentRef)
	}
	return nil
}

// branchName derives a unique, git-safe branch name from project + ref + timestamp.
func branchName(project string, ref controller.ThreadRef, unixMicro int64) string {
	h := sha256.Sum256([]byte(string(ref)))
	slug := fmt.Sprintf("%x", h[:3])
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '-'
	}, project)
	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "task"
	}
	return fmt.Sprintf("ctrl/%s-%s-%x", safe, slug, unixMicro&0xFFFFFF)
}

// parsePaneID finds the pane_id for workspaceID in `nexus herdr list` output.
func parsePaneID(out, workspaceID string) string {
	needle := "workspace_id=" + workspaceID
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, needle) {
			continue
		}
		for _, f := range strings.Split(line, "\t") {
			if v, ok := strings.CutPrefix(f, "pane_id="); ok {
				return v
			}
		}
	}
	return ""
}

// parseWorkspaceForPath finds the workspace_id matching repoPath in `herdr workspace list` JSON.
func parseWorkspaceForPath(out, repoPath string) string {
	var parsed struct {
		Result struct {
			Workspaces []struct {
				WorkspaceID string `json:"workspace_id"`
				Worktree    struct {
					CheckoutPath string `json:"checkout_path"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &parsed) == nil && len(parsed.Result.Workspaces) > 0 {
			break
		}
	}
	for _, ws := range parsed.Result.Workspaces {
		if strings.TrimRight(ws.Worktree.CheckoutPath, "/") == strings.TrimRight(repoPath, "/") {
			return ws.WorkspaceID
		}
	}
	return ""
}

// extractWorkspaceIDFromCreate extracts workspace_id from `herdr workspace create` JSON output.
func extractWorkspaceIDFromCreate(out string) string {
	var v struct {
		Result struct {
			Workspace struct {
				WorkspaceID string `json:"workspace_id"`
			} `json:"workspace"`
		} `json:"result"`
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &v) == nil && v.Result.Workspace.WorkspaceID != "" {
			return v.Result.Workspace.WorkspaceID
		}
	}
	return ""
}
