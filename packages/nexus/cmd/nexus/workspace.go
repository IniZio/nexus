package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/daemonclient"
	"github.com/inizio/nexus/packages/nexus/pkg/localws"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

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
	fs := flag.NewFlagSet("workspace create", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", "", "repository URL (required)")
	ref := fs.String("ref", "", "branch / ref (default: repo default branch)")
	name := fs.String("name", "", "workspace name (required)")
	profile := fs.String("profile", "default", "agent profile")
	_ = fs.Parse(args)

	if *repo == "" || *name == "" {
		fmt.Fprintf(os.Stderr, "nexus workspace create: --repo and --name are required\n")
		fs.Usage()
		os.Exit(2)
	}

	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace create: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	spec := workspacemgr.CreateSpec{
		Repo:          *repo,
		Ref:           *ref,
		WorkspaceName: *name,
		AgentProfile:  *profile,
	}
	var result struct {
		Workspace workspacemgr.Workspace `json:"workspace"`
	}
	if err := daemonRPC(conn, "workspace.create", map[string]any{"spec": spec}, &result); err != nil {
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
	_ = fs.Parse(args)

	if *id == "" || *childName == "" {
		fmt.Fprintf(os.Stderr, "nexus workspace fork: --id and --name are required\n")
		fs.Usage()
		os.Exit(2)
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
		"id": *id, "childWorkspaceName": *childName,
	}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace fork: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("forked workspace %s  (id: %s)\n", result.Workspace.WorkspaceName, result.Workspace.ID)
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
	case "stop":
		runWorkspaceStopCommand(rest)
	case "remove", "rm", "delete":
		runWorkspaceRemoveCommand(rest)
	case "fork":
		runWorkspaceForkCommand(rest)
	case "portal":
		runWorkspacePortalCommand(rest)
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
  create --repo <url> --name <name> [--ref <ref>] [--profile <profile>]
  stop <id>             stop a running workspace
  remove <id>           remove a workspace
  fork --id <id> --name <child-name>
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
