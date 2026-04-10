package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/daemonclient"
	"github.com/inizio/nexus/packages/nexus/pkg/localws"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
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

	fmt.Fprintln(os.Stderr, "nexus workspace create: runtime preflight failed")
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

// ── Daemon connection settings ────────────────────────────────────────────────

const defaultDaemonPort = 7874

func daemonPort() int {
	if v := os.Getenv("NEXUS_DAEMON_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			return p
		}
	}
	return defaultDaemonPort
}

func daemonToken() (string, error) {
	if t := os.Getenv("NEXUS_DAEMON_TOKEN"); t != "" {
		return t, nil
	}
	return daemonclient.LoadOrCreateToken()
}

// ensureDaemon starts the daemon if it is not already running and returns
// an authenticated WebSocket connection to it.
func ensureDaemon() (*websocket.Conn, error) {
	port := daemonPort()
	token, err := daemonToken()
	if err != nil {
		return nil, fmt.Errorf("daemon token: %w", err)
	}

	if err := daemonclient.EnsureRunning(port, token, ""); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	url := fmt.Sprintf("ws://localhost:%d/?token=%s", port, token)
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	return conn, nil
}

// ── RPC helper ────────────────────────────────────────────────────────────────

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
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
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
	var resp rpcResponse
	if err := conn.ReadJSON(&resp); err != nil {
		return fmt.Errorf("rpc recv: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if out != nil && resp.Result != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

var ensureDaemonFn = ensureDaemon
var daemonRPCFn = daemonRPC

// ── workspace list ────────────────────────────────────────────────────────────

func runWorkspaceListCommand(_ []string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace list: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var result struct {
		Workspaces []workspacemgr.Workspace `json:"workspaces"`
	}
	if err := daemonRPC(conn, "workspace.list", map[string]any{}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace list: %v\n", err)
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

// ── workspace create ──────────────────────────────────────────────────────────

func runWorkspaceCreateCommand(args []string) {
	fs := flag.NewFlagSet("workspace create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	backend := fs.String("backend", "", "runtime backend override (firecracker)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if len(fs.Args()) > 0 {
		fmt.Fprintln(os.Stderr, "nexus workspace create: this command does not take positional arguments")
		fs.Usage()
		os.Exit(2)
	}

	repoPath, err := normalizeLocalRepoPath(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace create: %v\n", err)
		os.Exit(2)
	}
	workspaceName := deriveWorkspaceName(repoPath)

	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace create: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	spec := workspacemgr.CreateSpec{
		Repo:          repoPath,
		Ref:           "",
		WorkspaceName: workspaceName,
		AgentProfile:  "default",
		Backend:       strings.TrimSpace(*backend),
	}
	var result struct {
		Workspace workspacemgr.Workspace `json:"workspace"`
	}
	if err := daemonRPC(conn, "workspace.create", map[string]any{"spec": spec}, &result); err != nil {
		if renderPreflightCreateError(err) {
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "nexus workspace create: %v\n", err)
		os.Exit(1)
	}

	ws := result.Workspace
	fmt.Printf("created workspace %s  (id: %s)\n", ws.WorkspaceName, ws.ID)

	// ── Set up local worktree + optional mutagen sync ─────────────────────
	// A remote sandbox path is needed for the mutagen sync beta endpoint.
	// If RootPath is empty (the daemon hasn't assigned one yet) we still set
	// up the local worktree; we just skip the sync.
	lwMgr, lwErr := localws.NewManager(localws.Config{})
	if lwErr != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace create: warning: cannot init localws manager: %v\n", lwErr)
	} else {
		setupSpec := localws.SetupSpec{
			WorkspaceID:   ws.ID,
			WorkspaceName: ws.WorkspaceName,
			Repo:          ws.Repo,
			Ref:           ws.Ref,
			RemotePath:    ws.RootPath, // empty → mutagen skipped gracefully
		}
		setupResult, setupErr := lwMgr.Setup(context.Background(), setupSpec)
		if setupErr != nil {
			fmt.Fprintf(os.Stderr, "nexus workspace create: warning: local worktree setup failed: %v\n", setupErr)
		} else {
			// Persist worktree info back on the daemon record.
			setParams := map[string]any{
				"id":                ws.ID,
				"localWorktreePath": setupResult.WorktreePath,
				"mutagenSessionId":  setupResult.MutagenSessionID,
			}
			if rpcErr := daemonRPC(conn, "workspace.setLocalWorktree", setParams, nil); rpcErr != nil {
				fmt.Fprintf(os.Stderr, "nexus workspace create: warning: setLocalWorktree RPC failed: %v\n", rpcErr)
			}
			fmt.Printf("local worktree:   %s\n", setupResult.WorktreePath)
			if setupResult.MutagenSessionID != "" {
				fmt.Printf("mutagen session:  %s\n", setupResult.MutagenSessionID)
			}
		}
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

// ── workspace stop ────────────────────────────────────────────────────────────

func runWorkspaceStopCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus workspace stop <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace stop: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := daemonRPC(conn, "workspace.stop", map[string]any{"id": args[0]}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace stop: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("stopped workspace %s\n", args[0])
}

// ── workspace start ───────────────────────────────────────────────────────────

func runWorkspaceStartCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus workspace start <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemonFn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace start: %v\n", err)
		os.Exit(1)
	}
	if conn != nil {
		defer conn.Close()
	}

	if err := daemonRPCFn(conn, "workspace.start", map[string]any{"id": args[0]}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace start: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("started workspace %s\n", args[0])
}

func runWorkspaceSSHCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus workspace ssh <id> [--shell <shell>] [--command <cmd>]")
		os.Exit(2)
	}
	workspaceID := strings.TrimSpace(args[0])
	fs := flag.NewFlagSet("workspace ssh", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	shell := fs.String("shell", "bash", "shell to launch in workspace")
	command := fs.String("command", "", "command to run before exit")
	_ = fs.Parse(args[1:])

	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace ssh: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	openID := fmt.Sprintf("open-%d", time.Now().UnixNano())
	if err := conn.WriteJSON(rpcRequest{
		JSONRPC: "2.0",
		ID:      openID,
		Method:  "pty.open",
		Params: map[string]any{
			"workspaceId": workspaceID,
			"shell":       strings.TrimSpace(*shell),
			"workdir":     "/workspace",
			"cols":        120,
			"rows":        40,
		},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace ssh: pty.open send failed: %v\n", err)
		os.Exit(1)
	}

	var sessionID string
	for {
		var msg rpcResponse
		if err := conn.ReadJSON(&msg); err != nil {
			fmt.Fprintf(os.Stderr, "nexus workspace ssh: pty.open recv failed: %v\n", err)
			os.Exit(1)
		}
		if msg.ID != openID {
			continue
		}
		if msg.Error != nil {
			fmt.Fprintf(os.Stderr, "nexus workspace ssh: pty.open rpc error %d: %s\n", msg.Error.Code, msg.Error.Message)
			os.Exit(1)
		}
		var open struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(msg.Result, &open); err != nil {
			fmt.Fprintf(os.Stderr, "nexus workspace ssh: invalid pty.open result: %v\n", err)
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

	if strings.TrimSpace(*command) != "" {
		payload := *command + "\nexit\n"
		if err := send("pty.write", map[string]any{"sessionId": sessionID, "data": payload}); err != nil {
			fmt.Fprintf(os.Stderr, "nexus workspace ssh: command send failed: %v\n", err)
			os.Exit(1)
		}
	} else {
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

	for {
		var msg rpcResponse
		if err := conn.ReadJSON(&msg); err != nil {
			fmt.Fprintf(os.Stderr, "nexus workspace ssh: read failed: %v\n", err)
			os.Exit(1)
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

// ── workspace remove ──────────────────────────────────────────────────────────

func runWorkspaceRemoveCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus workspace remove <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace remove: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := daemonRPC(conn, "workspace.remove", map[string]any{"id": args[0]}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace remove: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("removed workspace %s\n", args[0])
}

// ── workspace fork ────────────────────────────────────────────────────────────

func runWorkspaceForkCommand(args []string) {
	fs := flag.NewFlagSet("workspace fork", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	id := fs.String("id", "", "source workspace ID (required)")
	childName := fs.String("name", "", "child workspace name (required)")
	childRef := fs.String("ref", "", "child workspace git ref (defaults to --name)")
	_ = fs.Parse(args)

	if *id == "" || *childName == "" {
		fmt.Fprintf(os.Stderr, "nexus workspace fork: --id and --name are required\n")
		fs.Usage()
		os.Exit(2)
	}
	ref := strings.TrimSpace(*childRef)
	if ref == "" {
		ref = strings.TrimSpace(*childName)
	}

	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace fork: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var result struct {
		Workspace workspacemgr.Workspace `json:"workspace"`
	}
	if err := daemonRPC(conn, "workspace.fork", map[string]any{
		"id": *id, "childWorkspaceName": *childName, "childRef": ref,
	}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace fork: %v\n", err)
		os.Exit(1)
	}

	ws := result.Workspace
	fmt.Printf("forked workspace %s  (id: %s)\n", ws.WorkspaceName, ws.ID)

	if strings.TrimSpace(ws.LocalWorktreePath) != "" {
		fmt.Printf("local worktree:   %s\n", ws.LocalWorktreePath)
	}
}

// ── workspace portal ─────────────────────────────────────────────────────────

func runWorkspacePortalCommand(_ []string) {
	port := daemonPort()
	token, err := daemonToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace portal: %v\n", err)
		os.Exit(1)
	}
	if err := daemonclient.EnsureRunning(port, token, ""); err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace portal: %v\n", err)
		os.Exit(1)
	}
	url := fmt.Sprintf("http://localhost:%d/portal", port)
	fmt.Printf("portal: %s\n", url)
	// Attempt to open in browser (best-effort).
	_ = openBrowser(url)
}

// ── top-level workspace dispatcher ───────────────────────────────────────────

// runWorkspaceCommand dispatches nexus workspace <sub> args.
func runWorkspaceCommand(args []string) {
	if len(args) == 0 {
		printWorkspaceUsage()
		os.Exit(2)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		runWorkspaceListCommand(rest)
	case "create":
		runWorkspaceCreateCommand(rest)
	case "start":
		runWorkspaceStartCommand(rest)
	case "stop":
		runWorkspaceStopCommand(rest)
	case "remove", "rm", "delete":
		runWorkspaceRemoveCommand(rest)
	case "fork":
		runWorkspaceForkCommand(rest)
	case "portal":
		runWorkspacePortalCommand(rest)
	case "ssh":
		runWorkspaceSSHCommand(rest)
	default:
		printWorkspaceUsage()
		fmt.Fprintf(os.Stderr, "\nunknown workspace subcommand: %s\n", sub)
		os.Exit(2)
	}
}

func printWorkspaceUsage() {
	fmt.Fprint(os.Stderr, `usage: nexus workspace <subcommand> [options]

subcommands:
  list                  list all workspaces
  create [--backend <backend>]
  start <id>            start a workspace and make it accessible
  ssh <id>              open interactive shell via daemon PTY
  stop <id>             stop a running workspace
  remove <id>           remove a workspace
  fork --id <id> --name <child-name> [--ref <child-ref>]
  portal                open the admin portal in your browser

`)
}

// openBrowser attempts to open url in the user's default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
	return cmd.Start()
}
