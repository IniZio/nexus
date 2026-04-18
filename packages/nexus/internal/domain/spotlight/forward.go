package spotlight

import "time"

// ForwardState represents the state of a port forward.
type ForwardState string

const (
	ForwardStateActive   ForwardState = "active"
	ForwardStateInactive ForwardState = "inactive"
)

// ForwardSource identifies what initiated the forward.
type ForwardSource string

const (
	ForwardSourceUser ForwardSource = "user"
	ForwardSourceAuto ForwardSource = "auto"
)

// ExposeSpec describes a request to expose a port.
type ExposeSpec struct {
	LocalPort   int           `json:"localPort"`
	RemotePort  int           `json:"remotePort"`
	Protocol    string        `json:"protocol,omitempty"`
	Source      ForwardSource `json:"source,omitempty"`
	WorkspaceID string        `json:"workspaceId"`
}

// Forward is the domain entity for an active or inactive port forward.
type Forward struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspaceId"`
	LocalPort   int          `json:"localPort"`
	RemotePort  int          `json:"remotePort"`
	Protocol    string       `json:"protocol,omitempty"`
	State       ForwardState `json:"state"`
	CreatedAt   time.Time    `json:"created_at,omitempty"`
}
