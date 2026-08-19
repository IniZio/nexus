package cred

import "sort"

// AgentCapabilities describes host-enforced constraints for a specific agent
// type. Adding a new capability is a new field; adding a new agent type is a
// new [AgentProfile] value — no code branches required.
type AgentCapabilities struct {
	// GuestNoSelfRefresh instructs the perimeter to set a synthetic far-future
	// expiry in the guest's credential record so the guest-side HTTP client
	// never attempts token self-refresh. All refresh is handled host-side by
	// the broker (P5-S1).
	GuestNoSelfRefresh bool
}

// AgentProfile is a declarative descriptor for one agent type. It carries the
// minimum information the host needs to seed and broker credentials for that
// agent. All fields are read-only after construction.
//
// Adding support for a new agent is solely a matter of declaring a new
// AgentProfile value — no logic branches are required.
type AgentProfile struct {
	// Name is the stable, user-facing identifier for this agent type. It is
	// the value accepted by `--agent <name>` and the value persisted on the
	// sandbox record as domain.Sandbox.AgentName, so it must not change once
	// shipped. The zero value means "no agent profile" (an unnamed profile is
	// never registered and never resolvable by name).
	Name string

	// PlaceholderEnvVar is the name of the environment variable the guest is
	// seeded with (e.g. "CLAUDE_CODE_OAUTH_TOKEN"). The perimeter sets this
	// variable to the high-entropy placeholder value before sandbox boot.
	PlaceholderEnvVar string

	// CredentialedHost is the hostname whose outbound requests the real token
	// authenticates to (e.g. "api.anthropic.com"). The L7 MITM proxy uses this
	// to scope placeholder→real swaps.
	CredentialedHost string

	// EgressHosts is the minimal set of outbound hostnames this agent requires
	// to function. It is the authoritative source for the sandbox's egress
	// allowlist and for the set of hosts the credential broker mints
	// placeholders for. CredentialedHost must appear in this list.
	//
	// Read it through [AgentProfile.Egress], never directly, so callers cannot
	// alias (and therefore mutate) the package-level profile value.
	EgressHosts []string

	// Capabilities holds host-enforced behavioural flags for this agent type.
	Capabilities AgentCapabilities
}

// Egress returns a fresh copy of the profile's egress allowlist. Callers may
// safely assign the result to a mutable field without aliasing the
// package-level profile value.
func (p AgentProfile) Egress() []string {
	out := make([]string, len(p.EgressHosts))
	copy(out, p.EgressHosts)
	return out
}

// ClaudeCodeProfile is the canonical AgentProfile for Claude Code (claude CLI)
// agents running inside a nexus3 sandbox.
var ClaudeCodeProfile = AgentProfile{
	Name:              ClaudeCodeProfileName,
	PlaceholderEnvVar: "CLAUDE_CODE_OAUTH_TOKEN",
	CredentialedHost:  "api.anthropic.com",
	// platform.claude.com is required by claude's OAuth subscription login
	// flow; api.anthropic.com carries inference. Nothing else is reachable.
	EgressHosts: []string{"api.anthropic.com", "platform.claude.com"},
	Capabilities: AgentCapabilities{
		GuestNoSelfRefresh: true,
	},
}

// ClaudeCodeProfileName is the registered name of [ClaudeCodeProfile]. It is
// also the default when no agent is named explicitly.
const ClaudeCodeProfileName = "claude-code"

// DefaultProfileName is the agent used when a sandbox does not name one.
const DefaultProfileName = ClaudeCodeProfileName

// profiles is the registry of every agent nexus3 can seed credentials for,
// keyed by [AgentProfile.Name]. Adding an agent type means adding one entry
// here; no call site branches on the name.
var profiles = map[string]AgentProfile{
	ClaudeCodeProfileName: ClaudeCodeProfile,
}

// ProfileByName resolves a registered agent profile. The second return value
// is false when no agent of that name is registered; callers must surface that
// to the user rather than silently falling back to the default, so that a
// typo'd --agent name is not answered with the wrong credential seed.
func ProfileByName(name string) (AgentProfile, bool) {
	p, ok := profiles[name]
	return p, ok
}

// ProfileNames returns every registered agent name in sorted order, for use in
// help text and in the error message for an unknown --agent value.
func ProfileNames() []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
