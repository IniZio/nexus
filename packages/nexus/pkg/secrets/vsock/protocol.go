package vsock

import "time"

type Request struct {
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"`
}

type Response struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Error     string    `json:"error,omitempty"`
}
