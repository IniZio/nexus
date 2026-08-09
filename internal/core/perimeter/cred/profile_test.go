package cred_test

import (
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

func TestClaudeCodeProfile_Fields(t *testing.T) {
	p := cred.ClaudeCodeProfile

	if p.PlaceholderEnvVar == "" {
		t.Fatal("ClaudeCodeProfile.PlaceholderEnvVar must not be empty")
	}
	if p.CredentialedHost == "" {
		t.Fatal("ClaudeCodeProfile.CredentialedHost must not be empty")
	}
	if !p.Capabilities.GuestNoSelfRefresh {
		t.Fatal("ClaudeCodeProfile.Capabilities.GuestNoSelfRefresh must be true for Claude Code agents")
	}
}

func TestAgentProfile_DeclarativeExtension(t *testing.T) {
	// Adding a new agent type must require only a new value, not new code.
	// This test demonstrates that by constructing an arbitrary profile inline.
	custom := cred.AgentProfile{
		PlaceholderEnvVar: "MY_AGENT_TOKEN",
		CredentialedHost:  "api.example.com",
		Capabilities: cred.AgentCapabilities{
			GuestNoSelfRefresh: false,
		},
	}
	if custom.PlaceholderEnvVar != "MY_AGENT_TOKEN" {
		t.Fatalf("unexpected PlaceholderEnvVar: %q", custom.PlaceholderEnvVar)
	}
	if custom.CredentialedHost != "api.example.com" {
		t.Fatalf("unexpected CredentialedHost: %q", custom.CredentialedHost)
	}
}
