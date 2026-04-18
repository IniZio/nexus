package pty

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// SessionInfo is the serializable metadata for a PTY session.
type SessionInfo struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Name        string    `json:"name"`
	Shell       string    `json:"shell"`
	WorkDir     string    `json:"workDir"`
	Cols        int       `json:"cols"`
	Rows        int       `json:"rows"`
	CreatedAt   time.Time `json:"createdAt"`
	IsRemote    bool      `json:"isRemote"`
}

// Session represents an active PTY or remote shell session.
type Session struct {
	ID          string
	WorkspaceID string
	Name        string
	Shell       string
	WorkDir     string
	Cols        int
	Rows        int

	// Local PTY (process backend)
	Cmd  *exec.Cmd
	File *os.File

	// Remote shell (firecracker agent)
	RemoteConn net.Conn
	Enc        *json.Encoder
	Dec        *json.Decoder
	Remote     bool

	Mu      sync.Mutex
	Closing atomic.Bool
	Done    chan struct{}

	CreatedAt time.Time
}

// Info returns a serializable snapshot of session metadata.
func (s *Session) Info() SessionInfo {
	return SessionInfo{
		ID:          s.ID,
		WorkspaceID: s.WorkspaceID,
		Name:        s.Name,
		Shell:       s.Shell,
		WorkDir:     s.WorkDir,
		Cols:        s.Cols,
		Rows:        s.Rows,
		CreatedAt:   s.CreatedAt,
		IsRemote:    s.Remote,
	}
}
