package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/daemonclient"
	"github.com/inizio/nexus/packages/nexus/pkg/localws"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/authbundle"
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
var waitForInterruptFn = waitForInterrupt

// ── workspace list ────────────────────────────────────────────────────────────

func runWorkspaceListCommand(_ []string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus list: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

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

// ── workspace create ──────────────────────────────────────────────────────────

func runWorkspaceCreateCommand(args []string) {
	fs := flag.NewFlagSet("workspace create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	backend := fs.String("backend", "", "runtime backend override (firecracker)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if len(fs.Args()) > 0 {
		fmt.Fprintln(os.Stderr, "nexus create: this command does not take positional arguments")
		fs.Usage()
		os.Exit(2)
	}

	repoPath, err := normalizeLocalRepoPath(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus create: %v\n", err)
		os.Exit(2)
	}
	workspaceName := deriveWorkspaceName(repoPath)

	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus create: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	hostAuthBundle, err := authbundle.BuildFromHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus create: %v\n", err)
		os.Exit(1)
	}

	spec := workspacemgr.CreateSpec{
		Repo:          repoPath,
		Ref:           "",
		WorkspaceName: workspaceName,
		AgentProfile:  "default",
		Backend:       strings.TrimSpace(*backend),
		ConfigBundle:  hostAuthBundle,
	}
	var result struct {
		Workspace workspacemgr.Workspace `json:"workspace"`
	}
	if err := daemonRPC(conn, "workspace.create", map[string]any{"spec": spec}, &result); err != nil {
		if renderPreflightCreateError(err) {
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "nexus create: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "nexus create: warning: cannot init localws manager: %v\n", lwErr)
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
			fmt.Fprintf(os.Stderr, "nexus create: warning: local worktree setup failed: %v\n", setupErr)
		} else {
			// Persist worktree info back on the daemon record.
			setParams := map[string]any{
				"id":                ws.ID,
				"localWorktreePath": setupResult.WorktreePath,
				"mutagenSessionId":  setupResult.MutagenSessionID,
			}
			if rpcErr := daemonRPC(conn, "workspace.setLocalWorktree", setParams, nil); rpcErr != nil {
				fmt.Fprintf(os.Stderr, "nexus create: warning: setLocalWorktree RPC failed: %v\n", rpcErr)
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
		fmt.Fprintln(os.Stderr, "usage: nexus stop <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus stop: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := daemonRPC(conn, "workspace.stop", map[string]any{"id": args[0]}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus stop: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("stopped workspace %s\n", args[0])
}

// ── workspace start ───────────────────────────────────────────────────────────

func runWorkspaceStartCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus start <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemonFn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus start: %v\n", err)
		os.Exit(1)
	}
	if conn != nil {
		defer conn.Close()
	}

	if err := daemonRPCFn(conn, "workspace.start", map[string]any{"id": args[0]}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus start: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("started workspace %s\n", args[0])
}

func runWorkspaceShellCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus shell <id> [--timeout <dur>]")
		os.Exit(2)
	}
	workspaceID := strings.TrimSpace(args[0])
	fs := flag.NewFlagSet("workspace shell", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	ptyTimeout := fs.Duration("timeout", 0, "max wall time waiting for PTY output and exit (e.g. 90s); 0 = no limit")
	if err := fs.Parse(args[1:]); err != nil {
		os.Exit(2)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus shell <id> [--timeout <dur>]")
		os.Exit(2)
	}
	token := strings.TrimSpace(os.Getenv("NEXUS_AUTH_RELAY_TOKEN"))
	runWorkspacePTYSession("nexus shell", workspaceID, token, "bash", "", *ptyTimeout, true)
}

func runWorkspaceExecCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus exec <id> [--timeout <dur>] -- <command> [args...]")
		os.Exit(2)
	}
	workspaceID := strings.TrimSpace(args[0])
	rest := args[1:]
	dash := -1
	for i, a := range rest {
		if a == "--" {
			dash = i
			break
		}
	}
	if dash == -1 {
		fmt.Fprintln(os.Stderr, "usage: nexus exec <id> [--timeout <dur>] -- <command> [args...]")
		os.Exit(2)
	}
	preDash := rest[:dash]
	postDash := rest[dash+1:]
	if len(postDash) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus exec <id> [--timeout <dur>] -- <command> [args...]")
		os.Exit(2)
	}
	ptyTimeout := time.Duration(0)
	for i := 0; i < len(preDash); {
		if preDash[i] == "--timeout" && i+1 < len(preDash) {
			d, err := time.ParseDuration(preDash[i+1])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			ptyTimeout = d
			i += 2
			continue
		}
		fmt.Fprintln(os.Stderr, "usage: nexus exec <id> [--timeout <dur>] -- <command> [args...]")
		os.Exit(2)
	}
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

// ── workspace remove ──────────────────────────────────────────────────────────

func runWorkspaceRemoveCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus remove <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus remove: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := daemonRPC(conn, "workspace.remove", map[string]any{"id": args[0]}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus remove: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("removed workspace %s\n", args[0])
}

// ── workspace fork ────────────────────────────────────────────────────────────

func runWorkspaceForkCommand(args []string) {
	fs := flag.NewFlagSet("workspace fork", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	childRef := fs.String("ref", "", "child workspace git ref (defaults to child name)")
	_ = fs.Parse(args)

	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "usage: nexus fork <id> <name> [--ref <ref>]\n")
		os.Exit(2)
	}
	id := strings.TrimSpace(fs.Arg(0))
	childName := strings.TrimSpace(fs.Arg(1))
	ref := strings.TrimSpace(*childRef)
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
	if err := daemonRPC(conn, "workspace.fork", map[string]any{
		"id": id, "childWorkspaceName": childName, "childRef": ref,
	}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus fork: %v\n", err)
		os.Exit(1)
	}

	ws := result.Workspace
	fmt.Printf("forked workspace %s  (id: %s)\n", ws.WorkspaceName, ws.ID)

	if strings.TrimSpace(ws.LocalWorktreePath) != "" {
		fmt.Printf("local worktree:   %s\n", ws.LocalWorktreePath)
	}
}

func runWorkspaceTunnelCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus tunnel <workspace-id>")
		os.Exit(2)
	}
	workspaceID := strings.TrimSpace(args[0])
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
		Forwards []struct {
			ID         string `json:"id"`
			Service    string `json:"service"`
			Host       string `json:"host"`
			LocalPort  int    `json:"localPort"`
			RemotePort int    `json:"remotePort"`
		} `json:"forwards"`
		Errors []struct {
			Service    string `json:"service"`
			HostPort   int    `json:"hostPort"`
			TargetPort int    `json:"targetPort"`
			Message    string `json:"message"`
		} `json:"errors"`
	}
	if err := daemonRPCFn(conn, "spotlight.applyComposePorts", map[string]any{"workspaceId": workspaceID}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus tunnel: %v\n", err)
		os.Exit(1)
	}
	if len(result.Forwards) == 0 {
		fmt.Printf("no compose ports spotlighted for workspace %s\n", workspaceID)
		return
	}
	for _, fwd := range result.Forwards {
		host := strings.TrimSpace(fwd.Host)
		if host == "" {
			host = "127.0.0.1"
		}
		fmt.Printf("tunnel active %s %s:%d -> %d (%s)\n", fwd.Service, host, fwd.LocalPort, fwd.RemotePort, fwd.ID)
	}
	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "spotlight error %s %d->%d: %s\n", e.Service, e.HostPort, e.TargetPort, e.Message)
		}
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "press Ctrl-C to close tunnels")
	waitForInterruptFn()
	for _, fwd := range result.Forwards {
		if err := daemonRPCFn(conn, "spotlight.close", map[string]any{"id": fwd.ID}, nil); err != nil {
			fmt.Fprintf(os.Stderr, "nexus tunnel: close warning for %s: %v\n", fwd.ID, err)
		} else {
			fmt.Printf("closed tunnel %s\n", fwd.ID)
		}
	}
}

func waitForInterrupt() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	signal.Stop(ch)
}

func runWorkspacePauseCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus pause <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus pause: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := daemonRPC(conn, "workspace.pause", map[string]any{"id": args[0]}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus pause: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("paused workspace %s\n", args[0])
}

func runWorkspaceResumeCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus resume <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus resume: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := daemonRPC(conn, "workspace.resume", map[string]any{"id": args[0]}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus resume: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("resumed workspace %s\n", args[0])
}

func runWorkspaceRestoreCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus restore <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus restore: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	var result struct {
		Workspace workspacemgr.Workspace `json:"workspace"`
	}
	if err := daemonRPC(conn, "workspace.restore", map[string]any{"id": args[0]}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus restore: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("restored workspace %s  (id: %s)\n", result.Workspace.WorkspaceName, result.Workspace.ID)
}
