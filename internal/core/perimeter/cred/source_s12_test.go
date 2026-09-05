package cred

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestS12_AC1_SyntheticThirdProfile proves that a synthetic third profile with
// a genuinely different credential shape (nested {"tokens":{"access_token":"…"}}
// vs cursor's flat {"accessToken":"…"}) resolves through
// NewCredentialSourceForProfile without editing that function's body.
//
// The test registers its format directly into credSourceRegistry and cleans up
// afterward; neither source.go nor any selector branch is modified.
func TestS12_AC1_SyntheticThirdProfile(t *testing.T) {
	// Synthetic format: nested token shape — deliberately different from cursor.
	type syntheticAuthFile struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}

	const syntheticFormat CredentialFormat = "synthetic-nested-v1"
	const wantToken = "synthetic-token-ac1"

	// Write the fixture credential file.
	dir := t.TempDir()
	credPath := filepath.Join(dir, "creds.json")
	var f syntheticAuthFile
	f.Tokens.AccessToken = wantToken
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshalling fixture: %v", err)
	}
	if err := os.WriteFile(credPath, data, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	// Register the synthetic transform — one line, no edit to
	// NewCredentialSourceForProfile. The closure captures credPath from the
	// test because the transform API receives an AgentProfile, not a path; in
	// production the transform would resolve the path from profile.CredDirEnvVar,
	// just as cursor does via CursorCredPath.
	credSourceRegistry[syntheticFormat] = func(p AgentProfile) (CredentialSource, error) {
		raw, err := os.ReadFile(credPath)
		if err != nil {
			return nil, err
		}
		var af syntheticAuthFile
		if err := json.Unmarshal(raw, &af); err != nil {
			return nil, err
		}
		return NewStaticCredentialSource(&DedicatedCredStore{AccessToken: af.Tokens.AccessToken}), nil
	}
	t.Cleanup(func() { delete(credSourceRegistry, syntheticFormat) })

	// Synthetic profile — declares the new format, different from cursor.
	profile := AgentProfile{
		Name:             "synthetic-agent",
		CredentialFile:   "creds.json",
		CredentialFormat: syntheticFormat,
	}

	src, err := NewCredentialSourceForProfile(profile)
	if err != nil {
		t.Fatalf("NewCredentialSourceForProfile: %v", err)
	}
	if src == nil {
		t.Fatal("got nil source, want non-nil")
	}

	tok, _, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != wantToken {
		t.Errorf("token = %q, want %q", tok, wantToken)
	}
}

// TestS12_AC3_CursorViaSelector proves that CursorAgentProfile resolves to a
// working StaticCredentialSource through NewCredentialSourceForProfile (the
// selector), not by calling NewCursorCredentialSource directly. The existing
// cursor tests (cursor_test.go) exercise the import/JWT logic; this test
// exercises the registry dispatch path.
func TestS12_AC3_CursorViaSelector(t *testing.T) {
	xdgBase, _ := makeCursorDir(t, `{"accessToken":"cursor-tok-ac3","refreshToken":"r"}`)
	t.Setenv("XDG_CONFIG_HOME", xdgBase)

	src, err := NewCredentialSourceForProfile(CursorAgentProfile)
	if err != nil {
		t.Fatalf("NewCredentialSourceForProfile: %v", err)
	}
	if src == nil {
		t.Fatal("got nil source for cursor profile, want non-nil")
	}

	tok, _, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "cursor-tok-ac3" {
		t.Errorf("token = %q, want %q", tok, "cursor-tok-ac3")
	}
}

// TestS12_AC4_ClaudeYieldsNilSource proves that ClaudeCodeProfile (no
// CredentialFormat, no CredentialFile) returns (nil, nil) from the selector —
// Claude pushes credentials via a Refresher, not a file-based source.
func TestS12_AC4_ClaudeYieldsNilSource(t *testing.T) {
	src, err := NewCredentialSourceForProfile(ClaudeCodeProfile)
	if err != nil {
		t.Fatalf("NewCredentialSourceForProfile: unexpected error: %v", err)
	}
	if src != nil {
		t.Errorf("got non-nil source %T for ClaudeCodeProfile, want nil", src)
	}
}
