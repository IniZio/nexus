package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/credsbundle"
	"github.com/inizio/nexus/packages/nexus/pkg/daemonclient"
	"github.com/inizio/nexus/packages/nexus/pkg/project"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
	"github.com/spf13/cobra"
)

type preflightErrorEnvelope struct {
	Status         string `json:"status"`
	SetupAttempted bool   `json:"setupAttempted"`
	SetupOutcome   string `json:"setupOutcome"`
	Checks         []struct {
		Name        string `json:"name"`
		OK          bool   `json:"ok"`
		Message     string `json:"message"`
		Remediation string `json:"remediation"`
		Installable bool   `json:"installable,omitempty"`
	} `json:"checks"`
}

func renderPreflightCreateError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	idx := strings.Index(msg, "runtime preflight failed:")
	if idx < 0 {
		return false
	}
	jsonStart := strings.Index(msg[idx:], "{")
	if jsonStart < 0 {
		return false
	}
	jsonPayload := strings.TrimSpace(msg[idx+jsonStart:])

	var payload preflightErrorEnvelope
	if unmarshalErr := json.Unmarshal([]byte(jsonPayload), &payload); unmarshalErr != nil {
		return false
	}

	fmt.Fprintln(os.Stderr, "nexus create: runtime preflight failed")
	fmt.Fprintf(os.Stderr, "status: %s\n", payload.Status)
	if payload.SetupAttempted {
		if strings.TrimSpace(payload.SetupOutcome) != "" {
			fmt.Fprintf(os.Stderr, "setup: attempted (%s)\n", payload.SetupOutcome)
		} else {
			fmt.Fprintln(os.Stderr, "setup: attempted")
		}
	}
	for _, check := range payload.Checks {
		if check.OK {
			continue
		}
		suffix := ""
		if check.Installable {
			suffix = " (installable)"
		}
		fmt.Fprintf(os.Stderr, "- %s%s", check.Name, suffix)
		if strings.TrimSpace(check.Message) != "" {
			fmt.Fprintf(os.Stderr, ": %s", check.Message)
		}
		fmt.Fprintln(os.Stderr)
		if strings.TrimSpace(check.Remediation) != "" {
			fmt.Fprintf(os.Stderr, "  remediation: %s\n", check.Remediation)
		}
	}
	return true
}

func daemonPort() int {
	return daemonclient.PreferredPort()
}

func daemonToken() (string, error) {
	if t := os.Getenv("NEXUS_DAEMON_TOKEN"); t != "" {
		return t, nil
	}
	return daemonclient.ReadDaemonToken()
}

func ensureDaemon() (*websocket.Conn, error) {
	port := daemonPort()
	tokenEnv := strings.TrimSpace(os.Getenv("NEXUS_DAEMON_TOKEN"))
	worktreeRoot, _ := daemonclient.ProcessWorktreeRoot(".")
	if err := daemonclient.EnsureRunningForWorktree(port, "", tokenEnv, worktreeRoot); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	token, err := daemonToken()
	if err != nil {
		return nil, fmt.Errorf("daemon token: %w", err)
	}

	url := fmt.Sprintf("ws://localhost:%d/", port)
	header := http.Header{"Authorization": {"Bearer " + token}}
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	return conn, nil
}

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      string      `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

type daemonRPCError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *daemonRPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

func daemonRPC(conn *websocket.Conn, method string, params interface{}, out interface{}) error {
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Method:  method,
		Params:  params,
	}
	if err := conn.WriteJSON(req); err != nil {
		return fmt.Errorf("rpc send: %w", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Minute)); err != nil {
		return fmt.Errorf("rpc set deadline: %w", err)
	}
	var resp rpcResponse
	if err := conn.ReadJSON(&resp); err != nil {
		return fmt.Errorf("rpc recv: %w", err)
	}
	conn.SetReadDeadline(time.Time{})
	if resp.Error != nil {
		return &daemonRPCError{Code: resp.Error.Code, Message: resp.Error.Message, Data: resp.Error.Data}
	}
	if out != nil && resp.Result != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

var ensureDaemonFn = ensureDaemon
var daemonRPCFn = daemonRPC
var waitForInterruptFn = waitForInterrupt

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspaces",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		listWorkspaces()
	},
}

var createBackend string
var createFresh bool
var createProjectID string
var createRepo string
var createFrom string
var listFlat bool

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new sandbox",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		createWorkspace(
			strings.TrimSpace(createBackend),
			strings.TrimSpace(createProjectID),
			strings.TrimSpace(createRepo),
			strings.TrimSpace(createFrom),
		)
	},
}

var startCmd = &cobra.Command{
	Use:   "start <id>",
	Short: "Start a stopped workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		startWorkspace(args[0])
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop <id>",
	Short: "Stop a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		stopWorkspace(args[0])
	},
}

var removeCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		removeWorkspace(args[0], removeDeleteHostPath, removeYes)
	},
}

var removeDeleteHostPath bool
var removeYes bool

var forkRef string
var forkSourceWorkspaceID string

var forkCmd = &cobra.Command{
	Use:   "fork <id> <name>",
	Short: "Fork a workspace into a new named worktree",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		forkWorkspace(strings.TrimSpace(args[0]), strings.TrimSpace(args[1]), strings.TrimSpace(forkRef), strings.TrimSpace(forkSourceWorkspaceID))
	},
}

var shellTimeout time.Duration

var shellCmd = &cobra.Command{
	Use:   "shell <id>",
	Short: "Open an interactive shell in a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		shellWorkspace(strings.TrimSpace(args[0]), shellTimeout)
	},
}

var execTimeout time.Duration

var execCmd = &cobra.Command{
	Use:   "exec <id> -- <command> [args...]",
	Short: "Run a non-interactive command in a workspace",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.ArgsLenAtDash() == -1 {
			return fmt.Errorf("usage: nexus exec <id> [--timeout <dur>] -- <command> [args...]")
		}
		id := strings.TrimSpace(args[0])
		rest := args[1:]
		if len(rest) == 0 {
			return fmt.Errorf("command required after --")
		}
		execWorkspace(id, execTimeout, rest)
		return nil
	},
}

var tunnelCmd = &cobra.Command{
	Use:   "tunnel <id>",
	Short: "Activate tunnels for a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		tunnelWorkspace(strings.TrimSpace(args[0]))
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore <id>",
	Short: "Restore a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		restoreWorkspace(args[0])
	},
}

var checkoutConflictMode string

var checkoutCmd = &cobra.Command{
	Use:   "checkout <id> <ref>",
	Short: "Switch a workspace to another ref/branch",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		checkoutWorkspace(strings.TrimSpace(args[0]), strings.TrimSpace(args[1]), strings.TrimSpace(checkoutConflictMode))
	},
}

var sandboxCmd = &cobra.Command{
	Use:   "sandbox",
	Short: "Manage sandboxes",
}

func init() {
	createCmd.Flags().StringVar(&createBackend, "backend", "", "runtime backend override (firecracker)")
	createCmd.Flags().BoolVar(&createFresh, "fresh", false, "skip source workspace snapshot reuse and create from fresh base")
	createCmd.Flags().StringVar(&createProjectID, "project", "", "target project id (required when creating outside current repo)")
	createCmd.Flags().StringVar(&createRepo, "repo", "", "repo/path for project creation when --project is not provided")
	createCmd.Flags().StringVar(&createFrom, "from", "auto", "source mode: auto|fresh|branch:<name>|workspace:<id>")
	forkCmd.Flags().StringVar(&forkRef, "ref", "", "child workspace git ref (defaults to child name)")
	forkCmd.Flags().StringVar(&forkSourceWorkspaceID, "source-workspace", "", "explicit source workspace id override (for nested forks)")
	shellCmd.Flags().DurationVar(&shellTimeout, "timeout", 0, "max wall time waiting for PTY output and exit (e.g. 90s); 0 = no limit")
	execCmd.Flags().DurationVar(&execTimeout, "timeout", 0, "max wall time for the command; 0 = no limit")
	listCmd.Flags().BoolVar(&listFlat, "flat", false, "show flat list instead of hierarchical")
	checkoutCmd.Flags().StringVar(&checkoutConflictMode, "on-conflict", "", "checkout conflict behavior: prompt|stash|discard|fail")
	removeCmd.Flags().BoolVar(&removeDeleteHostPath, "delete-host-path", false, "delete the host local worktree directory too (disabled for project root sandbox)")
	removeCmd.Flags().BoolVarP(&removeYes, "yes", "y", false, "skip confirmation prompt")
	sandboxCmd.AddCommand(
		listCmd,
		createCmd,
		startCmd,
		stopCmd,
		removeCmd,
		forkCmd,
		checkoutCmd,
		shellCmd,
		execCmd,
		tunnelCmd,
		restoreCmd,
	)
	rootCmd.AddCommand(sandboxCmd)
}

func listWorkspaces() {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus list: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if listFlat {
		listWorkspacesFlat(conn)
		return
	}
	listWorkspacesHierarchical(conn)
}

func listWorkspacesFlat(conn *websocket.Conn) {
	var result struct {
		Workspaces []workspacemgr.Workspace `json:"workspaces"`
	}
	if err := daemonRPC(conn, "workspace.list", map[string]any{}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus list: %v\n", err)
		os.Exit(1)
	}

	if len(result.Workspaces) == 0 {
		fmt.Println("no workspaces")
		return
	}
	fmt.Printf("%-36s  %-20s  %-10s  %-10s  %s\n", "ID", "NAME", "STATE", "BACKEND", "WORKTREE")
	fmt.Printf("%-36s  %-20s  %-10s  %-10s  %s\n",
		"------------------------------------", "--------------------",
		"----------", "----------", "--------")
	for _, ws := range result.Workspaces {
		wt := ws.LocalWorktreePath
		if wt == "" {
			wt = "—"
		}
		fmt.Printf("%-36s  %-20s  %-10s  %-10s  %s\n",
			ws.ID, ws.WorkspaceName, ws.State, ws.Backend, wt)
	}
}

func listWorkspacesHierarchical(conn *websocket.Conn) {
	var projectsResult struct {
		Projects []project.Project `json:"projects"`
	}
	if err := daemonRPC(conn, "project.list", map[string]any{}, &projectsResult); err != nil {
		fmt.Fprintf(os.Stderr, "nexus list: %v\n", err)
		os.Exit(1)
	}

	var workspacesResult struct {
		Workspaces []workspacemgr.Workspace `json:"workspaces"`
	}
	if err := daemonRPC(conn, "workspace.list", map[string]any{}, &workspacesResult); err != nil {
		fmt.Fprintf(os.Stderr, "nexus list: %v\n", err)
		os.Exit(1)
	}

	if len(projectsResult.Projects) == 0 {
		fmt.Println("no projects")
		return
	}

	workspacesByProject := make(map[string][]workspacemgr.Workspace)
	for _, ws := range workspacesResult.Workspaces {
		pid := ws.ProjectID
		if pid == "" {
			pid = "orphan"
		}
		workspacesByProject[pid] = append(workspacesByProject[pid], ws)
	}

	for _, p := range projectsResult.Projects {
		fmt.Printf("PROJECT: %s (%s)\n", p.Name, p.PrimaryRepo)
		workspaces := workspacesByProject[p.ID]
		if len(workspaces) == 0 {
			fmt.Println("  (no workspaces)")
			continue
		}
		for _, ws := range workspaces {
			displayRef := ws.CurrentRef
			if strings.TrimSpace(displayRef) == "" {
				displayRef = ws.Ref
			}
			name := ws.WorkspaceName
			if ws.ProjectID != "" && strings.TrimSpace(ws.ParentWorkspaceID) == "" {
				name = name + " (root)"
			}
			fmt.Printf("  %-20s  %-10s  %-10s  %s\n",
				name, ws.State, ws.Backend, displayRef)
		}
		fmt.Println()
	}

	if orphans, ok := workspacesByProject["orphan"]; ok && len(orphans) > 0 {
		fmt.Println("PROJECT: (legacy workspaces)")
		for _, ws := range orphans {
			displayRef := ws.CurrentRef
			if strings.TrimSpace(displayRef) == "" {
				displayRef = ws.Ref
			}
			fmt.Printf("  %-20s  %-10s  %-10s  %s\n",
				ws.WorkspaceName, ws.State, ws.Backend, displayRef)
		}
	}

	totalWs := len(workspacesResult.Workspaces)
	fmt.Printf("%d projects, %d workspaces total\n", len(projectsResult.Projects), totalWs)
}

func createWorkspace(backend string, projectID string, repoHint string, fromMode string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus sandbox create: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	project, resolvedRepo, err := resolveCreateProject(conn, projectID, repoHint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus sandbox create: %v\n", err)
		os.Exit(2)
	}
	workspaceName := deriveWorkspaceName(project.PrimaryRepo)

	sourceBranch, sourceWorkspaceID, fresh, parseErr := resolveCreateFromMode(fromMode, createFresh, resolvedRepo)
	if parseErr != nil {
		fmt.Fprintf(os.Stderr, "nexus sandbox create: %v\n", parseErr)
		os.Exit(2)
	}

	configBundle, err := credsbundle.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus sandbox create: %v\n", err)
		os.Exit(1)
	}

	spec := workspacemgr.CreateSpec{
		Repo:          project.PrimaryRepo,
		Ref:           "",
		WorkspaceName: workspaceName,
		AgentProfile:  "default",
		Backend:       strings.TrimSpace(backend),
		ConfigBundle:  configBundle,
	}
	var result struct {
		Workspace             workspacemgr.Workspace `json:"workspace"`
		EffectiveSourceBranch string                 `json:"effectiveSourceBranch"`
		SourceWorkspaceID     string                 `json:"sourceWorkspaceId"`
		UsedLineageSnapshotID string                 `json:"usedLineageSnapshotId"`
		FreshApplied          bool                   `json:"freshApplied"`
	}
	fmt.Println("Creating sandbox... (this may take a few minutes on first run)")
	createParams := map[string]any{
		"projectId":         project.ID,
		"targetBranch":      spec.Ref,
		"sourceBranch":      sourceBranch,
		"sourceWorkspaceId": sourceWorkspaceID,
		"fresh":             fresh,
		"workspaceName":     spec.WorkspaceName,
		"agentProfile":      spec.AgentProfile,
		"backend":           spec.Backend,
		"configBundle":      spec.ConfigBundle,
		"authBinding":       spec.AuthBinding,
		"policy":            spec.Policy,
		"repo":              spec.Repo,
	}
	if err := daemonRPC(conn, "workspace.create", createParams, &result); err != nil {
		if renderPreflightCreateError(err) {
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "nexus sandbox create: %v\n", err)
		os.Exit(1)
	}

	ws := result.Workspace
	fmt.Printf("✓ Created workspace %s (id: %s)\n", ws.WorkspaceName, ws.ID)
	if ws.ProjectID != "" && strings.TrimSpace(ws.ParentWorkspaceID) == "" {
		fmt.Println("role: project root sandbox")
	}
	if result.FreshApplied {
		fmt.Println("source: fresh workspace requested")
	} else if strings.TrimSpace(result.EffectiveSourceBranch) != "" {
		fmt.Printf("source branch: %s\n", result.EffectiveSourceBranch)
	}
	if strings.TrimSpace(result.SourceWorkspaceID) != "" {
		fmt.Printf("source workspace: %s\n", result.SourceWorkspaceID)
	}
	if strings.TrimSpace(result.UsedLineageSnapshotID) != "" {
		fmt.Printf("snapshot: %s\n", result.UsedLineageSnapshotID)
	}
	if localWorktreePath := createWorkspaceLocalWorktreePath(ws); localWorktreePath != "" {
		fmt.Printf("local worktree:   %s\n", localWorktreePath)
	}
}

func createWorkspaceLocalWorktreePath(ws workspacemgr.Workspace) string {
	return strings.TrimSpace(ws.LocalWorktreePath)
}

func resolveCreateProject(conn *websocket.Conn, projectID string, repoHint string) (project.Project, string, error) {
	if strings.TrimSpace(projectID) != "" {
		var getResult struct {
			Project project.Project `json:"project"`
		}
		if err := daemonRPC(conn, "project.get", map[string]any{"id": strings.TrimSpace(projectID)}, &getResult); err != nil {
			return project.Project{}, "", err
		}
		return getResult.Project, getResult.Project.PrimaryRepo, nil
	}

	repoPath := strings.TrimSpace(repoHint)
	if repoPath == "" {
		var err error
		repoPath, err = normalizeLocalRepoPath(".")
		if err != nil {
			return project.Project{}, "", err
		}
	}

	var createResult struct {
		Project project.Project `json:"project"`
	}
	if err := daemonRPC(conn, "project.create", map[string]any{"repo": repoPath}, &createResult); err != nil {
		return project.Project{}, "", err
	}
	return createResult.Project, repoPath, nil
}

func resolveCreateFromMode(fromMode string, freshFlag bool, repoPath string) (string, string, bool, error) {
	mode := strings.TrimSpace(fromMode)
	fresh := freshFlag
	sourceBranch := ""
	sourceWorkspaceID := ""
	if mode == "" {
		mode = "auto"
	}
	switch {
	case mode == "auto":
	case mode == "fresh":
		fresh = true
	case strings.HasPrefix(mode, "branch:"):
		sourceBranch = strings.TrimSpace(strings.TrimPrefix(mode, "branch:"))
		if sourceBranch == "" {
			return "", "", false, fmt.Errorf("--from branch:<name> requires a branch name")
		}
	case strings.HasPrefix(mode, "workspace:"):
		sourceWorkspaceID = strings.TrimSpace(strings.TrimPrefix(mode, "workspace:"))
		if sourceWorkspaceID == "" {
			return "", "", false, fmt.Errorf("--from workspace:<id> requires a workspace id")
		}
	default:
		return "", "", false, fmt.Errorf("invalid --from value %q (expected auto|fresh|branch:<name>|workspace:<id>)", mode)
	}

	if fresh && sourceBranch != "" {
		return "", "", false, fmt.Errorf("--fresh and --from branch:<name> are mutually exclusive")
	}
	if fresh && sourceWorkspaceID != "" {
		return "", "", false, fmt.Errorf("--fresh and --from workspace:<id> are mutually exclusive")
	}
	if !fresh && sourceBranch == "" && sourceWorkspaceID == "" {
		sourceBranch = currentLocalGitBranch(repoPath)
	}
	return sourceBranch, sourceWorkspaceID, fresh, nil
}

func currentLocalGitBranch(repoPath string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return ""
	}
	return branch
}

func checkoutWorkspace(id string, ref string, onConflict string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus checkout: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	params := map[string]any{
		"workspaceId": id,
		"targetRef":   ref,
	}
	if strings.TrimSpace(onConflict) != "" {
		params["onConflict"] = strings.TrimSpace(onConflict)
	}
	var result struct {
		CurrentRef    string `json:"currentRef"`
		CurrentCommit string `json:"currentCommit"`
	}
	err = daemonRPC(conn, "workspace.checkout", params, &result)
	if err != nil {
		if rpcErr, ok := err.(*daemonRPCError); ok && rpcErr.Code == -32011 && strings.TrimSpace(onConflict) == "" {
			selected := promptCheckoutConflictResolution()
			if selected == "cancel" {
				fmt.Fprintln(os.Stderr, "nexus checkout: cancelled")
				os.Exit(1)
			}
			params["onConflict"] = selected
			if retryErr := daemonRPC(conn, "workspace.checkout", params, &result); retryErr != nil {
				fmt.Fprintf(os.Stderr, "nexus checkout: %v\n", retryErr)
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "nexus checkout: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("✓ Workspace %s checked out to %s\n", id, result.CurrentRef)
	if strings.TrimSpace(result.CurrentCommit) != "" {
		fmt.Printf("commit: %s\n", result.CurrentCommit)
	}
}

func promptCheckoutConflictResolution() string {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("Local changes detected before checkout.")
		fmt.Println("Choose an action:")
		fmt.Println("  1) stash changes and switch")
		fmt.Println("  2) discard changes and switch")
		fmt.Println("  3) cancel")
		fmt.Print("Selection [1-3]: ")
		raw, _ := reader.ReadString('\n')
		switch strings.TrimSpace(raw) {
		case "1":
			return "stash"
		case "2":
			return "discard"
		case "3":
			return "cancel"
		}
		fmt.Println("Invalid selection.")
	}
}

func normalizeLocalRepoPath(pathValue string) (string, error) {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		pathValue = "."
	}
	absolutePath, err := filepath.Abs(pathValue)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", pathValue, err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", absolutePath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", absolutePath)
	}
	return absolutePath, nil
}

func deriveWorkspaceName(repoPath string) string {
	base := filepath.Base(filepath.Clean(repoPath))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "workspace"
	}
	base = strings.ToLower(base)
	var b strings.Builder
	lastDash := false
	for _, r := range base {
		isLetter := r >= 'a' && r <= 'z'
		isNumber := r >= '0' && r <= '9'
		if isLetter || isNumber {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "workspace"
	}
	return name
}

func stopWorkspace(id string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus stop: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := daemonRPC(conn, "workspace.stop", map[string]any{"id": id}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus stop: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("stopped workspace %s\n", id)
}

func startWorkspace(id string) {
	conn, err := ensureDaemonFn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus start: %v\n", err)
		os.Exit(1)
	}
	if conn != nil {
		defer conn.Close()
	}

	if err := daemonRPCFn(conn, "workspace.start", map[string]any{"id": id}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus start: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("started workspace %s\n", id)
}

func shellWorkspace(workspaceID string, ptyTimeout time.Duration) {
	token := strings.TrimSpace(os.Getenv("NEXUS_AUTH_RELAY_TOKEN"))
	runWorkspacePTYSession("nexus shell", workspaceID, token, "bash", "", ptyTimeout, true)
}

func execWorkspace(workspaceID string, ptyTimeout time.Duration, postDash []string) {
	cmdLine := formatCommand(postDash[0], postDash[1:])
	payload := "cd /workspace >/dev/null 2>&1 || true\n" + cmdLine + "\nexit\n"
	token := strings.TrimSpace(os.Getenv("NEXUS_AUTH_RELAY_TOKEN"))
	runWorkspacePTYSession("nexus exec", workspaceID, token, "bash", payload, ptyTimeout, false)
}

func runWorkspacePTYSession(label, workspaceID, relayToken, shell, commandPayload string, ptyTimeout time.Duration, interactiveStdin bool) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", label, err)
		os.Exit(1)
	}
	defer conn.Close()

	openParams := map[string]any{
		"workspaceId": workspaceID,
		"shell":       strings.TrimSpace(shell),
		"workdir":     "/workspace",
		"cols":        120,
		"rows":        40,
	}
	if relayToken != "" {
		openParams["authRelayToken"] = relayToken
	}

	openID := fmt.Sprintf("open-%d", time.Now().UnixNano())
	if err := conn.WriteJSON(rpcRequest{
		JSONRPC: "2.0",
		ID:      openID,
		Method:  "pty.open",
		Params:  openParams,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "%s: pty.open send failed: %v\n", label, err)
		os.Exit(1)
	}

	var sessionID string
	for {
		var msg rpcResponse
		if err := conn.ReadJSON(&msg); err != nil {
			fmt.Fprintf(os.Stderr, "%s: pty.open recv failed: %v\n", label, err)
			os.Exit(1)
		}
		if msg.ID != openID {
			continue
		}
		if msg.Error != nil {
			fmt.Fprintf(os.Stderr, "%s: pty.open rpc error %d: %s\n", label, msg.Error.Code, msg.Error.Message)
			os.Exit(1)
		}
		var open struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(msg.Result, &open); err != nil {
			fmt.Fprintf(os.Stderr, "%s: invalid pty.open result: %v\n", label, err)
			os.Exit(1)
		}
		sessionID = strings.TrimSpace(open.SessionID)
		break
	}

	var writeMu sync.Mutex
	send := func(method string, params map[string]any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(rpcRequest{
			JSONRPC: "2.0",
			Method:  method,
			Params:  params,
		})
	}

	if strings.TrimSpace(commandPayload) != "" {
		if err := send("pty.write", map[string]any{"sessionId": sessionID, "data": commandPayload}); err != nil {
			fmt.Fprintf(os.Stderr, "%s: command send failed: %v\n", label, err)
			os.Exit(1)
		}
	} else if interactiveStdin {
		go func() {
			reader := bufio.NewReader(os.Stdin)
			buf := make([]byte, 1024)
			for {
				n, readErr := reader.Read(buf)
				if n > 0 {
					_ = send("pty.write", map[string]any{
						"sessionId": sessionID,
						"data":      string(buf[:n]),
					})
				}
				if readErr != nil {
					if readErr == io.EOF {
						_ = send("pty.close", map[string]any{"sessionId": sessionID})
					}
					return
				}
			}
		}()
	}

	var sessionDeadline time.Time
	if ptyTimeout > 0 {
		sessionDeadline = time.Now().Add(ptyTimeout)
	}

	for {
		if !sessionDeadline.IsZero() && time.Now().After(sessionDeadline) {
			fmt.Fprintf(os.Stderr, "%s: timed out after %v (no pty.exit)\n", label, ptyTimeout)
			_ = send("pty.close", map[string]any{"sessionId": sessionID})
			os.Exit(124)
		}
		if !sessionDeadline.IsZero() {
			_ = conn.SetReadDeadline(sessionDeadline)
		} else {
			_ = conn.SetReadDeadline(time.Time{})
		}

		var msg rpcResponse
		if err := conn.ReadJSON(&msg); err != nil {
			var netErr net.Error
			if !sessionDeadline.IsZero() && errors.As(err, &netErr) && netErr.Timeout() {
				fmt.Fprintf(os.Stderr, "%s: timed out after %v\n", label, ptyTimeout)
				_ = send("pty.close", map[string]any{"sessionId": sessionID})
				os.Exit(124)
			}
			if !sessionDeadline.IsZero() && time.Now().After(sessionDeadline) {
				fmt.Fprintf(os.Stderr, "%s: timed out after %v\n", label, ptyTimeout)
				_ = send("pty.close", map[string]any{"sessionId": sessionID})
				os.Exit(124)
			}
			fmt.Fprintf(os.Stderr, "%s: read failed: %v\n", label, err)
			os.Exit(1)
		}

		if msg.Method == "" && (msg.ID != "" || msg.Result != nil) {
			continue
		}

		if msg.Method == "pty.data" {
			var p struct {
				SessionID string `json:"sessionId"`
				Data      string `json:"data"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.SessionID == sessionID && p.Data != "" {
				fmt.Print(p.Data)
			}
			continue
		}
		if msg.Method == "pty.exit" {
			var p struct {
				SessionID string `json:"sessionId"`
				ExitCode  int    `json:"exitCode"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.SessionID != sessionID {
				continue
			}
			if p.ExitCode != 0 {
				os.Exit(p.ExitCode)
			}
			return
		}
	}
}

func removeWorkspace(id string, deleteHostPath bool, yes bool) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus remove: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var opened struct {
		Workspace workspacemgr.Workspace `json:"workspace"`
	}
	if err := daemonRPC(conn, "workspace.open", map[string]any{"id": id}, &opened); err != nil {
		fmt.Fprintf(os.Stderr, "nexus remove: %v\n", err)
		os.Exit(1)
	}
	ws := opened.Workspace
	if !yes {
		if !confirmWorkspaceRemoval(ws, deleteHostPath) {
			fmt.Println("aborted")
			return
		}
	}

	if err := daemonRPC(conn, "workspace.remove", map[string]any{
		"id":             id,
		"deleteHostPath": deleteHostPath,
	}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus remove: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("removed workspace %s\n", id)
}

func confirmWorkspaceRemoval(ws workspacemgr.Workspace, deleteHostPath bool) bool {
	if strings.TrimSpace(ws.Backend) != "" {
		fmt.Fprintf(os.Stderr, "warning: remote runtime state for backend %q will be destroyed.\n", ws.Backend)
	}
	msg := fmt.Sprintf("Remove workspace %q (%s)? [y/N]: ", ws.WorkspaceName, ws.ID)
	if deleteHostPath {
		msg = fmt.Sprintf("Remove workspace %q (%s) and delete host path %q? [y/N]: ", ws.WorkspaceName, ws.ID, strings.TrimSpace(ws.LocalWorktreePath))
	}
	fmt.Fprint(os.Stderr, msg)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func forkWorkspace(id, childName, ref string, sourceWorkspaceID string) {
	if ref == "" {
		ref = childName
	}

	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus fork: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var result struct {
		Workspace workspacemgr.Workspace `json:"workspace"`
	}
	params := map[string]any{
		"id": id, "childWorkspaceName": childName, "childRef": ref,
	}
	if sourceWorkspaceID != "" {
		params["sourceWorkspaceId"] = sourceWorkspaceID
	}
	if err := daemonRPC(conn, "workspace.fork", params, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus fork: %v\n", err)
		os.Exit(1)
	}

	ws := result.Workspace
	fmt.Printf("forked workspace %s  (id: %s)\n", ws.WorkspaceName, ws.ID)

	if strings.TrimSpace(ws.LocalWorktreePath) != "" {
		fmt.Printf("local worktree:   %s\n", ws.LocalWorktreePath)
	}
}

func tunnelWorkspace(workspaceID string) {
	if workspaceID == "" {
		fmt.Fprintln(os.Stderr, "usage: nexus tunnel <workspace-id>")
		os.Exit(2)
	}
	conn, err := ensureDaemonFn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus tunnel: %v\n", err)
		os.Exit(1)
	}
	if conn != nil {
		defer conn.Close()
	}
	var result struct {
		Active            bool   `json:"active"`
		ActiveWorkspaceID string `json:"activeWorkspaceId"`
	}
	if err := daemonRPCFn(conn, "workspace.tunnels.activate", map[string]any{"workspaceId": workspaceID}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus tunnel: %v\n", err)
		os.Exit(1)
	}
	if !result.Active {
		if result.ActiveWorkspaceID != "" {
			fmt.Fprintf(os.Stderr, "nexus tunnel: another workspace already has active tunnels: %s\n", result.ActiveWorkspaceID)
		} else {
			fmt.Fprintln(os.Stderr, "nexus tunnel: failed to activate tunnels")
		}
		os.Exit(1)
	}
	fmt.Printf("tunnels active for workspace %s\n", workspaceID)
	fmt.Fprintln(os.Stdout, "press Ctrl-C to deactivate tunnels")
	waitForInterruptFn()
	if err := daemonRPCFn(conn, "workspace.tunnels.deactivate", map[string]any{"workspaceId": workspaceID}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus tunnel: deactivate warning: %v\n", err)
	} else {
		fmt.Printf("tunnels deactivated for workspace %s\n", workspaceID)
	}
}

func waitForInterrupt() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	signal.Stop(ch)
}

func restoreWorkspace(id string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus restore: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	var result struct {
		Workspace workspacemgr.Workspace `json:"workspace"`
	}
	if err := daemonRPC(conn, "workspace.restore", map[string]any{"id": id}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus restore: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("restored workspace %s  (id: %s)\n", result.Workspace.WorkspaceName, result.Workspace.ID)
}
