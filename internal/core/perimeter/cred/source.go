package cred

import (
	"context"
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

// NewCredentialSourceForProfile returns the credential source appropriate for
// profile. For file-based agents (profile.CredentialFile != ""), it reads the
// credential file (e.g. cursor's auth.json) and returns a [StaticCredentialSource].
// For OAuth/env-var agents (profile.CredentialFile == ""), it returns nil, nil
// — those agents use a [Refresher] built separately from the OAuth credential store.
//
// Callers use this to get a credential source for any profile without branching
// on the agent type. A third agent that declares CredentialFile in its
// AgentProfile requires no code change here.
func NewCredentialSourceForProfile(profile AgentProfile) (CredentialSource, error) {
	if profile.CredentialFile == "" {
		return nil, nil
	}
	return NewCursorCredentialSource(profile)
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
