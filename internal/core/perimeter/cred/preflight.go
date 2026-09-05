package cred

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// PreflightReason classifies the outcome of [CheckCred] for a typed agent
// profile.  Callers branch on this value to render an operator-facing message
// or decide whether to proceed with sandbox creation.
type PreflightReason int

const (
	// PreflightOK means the credential is present and not yet expired.
	//
	// Profiles with CredentialFormatNone (e.g. Claude Code, which conveys its
	// credential entirely via env var / OAuth broker) always yield PreflightOK:
	// they have no file-based credential and are legitimately file-less.
	PreflightOK PreflightReason = iota

	// PreflightAbsent means the credential file expected by the profile does
	// not exist.  The operator must run the agent login flow to provision it.
	PreflightAbsent

	// PreflightUnreadable means the credential file exists but cannot be opened
	// or parsed (corrupt JSON, wrong permissions, empty token field).
	PreflightUnreadable

	// PreflightExpired means the credential was parsed successfully but its
	// recorded expiry timestamp is in the past.
	PreflightExpired
)

// PreflightResult is the typed outcome of [CheckCred].  It carries the agent
// name and the reason — enough for a caller to emit one operator-facing
// sentence.  It never embeds a token value.
type PreflightResult struct {
	// Reason is the classification of the credential state.
	Reason PreflightReason

	// AgentName is the profile.Name of the agent that was checked.
	AgentName string

	// ExpiredAt is the credential's expiry timestamp.  Non-zero only when
	// Reason == PreflightExpired.
	ExpiredAt time.Time
}

// OK reports whether the credential is usable.
func (r PreflightResult) OK() bool { return r.Reason == PreflightOK }

// Sentence returns a single operator-facing sentence naming the agent and the
// reason, suitable for surfacing at sandbox-create time.  Returns an empty
// string for PreflightOK.
//
// The sentence never contains a token value — only metadata.
func (r PreflightResult) Sentence() string {
	switch r.Reason {
	case PreflightOK:
		return ""
	case PreflightAbsent:
		return fmt.Sprintf(
			"%s: credential not found; run 'nexus3 auth login --agent %s' to provision it",
			r.AgentName, r.AgentName,
		)
	case PreflightUnreadable:
		return fmt.Sprintf(
			"%s: credential is present but cannot be read or parsed;"+
				" check file permissions or re-run 'nexus3 auth login --agent %s'",
			r.AgentName, r.AgentName,
		)
	case PreflightExpired:
		return fmt.Sprintf(
			"%s: credential expired at %s; run 'nexus3 auth login --agent %s' to refresh it",
			r.AgentName, r.ExpiredAt.UTC().Format(time.RFC3339), r.AgentName,
		)
	default:
		return fmt.Sprintf("%s: unknown credential state", r.AgentName)
	}
}

// preflightImportRegistry maps CredentialFormat values to the function that
// reads the profile's credential file and returns a raw [DedicatedCredStore].
//
// Preflight needs the store directly (not wrapped in a [CredentialSource]) so
// it can inspect ExpiresAt and distinguish os.ErrNotExist (absent) from other
// parse errors (unreadable).
//
// Adding support for a new file-based credential format requires exactly one
// new entry here; no other change to this file is needed.
var preflightImportRegistry = map[CredentialFormat]func(AgentProfile) (*DedicatedCredStore, error){
	CredentialFormatCursorJWT: ImportCursorCredentials,
}

// CheckCred reports whether the credential described by profile is present and
// usable, comparing expiry against the current wall clock.
//
// Profiles with CredentialFormatNone (e.g. Claude Code) always return
// PreflightOK: they convey credentials via env var / OAuth broker and have no
// file-based credential, so an absent file is expected and correct.
//
// For file-based agents the credential file is read, parsed, and its expiry
// inspected.  The returned [PreflightResult] distinguishes absent / unreadable
// / expired / ok without ever embedding a token value.
func CheckCred(profile AgentProfile) PreflightResult {
	return checkCredAt(profile, time.Now())
}

// checkCredAt is the clock-injectable implementation used by tests.  The now
// parameter is compared against the credential's ExpiresAt field.
func checkCredAt(profile AgentProfile, now time.Time) PreflightResult {
	if profile.CredentialFormat == CredentialFormatNone {
		// No file credential expected; legitimately OK.
		return PreflightResult{Reason: PreflightOK, AgentName: profile.Name}
	}

	importFn, ok := preflightImportRegistry[profile.CredentialFormat]
	if !ok {
		// Unregistered format: programming error in a new profile declaration.
		return PreflightResult{Reason: PreflightUnreadable, AgentName: profile.Name}
	}

	store, err := importFn(profile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PreflightResult{Reason: PreflightAbsent, AgentName: profile.Name}
		}
		return PreflightResult{Reason: PreflightUnreadable, AgentName: profile.Name}
	}

	if !store.ExpiresAt.IsZero() && store.ExpiresAt.Before(now) {
		return PreflightResult{Reason: PreflightExpired, AgentName: profile.Name, ExpiredAt: store.ExpiresAt}
	}

	return PreflightResult{Reason: PreflightOK, AgentName: profile.Name}
}
