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

// SourceTransform is a function that reads the credential described by profile
// and returns a [CredentialSource] for it. It is the type of
// [AgentRegistration.SourceFn].
type SourceTransform func(profile AgentProfile) (CredentialSource, error)

// AgentRegistration is the unit of registration in [agentRegistry]. One entry
// per [CredentialFormat] covers all three consumer paths: the credential-source
// path ([NewCredentialSourceForProfile]), the preflight path ([CheckCred]), and
// the CLI verify path ([ImportCred]).
//
// Static-credential agents (e.g. cursor, which holds a static JWT) set only
// ImportFn; [NewCredentialSourceForProfile] derives the source automatically by
// wrapping the imported store in a [StaticCredentialSource].
//
// Refresher-backed agents (e.g. codex, whose credential has a live OAuth
// refresh grant) set both ImportFn and SourceFn.  ImportFn reads the on-disk
// credential store (refresh token, expiry) so preflight and CLI verify work
// correctly; SourceFn constructs the live [Refresher] that the broker uses.
//
// OAuth / rotating-chain agents (CredentialFormatNone, e.g. claude-code) set
// DefaultFromPathFn and ImportFromPathFn instead of ImportFn. These two fields
// are CLI-only: they support the `nexus3 auth login --from <path>` import
// route. They are ignored by [NewCredentialSourceForProfile] (which returns
// nil, nil for [CredentialFormatNone] via an early-return) and by [ImportCred]
// (which must not be called for [CredentialFormatNone]).
//
// Adding a new file-based agent requires exactly one entry in [agentRegistry] —
// no other file needs editing.  Adding a new OAuth agent requires a
// [CredentialFormatNone] entry with DefaultFromPathFn and ImportFromPathFn set.
// Tests that register a synthetic format must clean up:
// t.Cleanup(func() { delete(agentRegistry, fmt) }).
type AgentRegistration struct {
	// ImportFn reads the on-disk credential and returns a [DedicatedCredStore].
	// Required for file-based agents; used by [CheckCred] (preflight) and
	// [ImportCred] (CLI verify).  When SourceFn is nil it is also the fallback
	// for the source path — [NewCredentialSourceForProfile] wraps its return
	// value in a [StaticCredentialSource].
	//
	// Nil for OAuth/rotating-chain agents (CredentialFormatNone) — those agents
	// use ImportFromPathFn instead (operator-supplied path, not profile-derived).
	ImportFn func(AgentProfile) (*DedicatedCredStore, error)

	// SourceFn returns the [CredentialSource] for this format.  Nil for
	// static-credential agents (cursor-style); the source is then derived from
	// ImportFn automatically.  Set for refresher-backed agents that need a live
	// [Refresher] — i.e. those whose source cannot be expressed as
	// ImportFn → [StaticCredentialSource].
	SourceFn SourceTransform

	// DefaultFromPathFn, if non-nil, returns the default --from path for the
	// CLI `auth login` import route.  Used only for OAuth/rotating-chain agents
	// (CredentialFormatNone) whose credential file is at an operator-configurable
	// location (not a fixed profile-derived location).  Nil for file-based
	// agents (cursor-style) that derive their path from the profile.
	DefaultFromPathFn func(AgentProfile) string

	// ImportFromPathFn, if non-nil, imports a credential from an explicit
	// operator-supplied on-disk path and returns a [DedicatedCredStore].  Used
	// only for OAuth/rotating-chain agents (CredentialFormatNone).  Nil for
	// file-based agents (cursor-style) whose path is profile-derived.
	ImportFromPathFn func(path string) (*DedicatedCredStore, error)
}

// agentRegistry maps [CredentialFormat] values to their [AgentRegistration].
// One entry per format covers the credential-source, preflight, and CLI verify
// paths.  Tests that register a synthetic format must clean up:
//
//	t.Cleanup(func() { delete(agentRegistry, fmt) })
var agentRegistry = map[CredentialFormat]AgentRegistration{
	// CredentialFormatNone: OAuth/rotating-chain agents (e.g. claude-code).
	// ImportFn and SourceFn are intentionally nil: [NewCredentialSourceForProfile]
	// short-circuits on CredentialFormatNone before reaching the registry, and
	// [ImportCred] must not be called for this format.  Only DefaultFromPathFn
	// and ImportFromPathFn are set — they serve the CLI import route only.
	CredentialFormatNone: {
		DefaultFromPathFn: claudeCodeDefaultFromPath,
		ImportFromPathFn:  ImportClaudeCredentials,
	},

	CredentialFormatCursorJWT: {ImportFn: ImportCursorCredentials},
}

// OAuthImportReg returns the CLI import registration for profile — the default
// --from path and the import-from-path function.  Returns ok=false when the
// profile's format has no path-based import registration (i.e. is not an
// OAuth/rotating-chain agent with CredentialFormatNone, or has no
// ImportFromPathFn registered).
//
// CLI callers: if --from was not set, use defaultPath; call importFn(fromPath)
// in place of any hardcoded per-agent importer.
func OAuthImportReg(profile AgentProfile) (defaultPath string, importFn func(string) (*DedicatedCredStore, error), ok bool) {
	reg, found := agentRegistry[profile.CredentialFormat]
	if !found || reg.ImportFromPathFn == nil {
		return "", nil, false
	}
	dp := ""
	if reg.DefaultFromPathFn != nil {
		dp = reg.DefaultFromPathFn(profile)
	}
	return dp, reg.ImportFromPathFn, true
}

// NewCredentialSourceForProfile returns the credential source appropriate for
// profile.
//
// For OAuth/env-var agents (profile.CredentialFormat == [CredentialFormatNone])
// it returns (nil, nil) — those agents push credentials via a [Refresher].
//
// For file-based agents it looks up the format in [agentRegistry].  If the
// registration supplies a SourceFn (refresher-backed agents) that function is
// called; otherwise the source is derived by calling ImportFn and wrapping the
// result in a [StaticCredentialSource].  An unregistered format (programming
// error: a profile declared a CredentialFormat but no entry was added to
// agentRegistry) returns a descriptive error.
//
// Adding a new file-based agent type requires a new [CredentialFormat] const in
// profile.go and one entry in [agentRegistry] only.  This function is never
// edited for new agents.
func NewCredentialSourceForProfile(profile AgentProfile) (CredentialSource, error) {
	if profile.CredentialFormat == CredentialFormatNone {
		return nil, nil
	}
	reg, ok := agentRegistry[profile.CredentialFormat]
	if !ok {
		return nil, fmt.Errorf("cred: NewCredentialSourceForProfile: no registration for format %q", profile.CredentialFormat)
	}
	if reg.SourceFn != nil {
		return reg.SourceFn(profile)
	}
	// Derive source from ImportFn: import the credential and wrap it.
	store, err := reg.ImportFn(profile)
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
