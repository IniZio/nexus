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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/core/store"
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
	PermissionMode  string
	HerdrSocketPath string
	NexusBin        string
	ExtraEnv        []string
	WorktreeDir     string
}

type entry struct {
	paneID         string
	nexusHandle    string
	nexusSandboxID string
	wsID           string
	tornDown       bool
}

// Backend implements controller.AgentBackend.
type Backend struct {
	cfg               Config
	herdrRun          runner
	nexusRun          runner
	agentOpts         []herdragent.Option
	agentReadyTimeout time.Duration // 0 = skip idle-wait in waitForAgentReady (tests)

	mu        sync.Mutex
	entries   map[string]*entry // agentRef → pane/torn state
	sandboxes map[string]string
}

// New returns a Backend using real herdr and nexus binaries.
func New(cfg Config) *Backend {
	b := &Backend{
		cfg:               cfg,
		entries:           make(map[string]*entry),
		sandboxes:         make(map[string]string),
		agentReadyTimeout: 90 * time.Second,
	}
	b.herdrRun = b.defaultHerdrRun
	b.nexusRun = b.defaultNexusRun
	return b
}

// SetAgentOpts overrides herdragent options after construction.
func (b *Backend) SetAgentOpts(opts ...herdragent.Option) { b.agentOpts = opts }

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

// nexusStoreRoot returns the nexus state root as subprocesses see it.
// cfg.ExtraEnv may carry XDG_STATE_HOME (e.g. from the test harness); that
// must match what store.DefaultRoot() returns inside the subprocess, so we
// read from there first before falling back to the process env via
// store.DefaultRoot().
func (b *Backend) nexusStoreRoot() string {
	for _, kv := range b.cfg.ExtraEnv {
		if strings.HasPrefix(kv, "XDG_STATE_HOME=") {
			return strings.TrimPrefix(kv, "XDG_STATE_HOME=") + "/nexus"
		}
	}
	root, _ := store.DefaultRoot()
	return root
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
	safeBranch := strings.ReplaceAll(branch, "/", "-")

	// Fix (a): write a controller-owned marker before herdr worktree create so
	// the worktree.created hook (on-worktree-created.sh) and the Go auto path
	// in herdrWorktreeSandbox see it and skip auto-provisioning.  The marker is
	// keyed on the safe branch name and lives in the nexus state dir.  We defer
	// its removal so it is always cleaned up when Provision returns, regardless
	// of success or failure.
	var markerPath string
	if storeRoot := b.nexusStoreRoot(); storeRoot != "" {
		claimsDir := filepath.Join(storeRoot, "controller-wt-claims")
		if mkErr := os.MkdirAll(claimsDir, 0o755); mkErr == nil {
			markerPath = filepath.Join(claimsDir, safeBranch)
			_ = os.WriteFile(markerPath, []byte{}, 0o644)
			defer func() { //nolint:errcheck
				if markerPath != "" {
					os.Remove(markerPath)
				}
			}()
		}
	}

	wtArgs := []string{"worktree", "create", "--workspace", parentWS, "--branch", branch, "--no-focus"}
	if b.cfg.WorktreeDir != "" {
		_ = os.MkdirAll(b.cfg.WorktreeDir, 0o755)
		wtArgs = append(wtArgs, "--path", filepath.Join(b.cfg.WorktreeDir, safeBranch))
	}
	wtOut, err := b.herdrRun(ctx, nil, wtArgs...)
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
	// Binding written with correct principal — marker is no longer needed.
	// Remove it now so the hook pane (if still starting) skips via the
	// step-1 idempotency check (binding already exists) rather than via the
	// file-system marker, which cannot be held past Provision's return.
	if markerPath != "" {
		os.Remove(markerPath) //nolint:errcheck
		markerPath = ""
	}

	listOut, err := b.nexusRun(ctx, nil, "herdr", "list")
	if err != nil {
		return "", "", fmt.Errorf("nexus herdr list: %w\n%s", err, listOut)
	}
	paneID := parsePaneID(listOut, wsID)
	if paneID == "" {
		return "", "", fmt.Errorf("no pane_id for workspace %s; list output: %s", wsID, listOut)
	}
	nexusHandle := parseNexusHandle(listOut, wsID)
	nexusSandboxID := parseSandboxID(listOut, wsID)
	if nexusSandboxID == "" {
		return "", "", fmt.Errorf("nexus herdr list: no sandbox_id for workspace %s", wsID)
	}

	// Fix (b): verify the bound sandbox's recorded principal matches the one
	// we requested.  A non-empty mismatch means the hook raced us and created
	// the sandbox under the wrong identity; fail provision so the caller can
	// clean up and retry rather than silently operating as the wrong principal.
	// An empty boundPrincipal means the binding predates this field — allow it.
	if boundPrincipal := parsePrincipal(listOut, wsID); boundPrincipal != "" && boundPrincipal != principal {
		return "", "", fmt.Errorf("nexus herdr provision: principal mismatch for workspace %s: bound=%q requested=%q", wsID, boundPrincipal, principal)
	}

	if err := b.waitForWorkspaceMount(ctx, nexusSandboxID); err != nil {
		return "", "", fmt.Errorf("workspace mount: %w", err)
	}

	if err := b.verifyClaudeInGuest(ctx, nexusSandboxID); err != nil {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanCancel()
		_, _ = b.nexusRun(cleanCtx, nil, "sandbox", "rm", nexusSandboxID)
		_, _ = b.herdrRun(cleanCtx, nil, "worktree", "remove", wsID)
		return "", "", err
	}

	agentName := "ctrl-" + wsID
	agentRef, err := b.startAgent(ctx, agentName, paneID, nexusSandboxID)
	if err != nil {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanCancel()
		_, _ = b.nexusRun(cleanCtx, nil, "sandbox", "rm", nexusSandboxID)
		_, _ = b.herdrRun(cleanCtx, nil, "worktree", "remove", wsID)
		return "", "", fmt.Errorf("start agent: %w", err)
	}

	b.mu.Lock()
	b.entries[agentRef] = &entry{
		paneID:         paneID,
		nexusHandle:    nexusHandle,
		nexusSandboxID: nexusSandboxID,
		wsID:           wsID,
	}
	b.sandboxes[nexusSandboxID] = agentRef
	b.mu.Unlock()

	return nexusSandboxID, agentRef, nil
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
func (b *Backend) permMode() string {
	if b.cfg.PermissionMode != "" {
		return b.cfg.PermissionMode
	}
	return "auto"
}

// fallbackMarkers are strings emitted by nexus-guest-shell when it falls back to the host.
var fallbackMarkers = []string{
	"nexus-guest-shell: FALLBACK host shell:",
	"not a nexus space",
	"no nexus sandbox binding",
}

// checkNoFallbackMarkers returns an error if out contains any fallback marker.
func checkNoFallbackMarkers(out string) error {
	for _, m := range fallbackMarkers {
		if strings.Contains(out, m) {
			return fmt.Errorf("fallback marker %q detected: pane is not in guest", m)
		}
	}
	return nil
}

// shellPromptVisible returns true when out contains a line ending with "# " or "$ "
// which indicates an interactive shell prompt is ready for input.
func shellPromptVisible(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " \t")
		if strings.HasSuffix(line, "#") || strings.HasSuffix(line, "$") {
			return true
		}
	}
	return false
}

// extractHostnameFromPrompt returns the hostname embedded in a bash prompt line
// of the form "user@HOSTNAME:PATH#" or "user@HOSTNAME:PATH$". Returns "" when
// no such line is found.
func extractHostnameFromPrompt(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " \t")
		if !strings.HasSuffix(line, "#") && !strings.HasSuffix(line, "$") {
			continue
		}
		atIdx := strings.Index(line, "@")
		if atIdx < 0 {
			continue
		}
		rest := line[atIdx+1:]
		colonIdx := strings.Index(rest, ":")
		if colonIdx < 0 {
			continue
		}
		h := rest[:colonIdx]
		if h != "" {
			return h
		}
	}
	return ""
}

// verifyPaneInGuest confirms the pane is running inside the sandbox guest VM.
// It waits for a bash prompt of the form "user@HOSTNAME:PATH#", extracts the
// hostname from the prompt, then cross-checks with "nexus exec <sandbox> -- hostname".
// Returns an error if the pane is on the host or shows fallback markers.
func (b *Backend) verifyPaneInGuest(ctx context.Context, paneID, nexusSandboxID string) error {
	// Wait for the guest bash prompt. The prompt embeds the hostname in
	// "user@HOSTNAME:PATH# " form so we can read it without sending keys.
	// bash -l takes time to run login scripts; 120s covers slow VM boots.
	promptDeadline := time.Now().Add(120 * time.Second)
	var paneHostname string
	for time.Now().Before(promptDeadline) {
		out, _ := b.herdrRun(ctx, nil, "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "150")
		if err := checkNoFallbackMarkers(out); err != nil {
			return fmt.Errorf("pane %s initial output: %w", paneID, err)
		}
		if h := extractHostnameFromPrompt(out); h != "" {
			paneHostname = h
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if paneHostname == "" {
		return fmt.Errorf("pane %s: no user@HOSTNAME prompt within 120s — bash login may be hung or prompt format unexpected", paneID)
	}

	// Cross-check against the canonical guest hostname.
	guestOut, err := b.nexusRun(ctx, nil, "exec", nexusSandboxID, "--", "hostname")
	if err != nil {
		return fmt.Errorf("nexus exec %s hostname: %w", nexusSandboxID, err)
	}
	guestHostname := strings.TrimSpace(guestOut)
	if paneHostname != guestHostname {
		return fmt.Errorf("pane %s hostname %q != guest %q: pane is not running inside the guest VM", paneID, paneHostname, guestHostname)
	}
	return nil
}

func (b *Backend) verifyClaudeInGuest(ctx context.Context, sbID string) error {
	out, err := b.nexusRun(ctx, nil, "exec", sbID, "--", "sh", "-lc", "command -v claude")
	if err != nil {
		return fmt.Errorf("claude check in guest %s: %w", sbID, err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("claude not found in guest %s: image is missing the claude recipe", sbID)
	}
	return nil
}

func (b *Backend) startAgent(ctx context.Context, name, paneID, nexusSandboxID string) (string, error) {
	// Safety gate: verify the pane is inside the guest before starting the agent.
	// On mismatch the agent would run on the host; fail closed.
	if err := b.verifyPaneInGuest(ctx, paneID, nexusSandboxID); err != nil {
		return "", fmt.Errorf("guest pane verification failed — refusing to start agent: %w", err)
	}

	out, err := b.herdrRun(ctx, nil, "agent", "start", name, "--kind", "claude", "--pane", paneID,
		"--", "--model", b.cfg.Model, "--permission-mode", b.permMode(), "--max-turns", "1")
	if err == nil {
		b.waitForAgentReady(ctx, name, 90*time.Second)
		return name, nil
	}
	code, _, ok := herdrout.ParseHerdrErrorCode(out)
	if ok && code == "agent_not_ready" {
		// herdr could not confirm the agent is ready — verify the pane is still
		// running claude (prompt absent) rather than silently succeeding when
		// claude exited immediately (e.g. command not found).
		time.Sleep(2 * time.Second)
		paneOut, _ := b.herdrRun(ctx, nil, "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "50")
		if shellPromptVisible(paneOut) {
			return "", fmt.Errorf("agent_not_ready and shell prompt visible: claude exited immediately (command not found, crash, or permission error)\npane tail:\n%s", paneOut)
		}
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
	claudeCmd := "claude --model " + b.cfg.Model + " --permission-mode " + b.permMode() + " --max-turns 1"
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
		return "", fmt.Errorf("agent detection failed after pane run — pane may not be running claude: %w", detectErr)
	}
	b.waitForAgentReady(ctx, agentName, 90*time.Second)
	return agentName, nil
}

// waitForWorkspaceMount polls until /workspace is accessible inside the sandbox.
func (b *Backend) waitForWorkspaceMount(ctx context.Context, sandboxID string) error {
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := b.nexusRun(ctx, nil, "exec", sandboxID, "--", "sh", "-c", "test -e /workspace/.git || test -f /workspace/README.md"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("workspace mount not ready in sandbox %s after 120s", sandboxID)
}

// waitForAgentReady waits until the agent is idle (ready to accept prompts).
func (b *Backend) waitForAgentReady(ctx context.Context, agentRef string, _ time.Duration) {
	if b.agentReadyTimeout == 0 {
		return
	}
	agentRunner := func(ctx context.Context, argv ...string) (string, error) {
		return b.herdrRun(ctx, nil, argv...)
	}
	client := herdragent.New(agentRunner, b.agentOpts...)
	deadline := time.Now().Add(b.agentReadyTimeout)
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
		key := strings.TrimSuffix(in.Key, "\r")
		sendEnter := key != in.Key
		var argv []string
		if key != "" {
			argv = []string{"agent", "send-keys", agentRef, key}
			if sendEnter {
				argv = append(argv, "Enter")
			}
		} else {
			argv = []string{"agent", "send-keys", agentRef, "Enter"}
		}
		out, err := b.herdrRun(ctx, nil, argv...)
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

// Teardown removes the nexus sandbox and then the herdr worktree.
// sandboxID is the nexus sb-... id returned by Provision.
//
// Safety contract: sandbox rm is ONLY called with the exact sb-... id recorded
// at provision time. Workspace ids, handle prefixes, or guessed ids are never
// passed to rm, because nexus resolves by prefix and could remove the wrong VM.
//
// Idempotency is detected by the exact id being absent from nexus ps, not by
// error-string matching. An unknown sandboxID (never provisioned by this backend)
// returns an error; it is never silently ignored.
// Restart re-launches the guest agent after a stop/start cycle.
func (b *Backend) Restart(ctx context.Context, sandboxID, agentRef string) (string, error) {
	return "", controller.ErrNotImplemented
}

func (b *Backend) Teardown(ctx context.Context, sandboxID string) error {
	b.mu.Lock()
	agRef, known := b.sandboxes[sandboxID]
	var nexusSandboxID, nexusHandle, wsID string
	if known {
		if e, ok := b.entries[agRef]; ok {
			if !e.tornDown {
				e.tornDown = true
			}
			nexusSandboxID = e.nexusSandboxID
			nexusHandle = e.nexusHandle
			wsID = e.wsID
		}
	}
	b.mu.Unlock()

	if !known {
		return fmt.Errorf("teardown %s: nexus sandbox id unknown — never provisioned by this backend; refusing sandbox rm", sandboxID)
	}

	if nexusSandboxID != "" {
		psOut, _ := b.nexusRun(ctx, nil, "ps")
		psHandle, found := parsePSLine(psOut, nexusSandboxID)
		if found {
			if nexusHandle != "" && psHandle != nexusHandle {
				return fmt.Errorf("teardown %s: id %s found with handle %q, expected %q; refusing rm", sandboxID, nexusSandboxID, psHandle, nexusHandle)
			}
			rmOut, rmErr := b.nexusRun(ctx, nil, "sandbox", "rm", nexusSandboxID)
			if rmErr != nil {
				return fmt.Errorf("teardown %s: sandbox rm %s: %w\n%s", sandboxID, nexusSandboxID, rmErr, rmOut)
			}
		}
	}

	return b.removeWorktree(ctx, wsID)
}

// removeWorktree removes the herdr worktree for sandboxID. workspace_not_found
// and worktree_not_found are treated as success (idempotent).
func (b *Backend) removeWorktree(ctx context.Context, sandboxID string) error {
	wtOut, wtErr := b.herdrRun(ctx, nil, "worktree", "remove", "--workspace", sandboxID, "--force")
	if wtErr == nil {
		return nil
	}
	code, _, parsed := herdrout.ParseHerdrErrorCode(wtOut)
	if parsed && (code == "workspace_not_found" || code == "worktree_not_found") {
		return nil
	}
	return fmt.Errorf("teardown %s: herdr worktree remove: %w\n%s", sandboxID, wtErr, wtOut)
}

// parsePSLine scans nexus ps output for a line whose last field equals exactID.
// Returns the handle (first field) and true when found.
func parsePSLine(out, exactID string) (handle string, found bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "HANDLE" || strings.Contains(fields[1], "sandbox") {
			continue
		}
		if fields[len(fields)-1] == exactID {
			return fields[0], true
		}
	}
	return "", false
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

// parseNexusHandle finds the nexus sandbox handle for workspaceID in `nexus herdr list` output.
func parseNexusHandle(out, workspaceID string) string {
	return parseListField(out, workspaceID, "handle=")
}

// parsePrincipal finds the principal field for workspaceID in `nexus herdr list` output.
// Returns "" when the field is absent (old binding without principal).
func parsePrincipal(out, workspaceID string) string {
	return parseListField(out, workspaceID, "principal=")
}

// parseSandboxID finds the exact sb-... nexus sandbox id for workspaceID in `nexus herdr list` output.
func parseSandboxID(out, workspaceID string) string {
	return parseListField(out, workspaceID, "sandbox_id=")
}

func parseListField(out, workspaceID, prefix string) string {
	needle := "workspace_id=" + workspaceID
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, needle) {
			continue
		}
		for _, f := range strings.Split(line, "\t") {
			if v, ok := strings.CutPrefix(f, prefix); ok {
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
