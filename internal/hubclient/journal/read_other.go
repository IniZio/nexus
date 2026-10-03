//go:build !linux

package journal

import (
	"context"

	"github.com/IniZio/nexus/internal/hubclient"
)

// Reader reads hub events from the journal; unsupported off Linux.
type Reader struct{}

// NewReader returns a Reader.
func NewReader() *Reader { return &Reader{} }

func (*Reader) Read(context.Context, hubclient.Filter, string) (<-chan hubclient.Item, error) {
	return nil, hubclient.ErrUnsupported
}

func (*Reader) Last(context.Context, string) (*hubclient.Event, error) {
	return nil, hubclient.ErrUnsupported
}

func (*Reader) LastAll(context.Context) ([]hubclient.Event, error) {
	return nil, hubclient.ErrUnsupported
}
