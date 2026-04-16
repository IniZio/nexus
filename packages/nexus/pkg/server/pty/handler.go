package pty

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	creackpty "github.com/creack/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/authrelay"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	runtimeprocess "github.com/inizio/nexus/packages/nexus/pkg/runtime/sandbox"
	"github.com/inizio/nexus/packages/nexus/pkg/safeenv"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type firecrackerAgentConnector interface {
	AgentConn(ctx context.Context, workspaceID string) (net.Conn, error)
}

type Deps struct {
	WorkspaceMgr   *workspacemgr.Manager
	RuntimeFactory *runtime.Factory
	AuthRelay      *authrelay.Broker
	RequireStarted func(workspaceID string) *rpckit.RPCError
	Registry       *Registry // Global PTY session registry for multi-tab support
	SessionStore   *Store
}

func shellQuoteUnixExport(val string) string {
	return "'" + strings.ReplaceAll(val, "'", "'\\''") + "'"
}

func buildRelayExportLines(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("export ")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(shellQuoteUnixExport(env[k]))
		b.WriteByte('\n')
	}
	return b.String()
}

func consumePTYAuthRelay(broker *authrelay.Broker, p OpenParams, workspaceID string) (map[string]string, *rpckit.RPCError) {
	tok := strings.TrimSpace(p.AuthRelayToken)
	if tok == "" {
		return nil, nil
	}
	if broker == nil {
		return nil, rpckit.ErrAuthRelayInvalid
	}
	injected, ok := broker.Consume(tok, workspaceID)
	if !ok {
		return nil, rpckit.ErrAuthRelayInvalid
	}
	return injected, nil
}

func forwardChunksUntilShellWriteAck(dec *json.Decoder, conn Conn, sessionID string) error {
	for {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			return err
		}
		typeStr, _ := msg["type"].(string)
		if typeStr == "ack" {
			return nil
		}
		if typeStr == "chunk" {
			if data, ok := msg["data"].(string); ok && data != "" {
				sendPTYData(conn, nil, sessionID, data)
			}
			continue
		}
		if typeStr == "result" {
			exitCode := 0
			if v, ok := msg["exit_code"].(float64); ok {
				exitCode = int(v)
			}
			return fmt.Errorf("unexpected shell result during auth relay inject (exit %d)", exitCode)
		}
		return fmt.Errorf("unexpected agent message type %q during auth relay inject", typeStr)
	}
}

func sendRemoteShellWrite(enc *json.Encoder, dec *json.Decoder, conn Conn, sessionID, data string) error {
	if strings.TrimSpace(data) == "" {
		return nil
	}
	writeID := fmt.Sprintf("write-%d", time.Now().UnixNano())
	writeReq := map[string]any{
		"id":   writeID,
		"type": "shell.write",
		"data": data,
	}
	if err := enc.Encode(writeReq); err != nil {
		return err
	}
	return forwardChunksUntilShellWriteAck(dec, conn, sessionID)
}

func sendRemoteShellWriteExpectMarker(
	enc *json.Encoder,
	dec *json.Decoder,
	conn Conn,
	sessionID string,
	data string,
	successMarker string,
	failureMarker string,
) error {
	if strings.TrimSpace(data) == "" {
		return nil
	}
	writeID := fmt.Sprintf("write-%d", time.Now().UnixNano())
	writeReq := map[string]any{
		"id":   writeID,
		"type": "shell.write",
		"data": data,
	}
	if err := enc.Encode(writeReq); err != nil {
		return err
	}
	foundMarker := false
	for {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			return err
		}
		typeStr, _ := msg["type"].(string)
		switch typeStr {
		case "chunk":
			chunk, _ := msg["data"].(string)
			if conn != nil && chunk != "" {
				sendPTYData(conn, nil, sessionID, chunk)
			}
			if successMarker != "" && strings.Contains(chunk, successMarker) {
				foundMarker = true
			}
			if failureMarker != "" && strings.Contains(chunk, failureMarker) {
				return fmt.Errorf("marker %q observed", failureMarker)
			}
		case "ack":
			if foundMarker {
				return nil
			}
			continue
		case "result":
			exitCode := 0
			if v, ok := msg["exit_code"].(float64); ok {
				exitCode = int(v)
			}
			return fmt.Errorf("shell closed while waiting for marker (exit %d)", exitCode)
		default:
			return fmt.Errorf("unexpected agent message type %q while waiting for marker", typeStr)
		}
	}
}

func buildTmuxSessionName(workspaceID, sessionID string) string {
	workspaceID = strings.ReplaceAll(strings.TrimSpace(workspaceID), "-", "_")
	sessionID = strings.ReplaceAll(strings.TrimSpace(sessionID), "-", "_")
	if workspaceID == "" {
		workspaceID = "workspace"
	}
	if sessionID == "" {
		sessionID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return "nexus_" + workspaceID + "_" + sessionID
}

func buildTmuxAttachCommand(tmuxSession, guestWorkdir string) string {
	quoted := shellQuoteUnixExport(tmuxSession)
	cwd := strings.TrimSpace(guestWorkdir)
	if cwd == "" {
		cwd = "/workspace"
	}
	cwdQ := shellQuoteUnixExport(cwd)
	return "if tmux has-session -t " + quoted + " >/dev/null 2>&1; then CUR=$(tmux display-message -p -t " + quoted + " '#{pane_current_path}' 2>/dev/null || true); if [ -n \"$CUR\" ] && [ ! -d \"$CUR\" ]; then tmux kill-session -t " + quoted + " >/dev/null 2>&1 || true; fi; fi; TERM=xterm-256color tmux new-session -A -c " + cwdQ + " -s " + quoted + " \\; set-option -t " + quoted + " status off\n"
}

func buildTmuxHealthCheckCommand(tmuxSession string) string {
	quoted := shellQuoteUnixExport(tmuxSession)
	return "if tmux has-session -t " + quoted + " >/dev/null 2>&1; then printf '__NEXUS_TMUX_OK__\\n'; else printf '__NEXUS_TMUX_MISSING__\\n'; fi\n"
}

func canonicalGuestWorkdir(driver runtime.Driver, workspaceID string) string {
	if provider, ok := driver.(runtime.GuestWorkdirProvider); ok {
		if workdir := strings.TrimSpace(provider.GuestWorkdir(workspaceID)); workdir != "" {
			return workdir
		}
	}
	return "/workspace"
}

func guestWorkdirForPTY(driver runtime.Driver, workspaceID, workDirHint string) string {
	w := strings.TrimSpace(workDirHint)
	if w == "" || w == "/workspace" {
		return canonicalGuestWorkdir(driver, workspaceID)
	}
	return w
}

func HandleOpen(deps *Deps, conn Conn, params json.RawMessage, ws *workspace.Workspace) (interface{}, *rpckit.RPCError) {
	var p OpenParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	rows := p.Rows
	if rows <= 0 {
		rows = 24
	}
	cols := p.Cols
	if cols <= 0 {
		cols = 80
	}

	shell := strings.TrimSpace(p.Shell)
	if shell == "" {
		shell = "bash"
	}
	workDirHint := strings.TrimSpace(p.WorkDir)
	if workDirHint == "" {
		workDirHint = "/workspace"
	}

	workspaceID := strings.TrimSpace(p.WorkspaceID)
	if workspaceID == "" {
		return nil, rpckit.ErrInvalidParams
	}
	if accessErr := deps.RequireStarted(workspaceID); accessErr != nil {
		return nil, accessErr
	}

	relayEnv, relayErr := consumePTYAuthRelay(deps.AuthRelay, p, workspaceID)
	if relayErr != nil {
		return nil, relayErr
	}

	wsRecord, ok := deps.WorkspaceMgr.Get(workspaceID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	log.Printf("[pty.open] workspace=%s name=%s backend=%s localWorktree=%s root=%s", wsRecord.ID, wsRecord.WorkspaceName, wsRecord.Backend, wsRecord.LocalWorktreePath, wsRecord.RootPath)
	if isGuestBackend(wsRecord.Backend) {
		return handleFirecrackerPTYOpen(deps, conn, p, wsRecord, relayEnv)
	}

	workDir := localWorkDirForOpen(wsRecord, ws)
	if workDir == "" {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "workspace root path unavailable"}
	}
	log.Printf("[pty.open] host backend: workDir=%s", workDir)

	cmd, err := localCommandForBackend(wsRecord, shell, workDir)
	if err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("pty command setup failed: %v", err)}
	}
	cmd.Dir = workDir
	cmdEnv := append(safeenv.Base(), "TERM=xterm-256color")
	if len(relayEnv) > 0 {
		for k, v := range relayEnv {
			cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
		}
	}
	cmd.Env = cmdEnv

	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("pty open failed: %v", err)}
	}

	sessionID := fmt.Sprintf("pty-%d", time.Now().UnixNano())

	// Determine session name (use provided name or generate default)
	sessionName := strings.TrimSpace(p.Name)
	if sessionName == "" {
		sessionName = fmt.Sprintf("Tab %d", deps.Registry.CountByWorkspace(workspaceID)+1)
	}

	session := &Session{
		ID:          sessionID,
		WorkspaceID: workspaceID,
		Name:        sessionName,
		Shell:       shell,
		WorkDir:     workDir,
		Cols:        cols,
		Rows:        rows,
		Cmd:         cmd,
		File:        ptmx,
		Done:        make(chan struct{}),
		CreatedAt:   time.Now(),
	}

	// Register in connection-local map for I/O handling
	conn.SetPTY(sessionID, session)

	// Register in global registry for workspace-scoped session management
	if deps.Registry != nil {
		deps.Registry.Register(session)
		deps.Registry.Subscribe(sessionID, conn)
	}

	go streamPTYOutput(conn, session, deps.Registry, deps.SessionStore)

	return &OpenResult{SessionID: sessionID}, nil
}

func localCommandForBackend(wsRecord *workspacemgr.Workspace, shell, workDir string) (*exec.Cmd, error) {
	if wsRecord != nil {
		backend := strings.TrimSpace(wsRecord.Backend)
		if strings.EqualFold(backend, "process") {
			repoRoot := strings.TrimSpace(wsRecord.Repo)
			return runtimeprocess.ShellCommand(shell, workDir, repoRoot)
		}
	}
	return exec.Command(shell), nil
}

// isGuestBackend reports whether the backend routes through a Lima/Firecracker
// guest VM rather than running as a local process on the daemon host.
func isGuestBackend(backend string) bool {
	switch strings.TrimSpace(backend) {
	case "firecracker", "lima":
		return true
	}
	return false
}

func handleFirecrackerPTYOpen(deps *Deps, conn Conn, p OpenParams, wsRecord *workspacemgr.Workspace, relayEnv map[string]string) (interface{}, *rpckit.RPCError) {
	if deps.RuntimeFactory == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "runtime factory unavailable"}
	}

	requestedBackend := strings.TrimSpace(wsRecord.Backend)
	backend := requestedBackend
	if backend == "" {
		backend = "firecracker"
	}
	if requestedBackend == "firecracker" || requestedBackend == "lima" {
		if driverAny, ok := deps.RuntimeFactory.DriverForBackend(requestedBackend); ok {
			reported := strings.TrimSpace(driverAny.Backend())
			log.Printf("[pty.open] %s driver type=%T reported-backend=%q", requestedBackend, driverAny, reported)
			if reported != "" {
				backend = reported
			}
		}
	}

	driverAny, ok := deps.RuntimeFactory.DriverForBackend(backend)
	if !ok {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("%s runtime driver not configured", backend)}
	}

	connector, ok := driverAny.(firecrackerAgentConnector)
	if !ok {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("%s runtime does not support agent connection", backend)}
	}

	openCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agentConn, err := connector.AgentConn(openCtx, wsRecord.ID)
	if err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("%s agent connect failed: %v", backend, err)}
	}

	shell := strings.TrimSpace(p.Shell)
	if shell == "" {
		shell = "bash"
	}
	workDirHint := strings.TrimSpace(p.WorkDir)
	if workDirHint == "" || workDirHint == "/workspace" {
		workDirHint = canonicalGuestWorkdir(driverAny, wsRecord.ID)
	}
	// Prefer tmux-backed shells for remote sessions so tabs can recover after daemon restarts.
	useTmux := true

	sessionID := fmt.Sprintf("pty-%d", time.Now().UnixNano())
	enc := json.NewEncoder(agentConn)
	dec := json.NewDecoder(agentConn)

	openReq := map[string]any{
		"id":      sessionID,
		"type":    "shell.open",
		"command": shell,
		"workdir": workDirHint,
	}
	if localPath := localWorkspacePathFromRecord(wsRecord); localPath != "" {
		openReq["local_path"] = localPath
	}

	if err := enc.Encode(openReq); err != nil {
		_ = agentConn.Close()
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("firecracker agent request failed: %v", err)}
	}
	var openResp map[string]any
	if err := dec.Decode(&openResp); err != nil {
		_ = agentConn.Close()
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("firecracker shell open failed: %v", err)}
	}
	if exitRaw, ok := openResp["exit_code"].(float64); ok && int(exitRaw) != 0 {
		_ = agentConn.Close()
		stderr, _ := openResp["stderr"].(string)
		if stderr == "" {
			stderr = "shell open failed"
		}
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: stderr}
	}

	if len(relayEnv) > 0 {
		data := buildRelayExportLines(relayEnv)
		if err := sendRemoteShellWrite(enc, dec, conn, sessionID, data); err != nil {
			_ = agentConn.Close()
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("auth relay inject failed: %v", err)}
		}
	}

	// Determine session name
	sessionName := strings.TrimSpace(p.Name)
	if sessionName == "" {
		sessionName = fmt.Sprintf("Tab %d", deps.Registry.CountByWorkspace(wsRecord.ID)+1)
	}

	session := &Session{
		ID:          sessionID,
		WorkspaceID: wsRecord.ID,
		Name:        sessionName,
		Shell:       shell,
		WorkDir:     workDirHint,
		Cols:        80, // Default, can be updated via resize
		Rows:        24,
		RemoteConn:  agentConn,
		Enc:         enc,
		Dec:         dec,
		Remote:      true,
		Done:        make(chan struct{}),
		CreatedAt:   time.Now(),
	}
	if useTmux {
		session.IsTmux = true
		session.TmuxSession = buildTmuxSessionName(wsRecord.ID, sessionID)
		if err := sendRemoteShellWrite(enc, dec, nil, sessionID, buildTmuxAttachCommand(session.TmuxSession, workDirHint)); err != nil {
			// Fall back to a plain shell when tmux is unavailable/misconfigured.
			// This keeps terminal startup usable on fresh machines where tmux has
			// not been installed yet.
			session.IsTmux = false
			session.TmuxSession = ""
			sendPTYData(conn, nil, sessionID, "\r\n[nexus] tmux unavailable; continuing without session persistence.\r\n")
		}
	}

	conn.SetPTY(sessionID, session)

	if deps.Registry != nil {
		deps.Registry.Register(session)
		deps.Registry.Subscribe(sessionID, conn)
	}
	_ = deps.SessionStore.Upsert(session.Info())

	go streamRemoteShellOutput(conn, session, deps.Registry, deps.SessionStore)

	return &OpenResult{SessionID: sessionID}, nil
}

func localWorkspacePathFromRecord(wsRecord *workspacemgr.Workspace) string {
	if wsRecord == nil {
		return ""
	}
	candidates := make([]string, 0, 3)
	candidates = append(candidates, strings.TrimSpace(wsRecord.HostWorkspacePath))
	candidates = append(candidates, strings.TrimSpace(wsRecord.LocalWorktreePath))
	if inferred := inferredWorkspaceWorktreePath(wsRecord); inferred != "" {
		candidates = append(candidates, inferred)
	}
	candidates = append(candidates, strings.TrimSpace(wsRecord.Repo))
	for _, candidate := range candidates {
		if canonical := canonicalWorkspaceCandidate(wsRecord, candidate); canonical != "" {
			return canonical
		}
	}
	return ""
}

func localWorkDirForOpen(wsRecord *workspacemgr.Workspace, ws *workspace.Workspace) string {
	if localPath := localWorkspacePathFromRecord(wsRecord); localPath != "" {
		return localPath
	}
	if ws != nil {
		if resolved := strings.TrimSpace(ws.Path()); resolved != "" {
			return resolved
		}
	}
	if wsRecord != nil {
		return strings.TrimSpace(wsRecord.RootPath)
	}
	return ""
}

func inferredWorkspaceWorktreePath(wsRecord *workspacemgr.Workspace) string {
	if wsRecord == nil {
		return ""
	}
	repoPath := canonicalExistingDir(strings.TrimSpace(wsRecord.Repo))
	if repoPath == "" {
		return ""
	}
	ref := strings.TrimSpace(wsRecord.CurrentRef)
	if ref == "" {
		ref = strings.TrimSpace(wsRecord.TargetBranch)
	}
	if ref == "" {
		ref = strings.TrimSpace(wsRecord.Ref)
	}
	return filepath.Join(repoPath, ".worktrees", workspacemgr.HostWorkspaceDirName(ref))
}

func canonicalExistingDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ""
	}
	resolved := filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(resolved); err == nil && strings.TrimSpace(real) != "" {
		resolved = filepath.Clean(real)
	}
	return resolved
}

func canonicalWorkspaceCandidate(wsRecord *workspacemgr.Workspace, candidate string) string {
	canonical := canonicalExistingDir(candidate)
	if canonical == "" {
		return ""
	}
	if wsRecord == nil {
		return canonical
	}
	if workspacemgr.IsManagedHostWorkspacePath(canonical) && !workspacemgr.HasValidHostWorkspaceMarker(canonical, wsRecord.ID) {
		return ""
	}
	return canonical
}

func streamRemoteShellOutput(conn Conn, session *Session, registry *Registry, store *Store) {
	defer close(session.Done)
	sentExit := false
	for {
		var msg map[string]any
		err := session.Dec.Decode(&msg)
		if err != nil {
			if !sentExit {
				sendPTYExit(conn, registry, session.ID, -1)
				sentExit = true
			}
			break
		}

		typeStr, _ := msg["type"].(string)
		if typeStr == "chunk" {
			if data, ok := msg["data"].(string); ok && data != "" {
				sendPTYData(conn, registry, session.ID, data)
			}
			continue
		}
		if typeStr == "result" {
			exitCode := 0
			if v, ok := msg["exit_code"].(float64); ok {
				exitCode = int(v)
			}
			sendPTYExit(conn, registry, session.ID, exitCode)
			sentExit = true
			break
		}
		continue
	}

	if conn != nil {
		conn.RemovePTY(session.ID)
	}
	if registry != nil {
		registry.Unregister(session.ID)
	}
	if session.Closing.Load() {
		_ = store.Delete(session.ID)
	}
	_ = session.RemoteConn.Close()
}

func sendPTYData(conn Conn, registry *Registry, sessionID, data string) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  "pty.data",
		"params": map[string]any{
			"sessionId": sessionID,
			"data":      data,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if registry != nil {
		registry.Broadcast(sessionID, encoded)
		return
	}
	if conn == nil {
		return
	}
	conn.Enqueue(encoded)
}

func sendPTYExit(conn Conn, registry *Registry, sessionID string, exitCode int) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  "pty.exit",
		"params": map[string]any{
			"sessionId": sessionID,
			"exitCode":  exitCode,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if registry != nil {
		registry.Broadcast(sessionID, encoded)
		return
	}
	if conn == nil {
		return
	}
	conn.Enqueue(encoded)
}

func lookupSession(deps *Deps, conn Conn, sessionID string) *Session {
	if sessionID == "" {
		return nil
	}
	if s := conn.GetPTY(sessionID); s != nil {
		return s
	}
	if deps != nil && deps.Registry != nil {
		return deps.Registry.Get(sessionID)
	}
	return nil
}

func HandleWrite(deps *Deps, params json.RawMessage, conn Conn) (interface{}, *rpckit.RPCError) {
	var p WriteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("invalid pty.write params: %v", err)}
	}

	session := lookupSession(deps, conn, p.SessionID)
	if session == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("pty session not found: %s", p.SessionID)}
	}
	if session.RemoteConn != nil {
		if p.Data == "" {
			return map[string]bool{"ok": true}, nil
		}

		session.Mu.Lock()
		defer session.Mu.Unlock()
		request := map[string]any{
			"id":   session.ID,
			"type": "shell.write",
			"data": p.Data,
		}
		if err := session.Enc.Encode(request); err != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("firecracker shell write failed: %v", err)}
		}
		return map[string]bool{"ok": true}, nil
	}

	if _, err := session.File.Write([]byte(p.Data)); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("pty write failed: %v", err)}
	}

	return map[string]bool{"ok": true}, nil
}

func HandleResize(deps *Deps, params json.RawMessage, conn Conn) (interface{}, *rpckit.RPCError) {
	var p ResizeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("invalid pty.resize params: %v", err)}
	}
	if p.Cols <= 0 || p.Rows <= 0 {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "invalid pty.resize params: cols/rows must be > 0"}
	}

	session := lookupSession(deps, conn, p.SessionID)
	if session == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("pty session not found: %s", p.SessionID)}
	}
	if session.RemoteConn != nil {
		session.Mu.Lock()
		_ = session.Enc.Encode(map[string]any{"id": session.ID, "type": "shell.resize", "cols": p.Cols, "rows": p.Rows})
		session.Mu.Unlock()
		return map[string]bool{"ok": true}, nil
	}

	if err := creackpty.Setsize(session.File, &creackpty.Winsize{Rows: uint16(p.Rows), Cols: uint16(p.Cols)}); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("pty resize failed: %v", err)}
	}

	return map[string]bool{"ok": true}, nil
}

func closeSession(session *Session, registry *Registry) {
	if session == nil {
		return
	}
	if session.RemoteConn != nil {
		session.Closing.Store(true)
		session.Mu.Lock()
		_ = session.Enc.Encode(map[string]any{"id": session.ID, "type": "shell.close"})
		session.Mu.Unlock()
		_ = session.RemoteConn.Close()
		if session.Done != nil {
			select {
			case <-session.Done:
			case <-time.After(2 * time.Second):
			}
		}
	} else {
		_ = session.File.Close()
		if session.Cmd.Process != nil {
			_ = session.Cmd.Process.Kill()
			_, _ = session.Cmd.Process.Wait()
		}
	}
	if registry != nil {
		registry.Unregister(session.ID)
	}
}

func HandleClose(deps *Deps, params json.RawMessage, conn Conn) (interface{}, *rpckit.RPCError) {
	var p CloseParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("invalid pty.close params: %v", err)}
	}

	if conn.ClosePTY(p.SessionID) {
		if deps != nil && deps.Registry != nil {
			deps.Registry.Unregister(p.SessionID)
		}
		if deps != nil && deps.SessionStore != nil {
			_ = deps.SessionStore.Delete(p.SessionID)
		}
		return map[string]bool{"closed": true}, nil
	}
	session := lookupSession(deps, conn, p.SessionID)
	closeSession(session, deps.Registry)
	if deps != nil && deps.SessionStore != nil {
		_ = deps.SessionStore.Delete(p.SessionID)
	}
	return map[string]bool{"closed": true}, nil
}

func streamPTYOutput(conn Conn, session *Session, registry *Registry, store *Store) {
	buf := make([]byte, 4096)
	for {
		n, err := session.File.Read(buf)
		if n > 0 {
			payload := map[string]any{
				"jsonrpc": "2.0",
				"method":  "pty.data",
				"params": map[string]any{
					"sessionId": session.ID,
					"data":      string(buf[:n]),
				},
			}
			encoded, marshalErr := json.Marshal(payload)
			if marshalErr == nil {
				if registry != nil {
					registry.Broadcast(session.ID, encoded)
				} else {
					conn.Enqueue(encoded)
				}
			}
		}

		if err != nil {
			break
		}
	}

	exitCode := -1
	if session.Cmd != nil && session.Cmd.Process != nil {
		if state, waitErr := session.Cmd.Process.Wait(); waitErr == nil && state != nil {
			exitCode = state.ExitCode()
		}
	}
	if exitCode == -1 && session.Cmd != nil && session.Cmd.ProcessState != nil {
		exitCode = session.Cmd.ProcessState.ExitCode()
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  "pty.exit",
		"params": map[string]any{
			"sessionId": session.ID,
			"exitCode":  exitCode,
		},
	}
	if encoded, marshalErr := json.Marshal(payload); marshalErr == nil {
		if registry != nil {
			registry.Broadcast(session.ID, encoded)
		} else if conn != nil {
			conn.Enqueue(encoded)
		}
	}
	if conn != nil {
		conn.RemovePTY(session.ID)
	}
	if registry != nil {
		registry.Unregister(session.ID)
	}
	if session.Closing.Load() && store != nil {
		_ = store.Delete(session.ID)
	}
}

func HandleAttach(deps *Deps, params json.RawMessage, conn Conn) (interface{}, *rpckit.RPCError) {
	var p AttachParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}
	sessionID := strings.TrimSpace(p.SessionID)
	if sessionID == "" {
		return nil, rpckit.ErrInvalidParams
	}
	if deps.Registry == nil {
		return &AttachResult{Attached: false}, nil
	}
	session := deps.Registry.Get(sessionID)
	if session == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "session not found"}
	}
	conn.SetPTY(sessionID, session)
	deps.Registry.Subscribe(sessionID, conn)
	return &AttachResult{Attached: true}, nil
}

// HandleList returns all PTY sessions for a workspace
func HandleList(deps *Deps, params json.RawMessage) (interface{}, *rpckit.RPCError) {
	var p ListParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	workspaceID := strings.TrimSpace(p.WorkspaceID)
	if workspaceID == "" {
		return nil, rpckit.ErrInvalidParams
	}

	if deps.Registry == nil {
		return &ListResult{Sessions: []SessionInfo{}}, nil
	}
	recoverPersistedSessionsForWorkspace(deps, workspaceID)

	sessions := deps.Registry.ListByWorkspace(workspaceID)
	return &ListResult{Sessions: sessions}, nil
}

// HandleGet returns info about a specific PTY session
func HandleGet(deps *Deps, params json.RawMessage) (interface{}, *rpckit.RPCError) {
	var p GetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	sessionID := strings.TrimSpace(p.SessionID)
	if sessionID == "" {
		return nil, rpckit.ErrInvalidParams
	}

	if deps.Registry == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "session not found"}
	}

	session := deps.Registry.Get(sessionID)
	if session == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "session not found"}
	}

	return &GetResult{Session: session.Info()}, nil
}

// HandleRename updates a session's display name
func HandleRename(deps *Deps, params json.RawMessage) (interface{}, *rpckit.RPCError) {
	var p RenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	sessionID := strings.TrimSpace(p.SessionID)
	newName := strings.TrimSpace(p.Name)
	if sessionID == "" || newName == "" {
		return nil, rpckit.ErrInvalidParams
	}

	if deps.Registry == nil {
		return &RenameResult{Success: false}, nil
	}

	success := deps.Registry.Rename(sessionID, newName)
	if success {
		if session := deps.Registry.Get(sessionID); session != nil {
			_ = deps.SessionStore.Upsert(session.Info())
		}
	}
	return &RenameResult{Success: success}, nil
}

func recoverPersistedSessionsForWorkspace(deps *Deps, workspaceID string) {
	if deps == nil || deps.SessionStore == nil || deps.Registry == nil {
		return
	}
	records, err := deps.SessionStore.List()
	if err != nil {
		return
	}
	for _, record := range records {
		if record.WorkspaceID != workspaceID || !record.IsTmux {
			continue
		}
		if deps.Registry.Get(record.ID) != nil {
			continue
		}
		alive, definitive := probePersistedTmuxSession(deps, record)
		if definitive && !alive {
			log.Printf("[pty] persisted tmux session %s missing; attempting rehydrate via attach", record.ID)
		}
		if err := recoverPersistedTmuxSession(deps, record); err != nil {
			_ = deps.SessionStore.Delete(record.ID)
		}
	}
}

func recoverPersistedTmuxSession(deps *Deps, info SessionInfo) error {
	if deps.RequireStarted != nil {
		if rpcErr := deps.RequireStarted(info.WorkspaceID); rpcErr != nil {
			return fmt.Errorf(rpcErr.Message)
		}
	}
	if deps.WorkspaceMgr == nil || deps.RuntimeFactory == nil {
		return fmt.Errorf("dependencies unavailable")
	}
	wsRecord, ok := deps.WorkspaceMgr.Get(info.WorkspaceID)
	if !ok {
		return fmt.Errorf("workspace not found")
	}
	backend := strings.TrimSpace(wsRecord.Backend)
	if backend == "" {
		backend = "firecracker"
	}
	driverAny, ok := deps.RuntimeFactory.DriverForBackend(backend)
	if !ok {
		return fmt.Errorf("backend %s unavailable", backend)
	}
	connector, ok := driverAny.(firecrackerAgentConnector)
	if !ok {
		return fmt.Errorf("backend %s has no agent connector", backend)
	}
	openCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	agentConn, err := connector.AgentConn(openCtx, wsRecord.ID)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(agentConn)
	dec := json.NewDecoder(agentConn)
	openReq := buildRecoveredShellOpenRequest(info, wsRecord)
	if err := enc.Encode(openReq); err != nil {
		_ = agentConn.Close()
		return err
	}
	var openResp map[string]any
	if err := dec.Decode(&openResp); err != nil {
		_ = agentConn.Close()
		return err
	}
	if exitRaw, ok := openResp["exit_code"].(float64); ok && int(exitRaw) != 0 {
		_ = agentConn.Close()
		return fmt.Errorf("shell open failed")
	}
	tmuxSession := strings.TrimSpace(info.TmuxSession)
	if tmuxSession == "" {
		tmuxSession = buildTmuxSessionName(info.WorkspaceID, info.ID)
	}
	guestCWD := guestWorkdirForPTY(driverAny, info.WorkspaceID, info.WorkDir)
	session := &Session{
		ID:          info.ID,
		WorkspaceID: info.WorkspaceID,
		Name:        info.Name,
		Shell:       "bash",
		WorkDir:     guestCWD,
		Cols:        info.Cols,
		Rows:        info.Rows,
		RemoteConn:  agentConn,
		Enc:         enc,
		Dec:         dec,
		Remote:      true,
		Done:        make(chan struct{}),
		CreatedAt:   info.CreatedAt,
		IsTmux:      true,
		TmuxSession: tmuxSession,
	}
	if err := sendRemoteShellWrite(enc, dec, nil, info.ID, buildTmuxAttachCommand(tmuxSession, guestCWD)); err != nil {
		_ = agentConn.Close()
		return err
	}
	deps.Registry.Register(session)
	_ = deps.SessionStore.Upsert(session.Info())
	go streamRemoteShellOutput(nil, session, deps.Registry, deps.SessionStore)
	return nil
}

// PruneStalePersistedSessions removes persisted tmux entries that are no longer valid.
func PruneStalePersistedSessions(deps *Deps) int {
	if deps == nil || deps.SessionStore == nil {
		return 0
	}
	entries, err := deps.SessionStore.List()
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsTmux {
			_ = deps.SessionStore.Delete(entry.ID)
			removed++
			continue
		}
		if deps.WorkspaceMgr == nil {
			_ = deps.SessionStore.Delete(entry.ID)
			removed++
			continue
		}
		ws, ok := deps.WorkspaceMgr.Get(entry.WorkspaceID)
		if !ok || ws.State != workspacemgr.StateRunning {
			_ = deps.SessionStore.Delete(entry.ID)
			removed++
			continue
		}
		if deps.Registry != nil && deps.Registry.Get(entry.ID) != nil {
			continue
		}
		alive, definitive := probePersistedTmuxSession(deps, entry)
		if definitive && !alive {
			_ = deps.SessionStore.Delete(entry.ID)
			removed++
		}
	}
	return removed
}

func buildRecoveredShellOpenRequest(info SessionInfo, wsRecord *workspacemgr.Workspace) map[string]any {
	openReq := map[string]any{
		"id":      info.ID,
		"type":    "shell.open",
		"command": "bash",
		"workdir": info.WorkDir,
	}
	if localPath := localWorkspacePathFromRecord(wsRecord); localPath != "" {
		openReq["local_path"] = localPath
	}
	return openReq
}

func probePersistedTmuxSession(deps *Deps, info SessionInfo) (bool, bool) {
	if deps == nil || deps.RuntimeFactory == nil || deps.WorkspaceMgr == nil {
		return false, false
	}
	ws, ok := deps.WorkspaceMgr.Get(info.WorkspaceID)
	if !ok {
		return false, true
	}
	backend := strings.TrimSpace(ws.Backend)
	if backend == "" {
		backend = "firecracker"
	}
	driverAny, ok := deps.RuntimeFactory.DriverForBackend(backend)
	if !ok {
		return false, false
	}
	connector, ok := driverAny.(firecrackerAgentConnector)
	if !ok {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	agentConn, err := connector.AgentConn(ctx, ws.ID)
	if err != nil {
		return false, false
	}
	defer agentConn.Close()

	enc := json.NewEncoder(agentConn)
	dec := json.NewDecoder(agentConn)
	openReq := map[string]any{
		"id":      info.ID,
		"type":    "shell.open",
		"command": "bash",
		"workdir": info.WorkDir,
	}
	if err := enc.Encode(openReq); err != nil {
		return false, false
	}
	var openResp map[string]any
	if err := dec.Decode(&openResp); err != nil {
		return false, false
	}
	if exitRaw, ok := openResp["exit_code"].(float64); ok && int(exitRaw) != 0 {
		return false, false
	}
	tmuxSession := strings.TrimSpace(info.TmuxSession)
	if tmuxSession == "" {
		tmuxSession = buildTmuxSessionName(info.WorkspaceID, info.ID)
	}
	if err := sendRemoteShellWriteExpectMarker(
		enc,
		dec,
		nil,
		info.ID,
		buildTmuxHealthCheckCommand(tmuxSession),
		"__NEXUS_TMUX_OK__",
		"__NEXUS_TMUX_MISSING__",
	); err != nil {
		if strings.Contains(err.Error(), "__NEXUS_TMUX_MISSING__") {
			return false, true
		}
		return false, false
	}
	_ = enc.Encode(map[string]any{"id": info.ID, "type": "shell.close"})
	return true, true
}

// HandleTmuxCommand executes a tmux command for tmux-based sessions
func HandleTmuxCommand(deps *Deps, conn Conn, params json.RawMessage) (interface{}, *rpckit.RPCError) {
	var p TmuxCommandParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	sessionID := strings.TrimSpace(p.SessionID)
	if sessionID == "" {
		return nil, rpckit.ErrInvalidParams
	}

	session := lookupSession(deps, conn, sessionID)
	if session == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "session not found"}
	}

	if !session.IsTmux {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "session is not a tmux session"}
	}

	// Build and execute tmux command against the logical session name.
	tmuxArgs := []string{p.Command}
	if session.TmuxSession != "" && p.Command != "list-sessions" && p.Command != "ls" {
		tmuxArgs = append(tmuxArgs, "-t", session.TmuxSession)
	}
	tmuxArgs = append(tmuxArgs, p.Args...)

	cmd := exec.Command("tmux", tmuxArgs...)
	output, err := cmd.CombinedOutput()

	result := &TmuxCommandResult{
		Success: err == nil,
		Output:  string(output),
	}
	if err != nil {
		result.ErrorMsg = err.Error()
	}

	return result, nil
}
