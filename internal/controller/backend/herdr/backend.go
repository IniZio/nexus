// Package herdr implements controller.AgentBackend backed by herdr + nexus CLIs.
package herdr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/core/sandboxhandle"
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
	branch         string // git branch ctrl/… created during Provision
	tornDown       bool
}

// Backend implements controller.AgentBackend.
type Backend struct {
	cfg       Config
	herdrRun  runner
	nexusRun  runner
	gitRun    runner            // injected for tests; real impl calls git directly
	agentOpts []herdragent.Option
	sleepFn   func(time.Duration) // injected for tests
	nowFn     func() time.Time    // injected for tests

	mu        sync.Mutex
	entries   map[string]*entry // agentRef → pane/torn state
	sandboxes map[string]string
	torn      map[string]bool // sandboxIDs torn down by this process; repeat Teardown is a no-op
}

// New returns a Backend using real herdr and nexus binaries.
func New(cfg Config) *Backend {
	b := &Backend{
		cfg:       cfg,
		entries:   make(map[string]*entry),
		sandboxes: make(map[string]string),
		torn:      make(map[string]bool),
		sleepFn:   time.Sleep,
		nowFn:     time.Now,
	}
	b.herdrRun = b.defaultHerdrRun
	b.nexusRun = b.defaultNexusRun
	b.gitRun = b.defaultGitRun
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
		gitRun:    func(_ context.Context, _ []string, _ ...string) (string, error) { return "", nil },
		agentOpts: opts,
		entries:   make(map[string]*entry),
		sandboxes: make(map[string]string),
		torn:      make(map[string]bool),
		sleepFn:   func(time.Duration) {}, // no-op for tests
		nowFn:     time.Now,
	}
	return b
}

// newWithRunnersGit creates a Backend with injected herdr, nexus, and git runners for testing.
func newWithRunnersGit(cfg Config, hr, nr, gr runner, opts ...herdragent.Option) *Backend {
	b := newWithRunners(cfg, hr, nr, opts...)
	b.gitRun = gr
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

func (b *Backend) defaultGitRun(ctx context.Context, _ []string, argv ...string) (string, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git not found: %w", err)
	}
	return runCmd(ctx, gitBin, nil, argv...)
}

// worktreeVolumeNames returns the exact named volumes for a nexus sandbox handle.
// Matches herdrWorktreeVolumeNames in internal/cli/cmd_herdr_plugin.go.
func worktreeVolumeNames(handle string) []string {
	slug := sandboxhandle.Slug(handle)
	return []string{
		slug + "-docker",
		slug + "-agentcfg",
		slug + "-gocache",
		slug + "-gopath",
		slug + "-nexusstate",
	}
}

// removeVolumes lists nexus volumes and removes exact names for handle's volume set.
// Logs failures; never returns an error (missing volumes are benign).
func (b *Backend) removeVolumes(ctx context.Context, handle string) {
	if handle == "" {
		return
	}
	names := worktreeVolumeNames(handle)
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	lsOut, err := b.nexusRun(ctx, nil, "volume", "ls")
	if err != nil {
		slog.Error("teardown: volume ls", "handle", handle, "err", err)
		return
	}
	for _, line := range strings.Split(lsOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if !nameSet[name] {
			continue
		}
		if _, rmErr := b.nexusRun(ctx, nil, "volume", "rm", name); rmErr != nil {
			slog.Error("teardown: volume rm", "volume", name, "err", rmErr)
		}
	}
}

// deleteBranch deletes the git branch (must start with "ctrl/") from cfg.RepoPath.
// Logs failures; never returns an error.
func (b *Backend) deleteBranch(ctx context.Context, branch string) {
	if branch == "" || !strings.HasPrefix(branch, "ctrl/") {
		return
	}
	repoPath := b.cfg.RepoPath
	if repoPath == "" {
		return
	}
	if _, err := b.gitRun(ctx, nil, "-C", repoPath, "branch", "-D", branch); err != nil {
		slog.Error("teardown: git branch -D", "branch", branch, "err", err)
	}
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

// filteredOSEnv returns os.Environ() with herdr/claude session vars removed so
// child processes do not inherit the launching shell's pane/workspace context.
func filteredOSEnv() []string {
	skipPrefixes := []string{
		"HERDR_PANE_ID=", "HERDR_TAB_ID=", "HERDR_WORKSPACE_ID=",
		"HERDR_ENV=", "CLAUDECODE=", "CLAUDE_CODE_",
	}
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		skip := false
		for _, p := range skipPrefixes {
			if strings.HasPrefix(kv, p) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}

func runCmd(ctx context.Context, bin string, extraEnv []string, argv ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if len(extraEnv) > 0 {
		cmd.Env = append(filteredOSEnv(), extraEnv...)
	}
	err := cmd.Run()
	return buf.String(), err
}

func runCmdPTY(ctx context.Context, bin string, extraEnv []string, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, argv...)
	if len(extraEnv) > 0 {
		cmd.Env = append(filteredOSEnv(), extraEnv...)
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
	repoPath := project
	if repoPath == "" {
		repoPath = b.cfg.RepoPath
	}
	parentWS, err := b.findOrCreateWorkspace(ctx, repoPath)
	if err != nil {
		return "", "", err
	}

	branch := branchName(project, ref, time.Now().UnixMicro())
	safeBranch := strings.ReplaceAll(branch, "/", "-")

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

	// Rollback: on any error return after worktree creation, remove sandbox (if
	// bound) then the worktree, volumes, and branch. committed is set to true only on success.
	var rollbackSandboxID string
	// Pre-compute the expected nexus handle from the repo name and worktree dir
	// basename so rollback can remove volumes even when worktree-sandbox fails
	// before the nexus herdr list call that would normally reveal the handle.
	rollbackNexusHandle := sandboxhandle.WorktreeHandle(filepath.Base(repoPath), safeBranch)
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanCancel()
		if rollbackSandboxID != "" {
			if _, rbErr := b.nexusRun(cleanCtx, nil, "sandbox", "rm", rollbackSandboxID); rbErr != nil {
				slog.Error("provision rollback: sandbox rm", "sandbox", rollbackSandboxID, "err", rbErr)
			}
		}
		if _, rbErr := b.herdrRun(cleanCtx, nil, "worktree", "remove", "--workspace", wsID, "--force"); rbErr != nil {
			slog.Error("provision rollback: worktree remove", "workspace", wsID, "err", rbErr)
		}
		b.removeVolumes(cleanCtx, rollbackNexusHandle)
		b.deleteBranch(cleanCtx, branch)
	}()

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
	nexusHandle := parseNexusHandle(listOut, wsID)
	nexusSandboxID := parseSandboxID(listOut, wsID)
	if nexusSandboxID == "" {
		return "", "", fmt.Errorf("nexus herdr list: no sandbox_id for workspace %s", wsID)
	}
	rollbackSandboxID = nexusSandboxID   // sandbox is now bound; rollback must rm it
	rollbackNexusHandle = nexusHandle

	boundPrincipal := parsePrincipal(listOut, wsID)
	if boundPrincipal == "" || boundPrincipal != principal {
		return "", "", fmt.Errorf("nexus herdr provision: principal mismatch for workspace %s: bound=%q requested=%q", wsID, boundPrincipal, principal)
	}

	recPrincipal, recErr := b.sandboxPrincipalFromRecord(nexusSandboxID)
	if recErr != nil {
		return "", "", fmt.Errorf("nexus herdr provision: sandbox record check: %w", recErr)
	}
	if recPrincipal == "" || recPrincipal != principal {
		return "", "", fmt.Errorf("nexus herdr provision: principal mismatch for sandbox %s: record=%q requested=%q", nexusSandboxID, recPrincipal, principal)
	}

	if err := b.waitForWorkspaceMount(ctx, nexusSandboxID); err != nil {
		return "", "", fmt.Errorf("workspace mount: %w", err)
	}

	if err := b.verifyClaudeInGuest(ctx, nexusSandboxID); err != nil {
		return "", "", err
	}

	agentName := agentNameFromWsID(wsID)
	agentRef, err := b.startAgent(ctx, agentName, paneID, nexusSandboxID, controller.PermModeFromCtx(ctx))
	if err != nil {
		return "", "", fmt.Errorf("start agent: %w", err)
	}

	b.mu.Lock()
	b.entries[agentRef] = &entry{
		paneID:         paneID,
		nexusHandle:    nexusHandle,
		nexusSandboxID: nexusSandboxID,
		wsID:           wsID,
		branch:         branch,
	}
	b.sandboxes[nexusSandboxID] = agentRef
	b.mu.Unlock()

	committed = true
	return nexusSandboxID, agentRef, nil
}

func (b *Backend) findOrCreateWorkspace(ctx context.Context, repoPath string) (string, error) {
	listOut, err := b.herdrRun(ctx, nil, "workspace", "list")
	if err != nil {
		return "", fmt.Errorf("herdr workspace list: %w\n%s", err, listOut)
	}
	if ws := parseWorkspaceForPath(listOut, repoPath); ws != "" {
		return ws, nil
	}
	createOut, err := b.herdrRun(ctx, nil, "workspace", "create", "--cwd", repoPath)
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
	herdragent.GuestShellFallbackMarker,
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

// startAgent launches the claude agent in paneID. permMode overrides the
// backend-default permission mode when non-empty; "" uses b.permMode().
func (b *Backend) startAgent(ctx context.Context, name, paneID, nexusSandboxID, permMode string) (string, error) {
	// Safety gate: verify the pane is inside the guest before starting the agent.
	// On mismatch the agent would run on the host; fail closed.
	if err := b.verifyPaneInGuest(ctx, paneID, nexusSandboxID); err != nil {
		return "", fmt.Errorf("guest pane verification failed — refusing to start agent: %w", err)
	}
	if permMode == "" {
		permMode = b.permMode()
	}

	out, err := b.herdrRun(ctx, nil, "agent", "start", name, "--kind", "claude", "--pane", paneID,
		"--timeout", "300000",
		"--", "--model", b.cfg.Model, "--permission-mode", permMode, "--max-turns", "1")
	if err == nil {
		if err := b.waitForAgentReady(ctx, name, 5*time.Minute); err != nil {
			return "", fmt.Errorf("agent not ready: %w", err)
		}
		return name, nil
	}
	code, _, ok := herdrout.ParseHerdrErrorCode(out)
	if ok && code == "agent_not_ready" {
		// herdr could not confirm the agent is ready — verify the pane is still
		// running claude (prompt absent) rather than silently succeeding when
		// claude exited immediately (e.g. command not found).
		b.sleepFn(2 * time.Second)
		paneOut, _ := b.herdrRun(ctx, nil, "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "50")
		if shellPromptVisible(paneOut) {
			return "", fmt.Errorf("agent_not_ready and shell prompt visible: claude exited immediately (command not found, crash, or permission error)\npane tail:\n%s", paneOut)
		}
		if err := b.waitForAgentReady(ctx, name, 5*time.Minute); err != nil {
			return "", fmt.Errorf("agent not ready: %w", err)
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
	claudeCmd := "claude --model " + b.cfg.Model + " --permission-mode " + permMode + " --max-turns 1"
	if renameErr == nil {
		if runOut, runErr := b.herdrRun(ctx, nil, "pane", "run", paneID, claudeCmd); runErr != nil {
			return "", fmt.Errorf("herdr pane run: %w\n%s", runErr, runOut)
		}
		if err := b.waitForAgentReady(ctx, name, 5*time.Minute); err != nil {
			return "", fmt.Errorf("agent not ready: %w", err)
		}
		return name, nil
	}
	if runOut, runErr := b.herdrRun(ctx, nil, "pane", "run", paneID, claudeCmd); runErr != nil {
		return "", fmt.Errorf("herdr pane run (fallback): %w\n%s", runErr, runOut)
	}
	agentName, detectErr := b.waitForAgentDetection(ctx, paneID, 5*time.Minute)
	if detectErr != nil {
		return "", fmt.Errorf("agent detection failed after pane run — pane may not be running claude: %w", detectErr)
	}
	// Rename auto-detected agent to the expected ctrl- name so rediscovery works.
	if agentName != name {
		if _, renameErr := b.herdrRun(ctx, nil, "agent", "rename", agentName, name); renameErr == nil {
			agentName = name
		}
	}
	if err := b.waitForAgentReady(ctx, agentName, 5*time.Minute); err != nil {
		return "", fmt.Errorf("agent not ready: %w", err)
	}
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
// Returns an error if the agent does not become ready within timeout.
func (b *Backend) waitForAgentReady(ctx context.Context, agentRef string, timeout time.Duration) error {
	agentRunner := func(ctx context.Context, argv ...string) (string, error) {
		return b.herdrRun(ctx, nil, argv...)
	}
	client := herdragent.New(agentRunner, b.agentOpts...)
	sleepFn := b.sleepFn
	if sleepFn == nil {
		sleepFn = time.Sleep
	}
	nowFn := b.nowFn
	if nowFn == nil {
		nowFn = time.Now
	}
	deadline := nowFn().Add(timeout)
	for nowFn().Before(deadline) && ctx.Err() == nil {
		st := client.Observe(ctx, agentRef, 0)
		if st.Status == herdragent.StatusIdle && st.Settled {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sleepFn(1 * time.Second)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("herdr backend: agent %q not ready after %s", agentRef, timeout)
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
	if err := b.checkAgent(ctx, agentRef); err != nil {
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
	if err := b.checkAgent(ctx, agentRef); err != nil {
		return herdragent.State{}, err
	}
	agentRunner := func(ctx context.Context, argv ...string) (string, error) {
		return b.herdrRun(ctx, nil, argv...)
	}
	client := herdragent.New(agentRunner, b.agentOpts...)

	if !wait {
		st := client.Observe(ctx, agentRef, 0)
		if st.Status == herdragent.StatusUnknown {
			return herdragent.State{}, fmt.Errorf("herdr agent %q: status unknown", agentRef)
		}
		return st, nil
	}

	var last herdragent.State
	for {
		st := client.Observe(ctx, agentRef, 0)
		if st.Status != herdragent.StatusUnknown {
			last = st
		}
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
	if err := b.checkAgent(ctx, agentRef); err != nil {
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
	if err := b.checkAgent(ctx, agentRef); err != nil {
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
// Restart re-launches the guest agent after a stop/start cycle; sandbox must be running.
func (b *Backend) Restart(ctx context.Context, sandboxID, agentRef string) (string, error) {
	b.mu.Lock()
	e, ok := b.entries[agentRef]
	b.mu.Unlock()
	if !ok {
		var discoverErr error
		_, e, discoverErr = b.rediscoverBySandboxID(ctx, sandboxID)
		if discoverErr != nil {
			return "", fmt.Errorf("restart %s: %w", sandboxID, discoverErr)
		}
	}

	// Verify the pane is alive and in the guest. After a sandbox stop/start the
	// guest shell may have exited; check once and recreate the pane if needed.
	paneID := e.paneID
	paneOut, _ := b.herdrRun(ctx, nil, "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "50")
	if extractHostnameFromPrompt(paneOut) == "" {
		reopenOut, reopenErr := b.nexusRun(ctx, nil, "herdr", "space-open-pane", e.wsID)
		if reopenErr != nil {
			return "", fmt.Errorf("restart %s: space-open-pane: %w\n%s", sandboxID, reopenErr, reopenOut)
		}
		listOut, listErr := b.nexusRun(ctx, nil, "herdr", "list")
		if listErr == nil {
			if newPaneID := parsePaneID(listOut, e.wsID); newPaneID != "" {
				paneID = newPaneID
			}
		}
	}

	newAgentName := agentNameFromWsID(e.wsID)
	newAgentRef, err := b.startAgent(ctx, newAgentName, paneID, e.nexusSandboxID, "")
	if err != nil {
		return "", fmt.Errorf("restart %s: start agent: %w", sandboxID, err)
	}

	newEntry := &entry{
		paneID:         paneID,
		nexusHandle:    e.nexusHandle,
		nexusSandboxID: e.nexusSandboxID,
		wsID:           e.wsID,
		branch:         e.branch,
	}
	b.mu.Lock()
	delete(b.entries, agentRef)
	b.entries[newAgentRef] = newEntry
	b.sandboxes[sandboxID] = newAgentRef
	b.mu.Unlock()
	return newAgentRef, nil
}

func (b *Backend) Teardown(ctx context.Context, sandboxID string) error {
	b.mu.Lock()
	if b.torn[sandboxID] {
		b.mu.Unlock()
		return nil
	}
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
		var e *entry
		var discoverErr error
		agRef, e, discoverErr = b.rediscoverBySandboxID(ctx, sandboxID)
		if discoverErr != nil {
			return fmt.Errorf("teardown %s: %w", sandboxID, discoverErr)
		}
		nexusSandboxID = e.nexusSandboxID
		nexusHandle = e.nexusHandle
		wsID = e.wsID
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

	if err := b.removeWorktree(ctx, wsID); err != nil {
		return err
	}
	b.removeVolumes(ctx, nexusHandle)
	// Retrieve branch from entry for deletion (safe: tornDown is set, entry still present).
	b.mu.Lock()
	var branch string
	if e, ok := b.entries[agRef]; ok {
		branch = e.branch
	}
	delete(b.entries, agRef)
	delete(b.sandboxes, sandboxID)
	b.torn[sandboxID] = true
	b.mu.Unlock()
	b.deleteBranch(ctx, branch)
	return nil
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

// rediscoverEntry reconstructs an entry from live nexus herdr list; agentRef must be ctrl-<...>.
func (b *Backend) rediscoverEntry(ctx context.Context, agentRef string) (*entry, error) {
	if !strings.HasPrefix(agentRef, "ctrl-") {
		return nil, fmt.Errorf("herdr backend: cannot rediscover agentRef %q: not a ctrl- reference", agentRef)
	}
	listOut, err := b.nexusRun(ctx, nil, "herdr", "list")
	if err != nil {
		return nil, fmt.Errorf("herdr backend: rediscover %q: nexus herdr list: %w", agentRef, err)
	}
	wsID := parseWorkspaceIDByAgentName(listOut, agentRef)
	if wsID == "" {
		return nil, fmt.Errorf("herdr backend: agentRef %q not found in nexus herdr list", agentRef)
	}
	paneID := parsePaneID(listOut, wsID)
	if paneID == "" {
		return nil, fmt.Errorf("herdr backend: agentRef %q (workspace %s) not found in nexus herdr list", agentRef, wsID)
	}
	e := &entry{
		paneID:         paneID,
		nexusHandle:    parseNexusHandle(listOut, wsID),
		nexusSandboxID: parseSandboxID(listOut, wsID),
		wsID:           wsID,
	}
	b.mu.Lock()
	b.entries[agentRef] = e
	if e.nexusSandboxID != "" {
		b.sandboxes[e.nexusSandboxID] = agentRef
	}
	b.mu.Unlock()
	return e, nil
}

// rediscoverBySandboxID reconstructs an entry from nexus herdr list on map miss.
func (b *Backend) rediscoverBySandboxID(ctx context.Context, sandboxID string) (agRef string, e *entry, err error) {
	listOut, listErr := b.nexusRun(ctx, nil, "herdr", "list")
	if listErr != nil {
		return "", nil, fmt.Errorf("herdr backend: rediscover sandbox %q: nexus herdr list: %w", sandboxID, listErr)
	}
	wsID := parseWorkspaceIDBySandboxID(listOut, sandboxID)
	if wsID == "" {
		return "", nil, fmt.Errorf("herdr backend: sandbox %q not found in nexus herdr list; never provisioned or already torn down", sandboxID)
	}
	agRef = agentNameFromWsID(wsID)
	e = &entry{
		paneID:         parsePaneID(listOut, wsID),
		nexusHandle:    parseNexusHandle(listOut, wsID),
		nexusSandboxID: sandboxID,
		wsID:           wsID,
	}
	b.mu.Lock()
	b.entries[agRef] = e
	b.sandboxes[sandboxID] = agRef
	b.mu.Unlock()
	return agRef, e, nil
}

func (b *Backend) checkAgent(ctx context.Context, agentRef string) error {
	b.mu.Lock()
	e, ok := b.entries[agentRef]
	b.mu.Unlock()
	if !ok {
		var err error
		e, err = b.rediscoverEntry(ctx, agentRef)
		if err != nil {
			return fmt.Errorf("herdr backend: unknown agentRef %q: %w", agentRef, err)
		}
	}
	if e.tornDown {
		return fmt.Errorf("herdr backend: torn-down agentRef %q", agentRef)
	}
	return nil
}

// branchName derives a unique, git-safe branch name from project + ref + timestamp.
// Uses filepath.Base(project) so absolute repo paths produce short branch names.
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
	}, filepath.Base(project))
	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "task"
	}
	return fmt.Sprintf("ctrl/%s-%s-%x", safe, slug, unixMicro&0xFFFFFF)
}

// agentNameFromWsID derives a herdr-valid agent name from a workspace ID.
// Herdr requires ^[a-z][a-z0-9_-]{0,31}$ (max 32 chars). Workspace IDs are
// case-sensitive, so a short stable hash of the original ID is appended to
// prevent collisions after lowercasing (e.g. "wDR" vs "wdr").
func agentNameFromWsID(wsID string) string {
	const pfx = "ctrl-"
	h := sha256.Sum256([]byte(wsID))
	sfx := fmt.Sprintf("-%x", h[:2]) // 5 chars: "-XXXX"
	var sb strings.Builder
	sb.WriteString(pfx)
	for _, r := range strings.ToLower(wsID) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	body := sb.String()
	if len(body)+len(sfx) > 32 {
		body = body[:32-len(sfx)]
	}
	return body + sfx
}

// parseWorkspaceIDByAgentName returns the workspace_id from nexus herdr list
// output whose agentNameFromWsID matches agentRef.
func parseWorkspaceIDByAgentName(out, agentRef string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		for _, f := range strings.Split(line, "\t") {
			if wsID, ok := strings.CutPrefix(f, "workspace_id="); ok {
				if agentNameFromWsID(wsID) == agentRef {
					return wsID
				}
			}
		}
	}
	return ""
}

// parsePaneID finds the pane_id for workspaceID in `nexus herdr list` output.
func parsePaneID(out, workspaceID string) string {
	needle := "workspace_id=" + workspaceID
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Split(line, "\t")
		if !containsField(fields, needle) {
			continue
		}
		for _, f := range fields {
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

// sandboxPrincipalFromRecord reads the principal from the on-disk sandbox record.
// The record is at <storeRoot>/sandboxes/<sandboxID>/record.json.
func (b *Backend) sandboxPrincipalFromRecord(sandboxID string) (string, error) {
	storeRoot := b.nexusStoreRoot()
	recordPath := filepath.Join(storeRoot, "sandboxes", sandboxID, "record.json")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return "", fmt.Errorf("read sandbox record %s: %w", sandboxID, err)
	}
	var rec struct {
		Principal string `json:"principal"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return "", fmt.Errorf("decode sandbox record %s: %w", sandboxID, err)
	}
	return rec.Principal, nil
}

// parseSandboxID finds the exact sb-... nexus sandbox id for workspaceID in `nexus herdr list` output.
func parseSandboxID(out, workspaceID string) string {
	return parseListField(out, workspaceID, "sandbox_id=")
}

// parseWorkspaceIDBySandboxID finds the workspace_id for a given sandbox_id in
// `nexus herdr list` output.
func parseWorkspaceIDBySandboxID(out, sandboxID string) string {
	needle := "sandbox_id=" + sandboxID
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Split(line, "\t")
		if !containsField(fields, needle) {
			continue
		}
		for _, f := range fields {
			if v, ok := strings.CutPrefix(f, "workspace_id="); ok {
				return v
			}
		}
	}
	return ""
}

// containsField reports whether fields contains exactly needle (not as a substring).
func containsField(fields []string, needle string) bool {
	for _, f := range fields {
		if f == needle {
			return true
		}
	}
	return false
}

func parseListField(out, workspaceID, prefix string) string {
	needle := "workspace_id=" + workspaceID
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Split(line, "\t")
		if !containsField(fields, needle) {
			continue
		}
		for _, f := range fields {
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
