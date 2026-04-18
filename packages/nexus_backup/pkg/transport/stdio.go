package transport

import "errors"

// StdioTransport is a placeholder for stdio-based transport.
// Not yet implemented — requires bidirectional streaming support.
type StdioTransport struct{}

func (t *StdioTransport) Name() string { return "stdio" }

func (t *StdioTransport) Serve(_ Registry, _ *Deps) error {
	return errors.New("stdio transport not yet implemented")
}

func (t *StdioTransport) Close() error { return nil }
