//go:build e2e

package auth_test

import (
	"testing"

	"github.com/inizio/nexus/packages/nexus/test/e2e/harness"
)

func TestAuthRelay(t *testing.T) {
	h := harness.New(t)

	// Create a workspace with an auth binding so we can mint tokens against it.
	var createRes struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
	}
	h.MustCall("workspace.create", map[string]any{
		"spec": map[string]any{
			"repo":          "git@github.com:test/repo.git",
			"ref":           "main",
			"workspaceName": "auth-relay-test",
			"authBinding": map[string]string{
				"GITHUB_TOKEN": "test-token-value",
			},
		},
	}, &createRes)

	wsID := createRes.Workspace.ID
	if wsID == "" {
		t.Fatal("create: got empty workspace id")
	}

	// Mint a relay token for the workspace's auth binding.
	var mintRes struct {
		Token string `json:"token"`
	}
	h.MustCall("authrelay.mint", map[string]any{
		"workspaceId": wsID,
		"binding":     "GITHUB_TOKEN",
		"ttlSeconds":  30,
	}, &mintRes)

	if mintRes.Token == "" {
		t.Fatal("authrelay.mint: got empty token")
	}

	// Revoke the token.
	var revokeRes struct {
		Revoked bool `json:"revoked"`
	}
	h.MustCall("authrelay.revoke", map[string]any{
		"token": mintRes.Token,
	}, &revokeRes)

	if !revokeRes.Revoked {
		t.Fatal("authrelay.revoke: expected revoked=true")
	}

	// Minting with an unknown binding should fail.
	err := h.Call("authrelay.mint", map[string]any{
		"workspaceId": wsID,
		"binding":     "NONEXISTENT_BINDING",
	}, nil)
	if err == nil {
		t.Fatal("authrelay.mint with unknown binding: expected error, got nil")
	}

	// Minting with an unknown workspace should fail.
	err = h.Call("authrelay.mint", map[string]any{
		"workspaceId": "does-not-exist",
		"binding":     "GITHUB_TOKEN",
	}, nil)
	if err == nil {
		t.Fatal("authrelay.mint with unknown workspace: expected error, got nil")
	}
}
