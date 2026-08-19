package service

// TBD-PD-32: everything agent-specific in the guest credential seed must come
// from the agent's profile. Before this, the API-key env var name, the CA-cert
// env var, and claude's telemetry switch were literals in the payload builder,
// so a second agent would have been seeded with Claude Code's environment.
//
// These tests use a synthetic profile deliberately: asserting only against
// ClaudeCodeProfile cannot distinguish "read from the profile" from "hardcoded
// to the same value", which is the exact bug being fixed.

import (
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// otherAgent shares no env var name or host with Claude Code, so any claude
// literal surviving in the payload builder shows up as a failed assertion.
var otherAgent = cred.AgentProfile{
	Name:              "other-agent",
	PlaceholderEnvVar: "OTHER_OAUTH_TOKEN",
	APIKeyEnvVar:      "OTHER_API_KEY",
	CredentialedHost:  "api.other.example",
	EgressHosts:       []string{"api.other.example"},
	CACertEnvVars:     []string{"OTHER_CA_BUNDLE"},
	GuestEnv:          map[string]string{"OTHER_QUIET": "1"},
}

func otherAgentRecords() []cred.PlaceholderRecord {
	return []cred.PlaceholderRecord{{
		Host:        otherAgent.CredentialedHost,
		Placeholder: "0badc0de0badc0de0badc0de",
		ExpiresAt:   time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC),
		SandboxID:   seedTestID(77),
	}}
}

func TestBuildAgentSeedPayload_UsesProfileEnvVarNames(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		kind    agentCredKind
		wantVar string
		notVar  string
	}{
		{"oauth path", kindOAuth, "OTHER_OAUTH_TOKEN", "OTHER_API_KEY"},
		{"api-key path", kindAuthToken, "OTHER_API_KEY", "OTHER_OAUTH_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recs := otherAgentRecords()
			got, err := buildAgentSeedPayload(recs, tc.kind, otherAgent)
			if err != nil {
				t.Fatalf("buildAgentSeedPayload: %v", err)
			}
			payload := string(got)

			if !strings.Contains(payload, tc.wantVar+"="+recs[0].Placeholder) {
				t.Errorf("payload missing %s=<placeholder>; got:\n%s", tc.wantVar, payload)
			}
			if strings.Contains(payload, tc.notVar+"=") {
				t.Errorf("payload must not carry %s on this path; got:\n%s", tc.notVar, payload)
			}
			// The claude literals that used to be unconditional.
			for _, leaked := range []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "NODE_EXTRA_CA_CERTS", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"} {
				if strings.Contains(payload, leaked) {
					t.Errorf("Claude Code's %s leaked into another agent's seed; got:\n%s", leaked, payload)
				}
			}
			if !strings.Contains(payload, "OTHER_CA_BUNDLE="+GuestCACertPath) {
				t.Errorf("payload missing the profile's CA-cert env var; got:\n%s", payload)
			}
			if !strings.Contains(payload, "OTHER_QUIET=1") {
				t.Errorf("payload missing the profile's GuestEnv; got:\n%s", payload)
			}
		})
	}
}

// A profile with no API-key path must fail loudly rather than emit "=<value>"
// with an empty name, which the guest would source as a syntax error at best
// and silently ignore at worst.
func TestBuildAgentSeedPayload_MissingCredEnvVarIsAnError(t *testing.T) {
	t.Parallel()
	oauthOnly := otherAgent
	oauthOnly.APIKeyEnvVar = ""

	if _, err := buildAgentSeedPayload(otherAgentRecords(), kindAuthToken, oauthOnly); err == nil {
		t.Fatal("expected an error for an agent with no API-key env var, got nil")
	}
}

// If no placeholder was minted for the credentialed host the guest would boot
// with no credential at all and fail later as an opaque 401 from inside the VM.
func TestBuildAgentSeedPayload_NoPlaceholderForCredentialedHostIsAnError(t *testing.T) {
	t.Parallel()
	recs := otherAgentRecords()
	recs[0].Host = "unrelated.example"

	if _, err := buildAgentSeedPayload(recs, kindOAuth, otherAgent); err == nil {
		t.Fatal("expected an error when no placeholder covers the credentialed host, got nil")
	}
}

// The default credential path must be chosen from the agent's OWN API-key
// variable. Previously every agent consulted ANTHROPIC_AUTH_TOKEN, so an
// operator holding an Anthropic key would push an unrelated agent onto its
// API-key path and seed it with a variable it does not read.
func TestResolveAgentCredKind_ConsultsOnlyTheAgentsOwnVariable(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-ant-operator-key")
	t.Setenv("OTHER_API_KEY", "")

	if got := resolveAgentCredKind(otherAgent); got != kindOAuth {
		t.Errorf("another agent flipped to the API-key path on Anthropic's variable: got %d, want kindOAuth (%d)", got, kindOAuth)
	}

	t.Setenv("OTHER_API_KEY", "other-key")
	if got := resolveAgentCredKind(otherAgent); got != kindAuthToken {
		t.Errorf("agent did not take its API-key path with its own variable set: got %d, want kindAuthToken (%d)", got, kindAuthToken)
	}
}

// An agent with no API-key path stays on OAuth no matter what is in the
// environment; an empty APIKeyEnvVar must not be read as "look at every
// variable" or as an empty-named lookup that happens to match.
func TestResolveAgentCredKind_NoAPIKeyPathStaysOAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-ant-operator-key")
	oauthOnly := otherAgent
	oauthOnly.APIKeyEnvVar = ""

	if got := resolveAgentCredKind(oauthOnly); got != kindOAuth {
		t.Errorf("agent with no API-key path resolved to %d, want kindOAuth (%d)", got, kindOAuth)
	}
}
