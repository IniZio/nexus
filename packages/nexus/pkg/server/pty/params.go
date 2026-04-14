package pty

type OpenParams struct {
	WorkspaceID    string `json:"workspaceId,omitempty"`
	Shell          string `json:"shell,omitempty"`
	WorkDir        string `json:"workdir,omitempty"`
	Cols           int    `json:"cols,omitempty"`
	Rows           int    `json:"rows,omitempty"`
	AuthRelayToken string `json:"authRelayToken,omitempty"`
	Name           string `json:"name,omitempty"`    // Optional display name for the tab
	UseTmux        bool   `json:"useTmux,omitempty"` // Whether to use tmux for this session
}

type OpenResult struct {
	SessionID string `json:"sessionId"`
}

type WriteParams struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
}

type ResizeParams struct {
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type CloseParams struct {
	SessionID string `json:"sessionId"`
}

// AttachParams reattaches the current connection to an existing PTY session.
type AttachParams struct {
	SessionID string `json:"sessionId"`
}

type AttachResult struct {
	Attached bool `json:"attached"`
}

// ListParams requests a list of PTY sessions for a workspace
type ListParams struct {
	WorkspaceID string `json:"workspaceId"`
}

// ListResult contains the list of sessions for a workspace
type ListResult struct {
	Sessions []SessionInfo `json:"sessions"`
}

// GetParams requests info about a specific session
type GetParams struct {
	SessionID string `json:"sessionId"`
}

// GetResult contains session details
type GetResult struct {
	Session SessionInfo `json:"session"`
}

// RenameParams requests renaming a session
type RenameParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

// RenameResult confirms the rename operation
type RenameResult struct {
	Success bool `json:"success"`
}

// TmuxCommandParams sends a tmux command to a tmux-based session
type TmuxCommandParams struct {
	SessionID string   `json:"sessionId"`
	Command   string   `json:"command"` // e.g., "new-window", "select-window", "list-windows"
	Args      []string `json:"args,omitempty"`
}

// TmuxCommandResult returns tmux command output
type TmuxCommandResult struct {
	Success  bool   `json:"success"`
	Output   string `json:"output,omitempty"`
	ErrorMsg string `json:"error,omitempty"`
}
