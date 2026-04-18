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

// SessionInfo contains metadata about a PTY session for listing/attaching
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
	IsTmux      bool      `json:"isTmux"`
	TmuxSession string    `json:"tmuxSession,omitempty"`
}

type Session struct {
	ID          string
	WorkspaceID string
	Name        string
	Shell       string
	WorkDir     string
	Cols        int
	Rows        int
	Cmd         *exec.Cmd
	File        *os.File
	RemoteConn  net.Conn
	Mu          sync.Mutex
	Closing     atomic.Bool
	Enc         *json.Encoder
	Dec         *json.Decoder
	Remote      bool
	Done        chan struct{}
	CreatedAt   time.Time
	// Tmux support
	IsTmux      bool
	TmuxSession string // tmux session name if using tmux
}

// Info returns a serializable snapshot of session metadata
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
		IsTmux:      s.IsTmux,
		TmuxSession: s.TmuxSession,
	}
}
