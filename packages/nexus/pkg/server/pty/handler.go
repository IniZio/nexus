package pty

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"

	creackpty "github.com/creack/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/authrelay"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
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

func forwardRelayChunksUntilShellWriteAck(dec *json.Decoder, conn Conn, sessionID string) error {
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
				sendPTYData(conn, sessionID, data)
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

	workDir := ws.Path()
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

	if wsRecord, ok := deps.WorkspaceMgr.Get(workspaceID); ok {
		log.Printf("[pty.open] workspace=%s name=%s backend=%s localWorktree=%s root=%s", wsRecord.ID, wsRecord.WorkspaceName, wsRecord.Backend, wsRecord.LocalWorktreePath, wsRecord.RootPath)
		if wsRecord.Backend == "firecracker" || wsRecord.Backend == "seatbelt" {
			return handleFirecrackerPTYOpen(deps, conn, p, wsRecord, relayEnv)
		}
		if strings.TrimSpace(wsRecord.LocalWorktreePath) != "" {
			workDir = wsRecord.LocalWorktreePath
		}
	}

	cmd := exec.Command(shell)
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
	session := &Session{ID: sessionID, Cmd: cmd, File: ptmx, Done: make(chan struct{})}

	conn.SetPTY(sessionID, session)

	go streamPTYOutput(conn, session)

	return &OpenResult{SessionID: sessionID}, nil
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
	if requestedBackend == "firecracker" || requestedBackend == "seatbelt" {
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
	if workDirHint == "" {
		workDirHint = "/workspace"
	}

	sessionID := fmt.Sprintf("pty-%d", time.Now().UnixNano())
	enc := json.NewEncoder(agentConn)
	dec := json.NewDecoder(agentConn)

	openReq := map[string]any{
		"id":      sessionID,
		"type":    "shell.open",
		"command": shell,
		"workdir": workDirHint,
	}
	if strings.TrimSpace(wsRecord.LocalWorktreePath) != "" {
		openReq["local_path"] = strings.TrimSpace(wsRecord.LocalWorktreePath)
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
		if data != "" {
			writeID := fmt.Sprintf("relay-%d", time.Now().UnixNano())
			writeReq := map[string]any{
				"id":   writeID,
				"type": "shell.write",
				"data": data,
			}
			if err := enc.Encode(writeReq); err != nil {
				_ = agentConn.Close()
				return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("auth relay shell write failed: %v", err)}
			}
			if err := forwardRelayChunksUntilShellWriteAck(dec, conn, sessionID); err != nil {
				_ = agentConn.Close()
				return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("auth relay inject failed: %v", err)}
			}
		}
	}

	session := &Session{ID: sessionID, RemoteConn: agentConn, Enc: enc, Dec: dec, Remote: true, Done: make(chan struct{})}

	conn.SetPTY(sessionID, session)

	go streamRemoteShellOutput(conn, session)

	return &OpenResult{SessionID: sessionID}, nil
}

func streamRemoteShellOutput(conn Conn, session *Session) {
	defer close(session.Done)
	sentExit := false
	for {
		var msg map[string]any
		err := session.Dec.Decode(&msg)
		if err != nil {
			if !sentExit {
				sendPTYExit(conn, session.ID, -1)
				sentExit = true
			}
			break
		}

		typeStr, _ := msg["type"].(string)
		if typeStr == "chunk" {
			if data, ok := msg["data"].(string); ok && data != "" {
				sendPTYData(conn, session.ID, data)
			}
			continue
		}
		if typeStr == "result" {
			exitCode := 0
			if v, ok := msg["exit_code"].(float64); ok {
				exitCode = int(v)
			}
			sendPTYExit(conn, session.ID, exitCode)
			sentExit = true
			break
		}
		continue
	}

	conn.RemovePTY(session.ID)
	_ = session.RemoteConn.Close()
}

func sendPTYData(conn Conn, sessionID, data string) {
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
	conn.Enqueue(encoded)
}

func sendPTYExit(conn Conn, sessionID string, exitCode int) {
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
	conn.Enqueue(encoded)
}

func HandleWrite(params json.RawMessage, conn Conn) (interface{}, *rpckit.RPCError) {
	var p WriteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("invalid pty.write params: %v", err)}
	}

	session := conn.GetPTY(p.SessionID)
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

func HandleResize(params json.RawMessage, conn Conn) (interface{}, *rpckit.RPCError) {
	var p ResizeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("invalid pty.resize params: %v", err)}
	}
	if p.Cols <= 0 || p.Rows <= 0 {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "invalid pty.resize params: cols/rows must be > 0"}
	}

	session := conn.GetPTY(p.SessionID)
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

func HandleClose(params json.RawMessage, conn Conn) (interface{}, *rpckit.RPCError) {
	var p CloseParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("invalid pty.close params: %v", err)}
	}

	if !conn.ClosePTY(p.SessionID) {
		return map[string]bool{"closed": true}, nil
	}

	return map[string]bool{"closed": true}, nil
}

func streamPTYOutput(conn Conn, session *Session) {
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
				conn.Enqueue(encoded)
			}
		}

		if err != nil {
			break
		}
	}

	conn.RemovePTY(session.ID)
	exitCode := -1
	if session.Cmd.Process != nil {
		_, _ = session.Cmd.Process.Wait()
	}
	if session.Cmd.ProcessState != nil {
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
		conn.Enqueue(encoded)
	}
}
