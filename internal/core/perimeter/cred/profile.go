package cred

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
	// PlaceholderEnvVar is the name of the environment variable the guest is
	// seeded with (e.g. "CLAUDE_CODE_OAUTH_TOKEN"). The perimeter sets this
	// variable to the high-entropy placeholder value before sandbox boot.
	PlaceholderEnvVar string

	// CredentialedHost is the hostname whose outbound requests the real token
	// authenticates to (e.g. "api.anthropic.com"). The L7 MITM proxy uses this
	// to scope placeholder→real swaps.
	CredentialedHost string

	// Capabilities holds host-enforced behavioural flags for this agent type.
	Capabilities AgentCapabilities
}

// ClaudeCodeProfile is the canonical AgentProfile for Claude Code (claude CLI)
// agents running inside a nexus3 sandbox.
var ClaudeCodeProfile = AgentProfile{
	PlaceholderEnvVar: "CLAUDE_CODE_OAUTH_TOKEN",
	CredentialedHost:  "api.anthropic.com",
	Capabilities: AgentCapabilities{
		GuestNoSelfRefresh: true,
	},
}
