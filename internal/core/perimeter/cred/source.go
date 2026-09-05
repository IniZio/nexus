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

// credSourceRegistry is the override registry for credential source transforms.
//
// Production file-based formats are registered in [preflightImportRegistry];
// [NewCredentialSourceForProfile] derives a [CredentialSource] from that
// registry automatically (wrapping the imported store in a
// [StaticCredentialSource]).  The two registries therefore cannot drift apart
// for production formats — a single [preflightImportRegistry] entry covers
// both the preflight path and the credential-source path.
//
// Register a format here only when its source transform cannot be expressed as
// ImportFn → [NewStaticCredentialSource] (e.g. a test whose closure captures a
// temp-dir path instead of resolving it via the profile).  Tests that register
// here must clean up with t.Cleanup(func() { delete(credSourceRegistry, fmt) }).
var credSourceRegistry = map[CredentialFormat]SourceTransform{}

// NewCredentialSourceForProfile returns the credential source appropriate for
// profile.
//
// For OAuth/env-var agents (profile.CredentialFormat == [CredentialFormatNone])
// it returns (nil, nil) — those agents push credentials via a [Refresher].
//
// For file-based agents it first checks the override [credSourceRegistry].  If
// no override is registered it derives the source from [preflightImportRegistry]
// by calling the import function and wrapping the result in a
// [StaticCredentialSource].  An unregistered format (programming error: a
// profile declared a CredentialFormat but no entry was added to
// preflightImportRegistry) returns a descriptive error.
//
// Adding a new file-based agent type requires a new [CredentialFormat] const in
// profile.go and one entry in [preflightImportRegistry] only.  This function is
// never edited for new agents.
func NewCredentialSourceForProfile(profile AgentProfile) (CredentialSource, error) {
	if profile.CredentialFormat == CredentialFormatNone {
		return nil, nil
	}
	// Check the override registry first — for test-time registrations whose
	// closure captures a temp-dir path instead of resolving from the profile.
	if fn, ok := credSourceRegistry[profile.CredentialFormat]; ok {
		return fn(profile)
	}
	// Derive source from the import registry: import the credential and wrap it.
	store, err := ImportCred(profile)
	if err != nil {
		return nil, fmt.Errorf("cred: NewCredentialSourceForProfile: %w", err)
	}
	return NewStaticCredentialSource(store), nil
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
