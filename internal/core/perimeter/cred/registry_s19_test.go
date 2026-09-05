package cred

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestS19_AC1_SingleRegistrationAllThreePaths proves that registering a synthetic
// file-based agent format in [agentRegistry] exactly once makes it work
// through all three paths that a new file-based agent must traverse:
//
//  1. The credential-source path ([NewCredentialSourceForProfile]).
//  2. The preflight path ([checkCredAt] / [CheckCred]).
//  3. The CLI verify path ([ImportCred], which [runAuthLoginVerify] dispatches to).
//
// A single [agentRegistry] entry drives all three — no separate
// [AgentRegistration.SourceFn] is needed for a static-credential agent.  If
// adding SourceFn were also required, this test would need a more complex
// registration, which would make it fail its own one-registration invariant.
//
// S19-AC-1.
func TestS19_AC1_SingleRegistrationAllThreePaths(t *testing.T) {
	const syntheticFormat CredentialFormat = "s19-synthetic-v1"
	const wantToken = "s19-test-token-ac1"

	// Write the fixture credential file.
	dir := t.TempDir()
	credPath := filepath.Join(dir, "agent-creds.json")
	type credFile struct {
		Token string `json:"token"`
	}
	data, err := json.Marshal(credFile{Token: wantToken})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(credPath, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	importFn := func(p AgentProfile) (*DedicatedCredStore, error) {
		raw, err := os.ReadFile(credPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, os.ErrNotExist
			}
			return nil, err
		}
		var f credFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		return &DedicatedCredStore{AccessToken: f.Token}, nil
	}

	// ONE registration: agentRegistry only, ImportFn only (static-credential agent).
	// SourceFn is deliberately not set — the source must derive automatically.
	agentRegistry[syntheticFormat] = AgentRegistration{ImportFn: importFn}
	t.Cleanup(func() { delete(agentRegistry, syntheticFormat) })

	profile := AgentProfile{
		Name:             "s19-synthetic",
		CredentialFormat: syntheticFormat,
	}

	// ── Path 1: credential-source path ───────────────────────────────────────
	//
	// NewCredentialSourceForProfile must derive the source from agentRegistry
	// without requiring a SourceFn entry.
	src, err := NewCredentialSourceForProfile(profile)
	if err != nil {
		t.Fatalf("path-1 NewCredentialSourceForProfile: %v", err)
	}
	if src == nil {
		t.Fatal("path-1: got nil source, want non-nil")
	}
	tok, _, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("path-1 Token: %v", err)
	}
	if tok != wantToken {
		t.Errorf("path-1: token = %q, want %q", tok, wantToken)
	}

	// ── Path 2: preflight path ────────────────────────────────────────────────
	//
	// checkCredAt must resolve the import function from agentRegistry and
	// return PreflightOK for a valid, non-expired credential.
	result := checkCredAt(profile, time.Now())
	if result.Reason != PreflightOK {
		t.Errorf("path-2: checkCredAt reason = %v (%s), want PreflightOK",
			result.Reason, result.Sentence())
	}
	if result.AgentName != profile.Name {
		t.Errorf("path-2: AgentName = %q, want %q", result.AgentName, profile.Name)
	}

	// ── Path 3: CLI verify path ───────────────────────────────────────────────
	//
	// ImportCred is the single dispatch point that runAuthLoginVerify calls.
	// It must return the store loaded by the registered import function.
	store, err := ImportCred(profile)
	if err != nil {
		t.Fatalf("path-3 ImportCred: %v", err)
	}
	if store.AccessToken != wantToken {
		t.Errorf("path-3: store.AccessToken = %q, want %q", store.AccessToken, wantToken)
	}
}
