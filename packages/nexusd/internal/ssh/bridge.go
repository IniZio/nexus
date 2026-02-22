package ssh

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/gorilla/websocket"
)

type SSHBridge struct {
	workspaceID   string
	listener       net.Listener
	socketPath     string
	wsConn         *websocket.Conn
	mu             sync.Mutex
	onActivity     func()
}

func NewBridge(workspaceID string) (*SSHBridge, error) {
	tmpDir := os.TempDir()
	socketDir := filepath.Join(tmpDir, "nexus-ssh-bridge")
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		return nil, fmt.Errorf("creating socket dir: %w", err)
	}

	socketPath := filepath.Join(socketDir, fmt.Sprintf("%s.sock", workspaceID))

	return &SSHBridge{
		workspaceID: workspaceID,
		socketPath:  socketPath,
	}, nil
}

func (b *SSHBridge) SetActivityCallback(callback func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onActivity = callback
}

func (b *SSHBridge) notifyActivity() {
	b.mu.Lock()
	callback := b.onActivity
	b.mu.Unlock()

	if callback != nil {
		callback()
	}
}

func (b *SSHBridge) Start() (string, error) {
	ln, err := net.Listen("unix", b.socketPath)
	if err != nil {
		return "", fmt.Errorf("listening on socket: %w", err)
	}

	if err := os.Chmod(b.socketPath, 0700); err != nil {
		ln.Close()
		return "", fmt.Errorf("chmod socket: %w", err)
	}

	b.listener = ln
	return b.socketPath, nil
}

func (b *SSHBridge) SetWebSocket(ws *websocket.Conn) {
	b.mu.Lock()
	b.wsConn = ws
	b.mu.Unlock()
}

func (b *SSHBridge) HandleConnections() {
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			if b.isClosed() {
				return
			}
			fmt.Printf("SSH bridge accept error: %v\n", err)
			continue
		}

		go b.handleConnection(conn)
	}
}

func (b *SSHBridge) handleConnection(agentConn net.Conn) {
	defer agentConn.Close()

	b.notifyActivity()

	b.mu.Lock()
	wsConn := b.wsConn
	b.mu.Unlock()

	if wsConn == nil {
		fmt.Printf("No WebSocket connection for workspace %s\n", b.workspaceID)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		ProxyAgentToWebSocket(agentConn, wsConn)
	}()

	go func() {
		defer wg.Done()
		ProxyWebSocketToAgent(wsConn, agentConn)
	}()

	wg.Wait()
}

func (b *SSHBridge) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.listener == nil
}

func (b *SSHBridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.listener != nil {
		b.listener.Close()
		b.listener = nil
	}

	if b.wsConn != nil {
		b.wsConn.Close()
		b.wsConn = nil
	}

	if b.socketPath != "" {
		os.Remove(b.socketPath)
		b.socketPath = ""
	}
}

func (b *SSHBridge) GetWebSocket() *websocket.Conn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wsConn
}

func (b *SSHBridge) GetSocketPath() string {
	return b.socketPath
}
