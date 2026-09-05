package cred

import (
	"context"
	"fmt"
	"time"
)

// CredentialSource is a host-internal interface that yields the current real
// bearer token for a credential. It is consumed by the host-side
// refresher/broker (P5-S1) and never exposed to the guest.
//
// Implementations must be safe for concurrent use from multiple goroutines.
//
// Token returns the current real token and its expiry. If no token is
// available (e.g. the backing store is absent or the token has been revoked),
// Token returns a non-nil error and callers must not use the returned string.
// An empty token with a nil error is a contract violation.
type CredentialSource interface {
	Token(ctx context.Context) (token string, expiresAt time.Time, err error)
}

// StaticCredentialSource is a [CredentialSource] that returns a fixed token
// loaded from a [DedicatedCredStore]. It does not refresh; it is the tracer
// proof that a store → source → token path works end-to-end. A refreshing
// implementation will be added in S1.
//
// A zero-value StaticCredentialSource is not usable; construct one via
// [NewStaticCredentialSource].
type StaticCredentialSource struct {
	store *DedicatedCredStore
}

// SourceTransform is a function that reads the credential file described by
// profile and returns a [CredentialSource] for it. It is the unit of
// registration in [credSourceRegistry].
type SourceTransform func(profile AgentProfile) (CredentialSource, error)

// credSourceRegistry maps each [CredentialFormat] to the transform that reads
// the corresponding credential file and returns a [CredentialSource].
//
// Adding support for a new file-based credential format requires:
//  1. A new [CredentialFormat] const in profile.go.
//  2. One entry here.
//
// [NewCredentialSourceForProfile] — the selector — is never edited for new agents.
var credSourceRegistry = map[CredentialFormat]SourceTransform{
	CredentialFormatCursorJWT: func(p AgentProfile) (CredentialSource, error) {
		return NewCursorCredentialSource(p)
	},
}

// NewCredentialSourceForProfile returns the credential source appropriate for
// profile by dispatching on profile.CredentialFormat via [credSourceRegistry].
//
// For OAuth/env-var agents (profile.CredentialFormat == [CredentialFormatNone])
// it returns (nil, nil) — those agents push credentials via a [Refresher].
//
// For file-based agents the registered [SourceTransform] is called. An
// unregistered format (programming error: a profile declared CredentialFormat
// but no entry was added to credSourceRegistry) returns a descriptive error.
//
// Adding a new file-based agent type requires a new [CredentialFormat] const
// in profile.go and one entry in [credSourceRegistry]. This function is not
// edited for new agents.
func NewCredentialSourceForProfile(profile AgentProfile) (CredentialSource, error) {
	if profile.CredentialFormat == CredentialFormatNone {
		return nil, nil
	}
	fn, ok := credSourceRegistry[profile.CredentialFormat]
	if !ok {
		return nil, fmt.Errorf("cred: NewCredentialSourceForProfile: no transform registered for format %q", profile.CredentialFormat)
	}
	return fn(profile)
}

// NewStaticCredentialSource wraps store in a [StaticCredentialSource].
// store must not be nil.
func NewStaticCredentialSource(store *DedicatedCredStore) *StaticCredentialSource {
	if store == nil {
		panic("cred: NewStaticCredentialSource called with nil store")
	}
	return &StaticCredentialSource{store: store}
}

// Token implements [CredentialSource]. It returns the access token and expiry
// recorded in the backing store. It never refreshes.
func (s *StaticCredentialSource) Token(_ context.Context) (string, time.Time, error) {
	return s.store.AccessToken, s.store.ExpiresAt, nil
}
