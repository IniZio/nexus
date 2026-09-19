package cred

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// OpencodeGoProviderID is the auth.json key for OpenCode Go.
// The same file is a map of providers; other entries are not this profile's credential.
const OpencodeGoProviderID = "opencode-go"

// opencodeAPIAuth is one provider value in auth.json.
// type "api" carries a static key. type "oauth" and "wellknown" are different grants and are refused.
type opencodeAPIAuth struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

func init() {
	agentRegistry[CredentialFormatOpencodeAPIKey] = AgentRegistration{
		ImportFn: ImportOpencodeCredentials,
	}
}

// OpencodeCredPath returns the absolute path to opencode's auth.json.
//
// Resolution order:
//  1. The env var named by profile.CredDirEnvVar (XDG_DATA_HOME).
//  2. The XDG data default: $HOME/.local/share.
//
// This is not [StaticCredFilePath]. That helper falls back to ~/.config, which
// is the config dir, not the data dir where OpenCode writes auth.json.
func OpencodeCredPath(profile AgentProfile) (string, error) {
	if profile.CredentialFile == "" {
		return "", fmt.Errorf("cred: OpencodeCredPath: profile %q has no CredentialFile", profile.Name)
	}
	base := ""
	if profile.CredDirEnvVar != "" {
		base = os.Getenv(profile.CredDirEnvVar)
	}
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cred: OpencodeCredPath: cannot determine home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, profile.CredentialFile), nil
}

// ImportOpencodeCredentials reads the opencode-go API key from the auth.json
// described by profile and returns a [DedicatedCredStore].
//
// A missing file returns an error wrapping [os.ErrNotExist]. A file that does
// not contain an opencode-go entry of type "api" with a non-empty key returns
// a descriptive error. Other providers in the same file are ignored; their
// keys are never imported.
//
// The key is a static API key. No [Refresher] is wired, and ExpiresAt stays
// the zero [time.Time] because the file has no expiry claim.
func ImportOpencodeCredentials(profile AgentProfile) (*DedicatedCredStore, error) {
	path, err := OpencodeCredPath(profile)
	if err != nil {
		return nil, err
	}
	return importOpencodeCredentialsAt(profile, path)
}

func importOpencodeCredentialsAt(profile AgentProfile, path string) (*DedicatedCredStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cred: ImportOpencodeCredentials %s: %w", path, os.ErrNotExist)
		}
		return nil, fmt.Errorf("cred: ImportOpencodeCredentials %s: reading file: %w", path, err)
	}

	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("cred: ImportOpencodeCredentials %s: parsing JSON: %w", path, err)
	}

	raw, ok := file[OpencodeGoProviderID]
	if !ok {
		return nil, fmt.Errorf("cred: ImportOpencodeCredentials %s: provider %q is absent; cannot import unusable credential",
			path, OpencodeGoProviderID)
	}

	var entry opencodeAPIAuth
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, fmt.Errorf("cred: ImportOpencodeCredentials %s: parsing provider %q: %w", path, OpencodeGoProviderID, err)
	}
	if entry.Type != "api" {
		return nil, fmt.Errorf("cred: ImportOpencodeCredentials %s: provider %q has type %q, want \"api\"; refusing to guess",
			path, OpencodeGoProviderID, entry.Type)
	}
	if entry.Key == "" {
		return nil, fmt.Errorf("cred: ImportOpencodeCredentials %s: %s is empty; cannot import unusable credential",
			path, profile.CredentialFileKey)
	}

	return &DedicatedCredStore{
		AccessToken: entry.Key,
		TokenType:   "Bearer",
	}, nil
}

// NewOpencodeCredentialSource imports the opencode-go key and wraps it in a
// [StaticCredentialSource]. There is no refresh grant.
func NewOpencodeCredentialSource(profile AgentProfile) (*StaticCredentialSource, error) {
	store, err := ImportOpencodeCredentials(profile)
	if err != nil {
		return nil, err
	}
	return NewStaticCredentialSource(store), nil
}
