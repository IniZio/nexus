package vault

import (
	"context"
	"errors"
	"fmt"
)

// LinkFlow enumerates the OAuth link flows a connector may advertise.
type LinkFlow string

const (
	LinkFlowDevice LinkFlow = "device"
	LinkFlowPKCE   LinkFlow = "pkce"
)

// DeviceAuth holds the data returned by a device-flow StartDevice call.
type DeviceAuth struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       int
	Interval        int
}

// Connector describes an OAuth integration that the vault can link against.
// Device-flow connectors implement StartDevice and PollDevice; PKCE connectors
// implement AuthURL and Exchange. Both implement Refresh and the metadata methods.
type Connector interface {
	// ID returns the stable identifier for this integration (e.g. "github", "linear").
	ID() string

	// LinkFlow returns the flow this connector uses: "device" or "pkce".
	LinkFlow() LinkFlow

	// StartDevice initiates a device-flow authorization request.
	// Only called when LinkFlow() == LinkFlowDevice.
	StartDevice(ctx context.Context) (DeviceAuth, error)

	PollDevice(ctx context.Context, deviceCode string) (Record, error)

	AuthURL(state string) (url, codeVerifier string, err error)

	Exchange(ctx context.Context, code, codeVerifier string) (Record, error)

	Refresh(ctx context.Context, rec Record) (Record, error)

	AllowedHosts() []string
}

// Registry holds the set of registered connectors. Duplicate IDs are rejected.
type Registry struct {
	connectors map[string]Connector
}

// ErrDuplicateConnector is returned when a connector with the same ID is registered twice.
var ErrDuplicateConnector = errors.New("vault: connector ID already registered")

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{connectors: make(map[string]Connector)}
}

// Register adds c to the registry. It returns ErrDuplicateConnector if a
// connector with the same ID has already been registered.
func (r *Registry) Register(c Connector) error {
	id := c.ID()
	if _, exists := r.connectors[id]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateConnector, id)
	}
	r.connectors[id] = c
	return nil
}

// Get returns the connector with the given id, or an error if not found.
func (r *Registry) Get(id string) (Connector, error) {
	c, ok := r.connectors[id]
	if !ok {
		return nil, fmt.Errorf("vault: connector %q not registered", id)
	}
	return c, nil
}

// All returns all registered connectors.
func (r *Registry) All() []Connector {
	out := make([]Connector, 0, len(r.connectors))
	for _, c := range r.connectors {
		out = append(out, c)
	}
	return out
}
