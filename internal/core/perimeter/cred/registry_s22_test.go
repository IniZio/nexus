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

// syntheticRefresherSource is the fake CredentialSource used to represent a
// refresher-backed agent in S22 tests.  It does not make network calls.
type syntheticRefresherSource struct {
	token     string
	expiresAt time.Time
}

func (s *syntheticRefresherSource) Token(_ context.Context) (string, time.Time, error) {
	return s.token, s.expiresAt, nil
}

// TestS22_AC1_RefresherBackedAgentAllThreePaths is the S22 core assertion.
//
// It registers a synthetic refresher-backed agent format ONCE in [agentRegistry]
// — with both ImportFn (reads the on-disk store for preflight and CLI verify)
// and SourceFn (builds the live CredentialSource the broker would use).  Then
// it proves the single registration is visible to all three consumer paths:
//
//  1. The credential-source path ([NewCredentialSourceForProfile]) dispatches to
//     SourceFn and returns the live source.
//  2. The preflight path ([CheckCred]) reads the store via ImportFn and returns
//     PreflightOK.
//  3. The CLI verify path ([ImportCred]) reads the store via ImportFn and returns
//     the correct token.
//
// If two registrations were required (one in agentRegistry.ImportFn for paths
// 2–3, another elsewhere for path 1), this test would fail because the extra
// entry is never made.
//
// S22-AC-1.
func TestS22_AC1_RefresherBackedAgentAllThreePaths(t *testing.T) {
	const syntheticFormat CredentialFormat = "s22-refresher-v1"
	const wantToken = "s22-access-token-live"

	// Write the fixture credential store (mimics a refresh-token credential file).
	dir := t.TempDir()
	credPath := filepath.Join(dir, "s22creds.json")
	type credFile struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	data, err := json.Marshal(credFile{AccessToken: wantToken, RefreshToken: "s22-refresh-tok"})
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
		return &DedicatedCredStore{AccessToken: f.AccessToken, RefreshToken: f.RefreshToken}, nil
	}

	// SourceFn returns a fake "refresher" — in production this would construct
	// a real *Refresher using the store's refresh token.
	sourceFn := func(p AgentProfile) (CredentialSource, error) {
		store, err := importFn(p)
		if err != nil {
			return nil, err
		}
		return &syntheticRefresherSource{token: store.AccessToken}, nil
	}

	// ONE registration — ImportFn + SourceFn together in agentRegistry.
	agentRegistry[syntheticFormat] = AgentRegistration{
		ImportFn: importFn,
		SourceFn: sourceFn,
	}
	t.Cleanup(func() { delete(agentRegistry, syntheticFormat) })

	profile := AgentProfile{
		Name:             "s22-refresher-agent",
		CredentialFormat: syntheticFormat,
	}

	// ── Path 1: credential-source path ───────────────────────────────────────
	//
	// Must dispatch to SourceFn (not wrap ImportFn in StaticCredentialSource),
	// returning the live refresher-backed source.
	src, err := NewCredentialSourceForProfile(profile)
	if err != nil {
		t.Fatalf("path-1 NewCredentialSourceForProfile: %v", err)
	}
	if src == nil {
		t.Fatal("path-1: got nil source, want non-nil")
	}
	// Confirm it is the SourceFn result (syntheticRefresherSource), not a StaticCredentialSource.
	if _, isStatic := src.(*StaticCredentialSource); isStatic {
		t.Error("path-1: got StaticCredentialSource; want refresher-backed SourceFn result")
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
	// checkCredAt must find the format in agentRegistry via ImportFn and return
	// PreflightOK.  Under the old split-registry design, a format registered
	// only in credSourceRegistry returned PreflightUnreadable here.
	result := checkCredAt(profile, time.Now())
	if result.Reason != PreflightOK {
		t.Errorf("path-2: checkCredAt reason = %v (%s), want PreflightOK",
			result.Reason, result.Sentence())
	}

	// ── Path 3: CLI verify path ───────────────────────────────────────────────
	store, err := ImportCred(profile)
	if err != nil {
		t.Fatalf("path-3 ImportCred: %v", err)
	}
	if store.AccessToken != wantToken {
		t.Errorf("path-3: store.AccessToken = %q, want %q", store.AccessToken, wantToken)
	}
}

// TestS22_AC2_StaticAgentUnchanged confirms that a static-credential agent
// (cursor-style: ImportFn only, no SourceFn) still works through all three
// paths after the unified-registry change.  The fix for refresher-backed agents
// must not break static agents.
//
// S22-AC-2.
func TestS22_AC2_StaticAgentUnchanged(t *testing.T) {
	const syntheticFormat CredentialFormat = "s22-static-v1"
	const wantToken = "s22-static-token"

	dir := t.TempDir()
	credPath := filepath.Join(dir, "static-creds.json")
	type credFile struct{ Token string `json:"token"` }
	data, _ := json.Marshal(credFile{Token: wantToken})
	if err := os.WriteFile(credPath, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	agentRegistry[syntheticFormat] = AgentRegistration{
		ImportFn: func(p AgentProfile) (*DedicatedCredStore, error) {
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
		},
	}
	t.Cleanup(func() { delete(agentRegistry, syntheticFormat) })

	profile := AgentProfile{Name: "s22-static-agent", CredentialFormat: syntheticFormat}

	// Path 1: source derived from ImportFn — must be StaticCredentialSource.
	src, err := NewCredentialSourceForProfile(profile)
	if err != nil {
		t.Fatalf("path-1: %v", err)
	}
	if _, ok := src.(*StaticCredentialSource); !ok {
		t.Errorf("path-1: got %T, want *StaticCredentialSource", src)
	}
	tok, _, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("path-1 Token: %v", err)
	}
	if tok != wantToken {
		t.Errorf("path-1: token = %q, want %q", tok, wantToken)
	}

	// Path 2: preflight.
	if r := checkCredAt(profile, time.Now()); r.Reason != PreflightOK {
		t.Errorf("path-2: got %v, want PreflightOK", r.Reason)
	}

	// Path 3: verify.
	store, err := ImportCred(profile)
	if err != nil {
		t.Fatalf("path-3: %v", err)
	}
	if store.AccessToken != wantToken {
		t.Errorf("path-3: %q, want %q", store.AccessToken, wantToken)
	}
}

// TestS22_AC3_CheckCredSeesRegisteredFormat proves that [CheckCred] does not
// return [PreflightUnreadable] for a format that is registered in [agentRegistry].
//
// This test goes RED under the old code: under the split-registry design, a
// format registered in credSourceRegistry (the override for refresher-backed
// agents) was invisible to [checkCredAt], which consulted only
// preflightImportRegistry and returned PreflightUnreadable.  Under the unified
// agentRegistry, a single registration makes the format visible to all paths.
//
// S22-AC-3.
func TestS22_AC3_CheckCredSeesRegisteredFormat(t *testing.T) {
	const syntheticFormat CredentialFormat = "s22-ac3-visibility"
	const wantToken = "ac3-token"

	dir := t.TempDir()
	credPath := filepath.Join(dir, "creds.json")
	type credFile struct{ Token string `json:"token"` }
	data, _ := json.Marshal(credFile{Token: wantToken})
	if err := os.WriteFile(credPath, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Register with both ImportFn and a SourceFn (refresher-backed pattern).
	// Under the old code only SourceFn would be registered (in credSourceRegistry)
	// and CheckCred would return PreflightUnreadable.
	agentRegistry[syntheticFormat] = AgentRegistration{
		ImportFn: func(p AgentProfile) (*DedicatedCredStore, error) {
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
		},
		SourceFn: func(p AgentProfile) (CredentialSource, error) {
			return &syntheticRefresherSource{token: wantToken}, nil
		},
	}
	t.Cleanup(func() { delete(agentRegistry, syntheticFormat) })

	profile := AgentProfile{Name: "ac3-agent", CredentialFormat: syntheticFormat}

	result := CheckCred(profile)
	if result.Reason == PreflightUnreadable {
		t.Errorf("CheckCred returned PreflightUnreadable for a registered format — "+
			"format is not visible to preflight; got: %s", result.Sentence())
	}
	if result.Reason != PreflightOK {
		t.Errorf("CheckCred reason = %v, want PreflightOK", result.Reason)
	}
}

// TestS22_AC5_MutationProof_SingleRegistration is the mutation-proof RED/GREEN
// pair for S22-AC-5.
//
// It registers a refresher-backed format, confirms all three paths pass (GREEN),
// then removes the registration and confirms all three paths fail (RED).
// Restoring the registration brings everything back to GREEN.
//
// S22-AC-5.
func TestS22_AC5_MutationProof_SingleRegistration(t *testing.T) {
	const syntheticFormat CredentialFormat = "s22-ac5-mutation"
	const wantToken = "ac5-token"

	dir := t.TempDir()
	credPath := filepath.Join(dir, "creds.json")
	type credFile struct{ Token string `json:"token"` }
	data, _ := json.Marshal(credFile{Token: wantToken})
	if err := os.WriteFile(credPath, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	registration := AgentRegistration{
		ImportFn: func(p AgentProfile) (*DedicatedCredStore, error) {
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
		},
		SourceFn: func(p AgentProfile) (CredentialSource, error) {
			return &syntheticRefresherSource{token: wantToken}, nil
		},
	}

	profile := AgentProfile{Name: "ac5-agent", CredentialFormat: syntheticFormat}
	t.Cleanup(func() { delete(agentRegistry, syntheticFormat) })

	checkAllThreePaths := func(t *testing.T, wantPass bool) {
		t.Helper()

		// Path 1: source.
		src, err := NewCredentialSourceForProfile(profile)
		if wantPass {
			if err != nil {
				t.Errorf("path-1: unexpected error: %v", err)
			} else if src == nil {
				t.Error("path-1: got nil source")
			} else {
				tok, _, terr := src.Token(context.Background())
				if terr != nil || tok != wantToken {
					t.Errorf("path-1: tok=%q err=%v, want %q nil", tok, terr, wantToken)
				}
			}
		} else {
			if err == nil {
				t.Error("path-1: expected error when registration removed, got nil")
			}
		}

		// Path 2: preflight.
		result := CheckCred(profile)
		if wantPass {
			if result.Reason != PreflightOK {
				t.Errorf("path-2: got %v, want PreflightOK", result.Reason)
			}
		} else {
			if result.Reason == PreflightOK {
				t.Error("path-2: expected non-OK when registration removed, got PreflightOK")
			}
		}

		// Path 3: verify.
		store, err := ImportCred(profile)
		if wantPass {
			if err != nil {
				t.Errorf("path-3: unexpected error: %v", err)
			} else if store.AccessToken != wantToken {
				t.Errorf("path-3: %q, want %q", store.AccessToken, wantToken)
			}
		} else {
			if err == nil {
				t.Error("path-3: expected error when registration removed, got nil")
			}
		}
	}

	// GREEN: registration present — all three paths pass.
	agentRegistry[syntheticFormat] = registration
	t.Run("GREEN_registered", func(t *testing.T) {
		checkAllThreePaths(t, true)
	})

	// RED: remove the registration — all three paths fail.
	delete(agentRegistry, syntheticFormat)
	t.Run("RED_unregistered", func(t *testing.T) {
		checkAllThreePaths(t, false)
	})

	// GREEN: restore — all three paths pass again.
	agentRegistry[syntheticFormat] = registration
	t.Run("GREEN_restored", func(t *testing.T) {
		checkAllThreePaths(t, true)
	})
}
