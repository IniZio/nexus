package pty

type OpenParams struct {
	WorkspaceID    string `json:"workspaceId,omitempty"`
	Shell          string `json:"shell,omitempty"`
	WorkDir        string `json:"workdir,omitempty"`
	Cols           int    `json:"cols,omitempty"`
	Rows           int    `json:"rows,omitempty"`
	AuthRelayToken string `json:"authRelayToken,omitempty"`
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
