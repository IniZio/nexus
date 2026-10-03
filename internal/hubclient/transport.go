package hubclient

import (
	"context"
	"errors"
	"time"
)

// Journal field names of the CloudEvents-in-journald substrate.
const (
	CE_SPECVERSION     = "CE_SPECVERSION"
	CE_ID              = "CE_ID"
	CE_SOURCE          = "CE_SOURCE"
	CE_TYPE            = "CE_TYPE"
	CE_SUBJECT         = "CE_SUBJECT"
	CE_TIME            = "CE_TIME"
	CE_DATACONTENTTYPE = "CE_DATACONTENTTYPE"
	NEXUS_SANDBOX      = "NEXUS_SANDBOX"
	SYSLOG_IDENTIFIER  = "SYSLOG_IDENTIFIER"

	CESpecVersion    = "1.0"
	SyslogIdentifier = "nexus-hub"
)

// ErrUnsupported is returned by transports on platforms without a journal.
var ErrUnsupported = errors.New("hubclient: transport unsupported on this platform")

// Filter selects events by CE_SUBJECT (topics) and CE_TYPE. Empty matches all;
// multiple topics are ORed. Sandboxes additionally match guest-forwarded events
// by NEXUS_SANDBOX, whatever their own CE_SUBJECT.
type Filter struct {
	Topics    []string
	Types     []string
	Sandboxes []string
	// Since bounds a read with no cursor; zero means from now.
	Since time.Time
}

// Item is one read result: an Event, or a Gap when the cursor is no longer found.
type Item struct {
	Event Event
	Gap   bool
}

// Transport emits and reads hub events.
type Transport interface {
	Emit(ctx context.Context, ev Event) error
	// Read streams items after cursor ("" = from now) until ctx is cancelled.
	Read(ctx context.Context, f Filter, cursor string) (<-chan Item, error)
	// Last returns the newest event for a sandbox subject, or nil.
	Last(ctx context.Context, subject string) (*Event, error)
	LastAll(ctx context.Context) ([]Event, error)
}
