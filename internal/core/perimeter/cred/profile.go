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

	// APIKeyEnvVar is the env var name for this agent's direct API-key
	// credential path, as opposed to the OAuth subscription path carried by
	// PlaceholderEnvVar (e.g. "ANTHROPIC_AUTH_TOKEN" for Claude Code). The
	// guest is seeded with exactly one of the two.
	//
	// It doubles as the name of the HOST env var consulted to decide which of
	// the two paths to take by default: an API key present in the host
	// environment under this name selects the API-key path. Each agent looks
	// only at its own variable, so an operator holding an Anthropic API key
	// does not push a different agent onto its API-key path.
	//
	// Empty means this agent has no API-key path; only the OAuth path is
	// available.
	APIKeyEnvVar string

	// CACertEnvVars names guest environment variables that must be set to the
	// path of the MITM proxy's CA certificate inside the guest. Runtimes that
	// read a CA bundle from the environment (Node.js via NODE_EXTRA_CA_CERTS)
	// trust the proxy this way without a system CA-bundle update.
	//
	// The path itself is not stored here: it is a property of the guest
	// filesystem layout, not of the agent, and lives with the seeding code.
	CACertEnvVars []string

	// GuestEnv is additional fixed environment this agent needs in the guest,
	// merged into the credential seed payload. Use it for agent-specific
	// switches such as suppressing telemetry that the egress allowlist would
	// block anyway. Keys are emitted in sorted order so the payload is
	// byte-stable across runs.
	//
	// It must never carry a credential: the seed payload is written to a file
	// in the guest, and the whole design turns on the guest holding only
	// placeholders.
	GuestEnv map[string]string

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
	EgressHosts:  []string{"api.anthropic.com", "platform.claude.com"},
	APIKeyEnvVar: "ANTHROPIC_AUTH_TOKEN",
	// claude runs on Node.js, which reads NODE_EXTRA_CA_CERTS directly and so
	// trusts the MITM proxy without update-ca-certificates having run.
	CACertEnvVars: []string{"NODE_EXTRA_CA_CERTS"},
	GuestEnv: map[string]string{
		// Telemetry and auto-update calls target hosts outside EgressHosts;
		// left on, they retry against a default-deny perimeter.
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	},
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
