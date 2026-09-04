package cred

import "sort"

// MCPConfigFormat identifies the file format a given agent uses for its MCP
// server configuration. The zero value (MCPConfigFormatNone) is correct for
// agents that have no MCP config concept.
//
// Selecting the right format is purely declarative: add a new const for a new
// agent; no code branch is required.
type MCPConfigFormat string

const (
	// MCPConfigFormatNone means the agent has no MCP configuration concept.
	// This is the zero value; unset fields read as none without explicit init.
	MCPConfigFormatNone MCPConfigFormat = ""

	// MCPConfigFormatClaudeJSON is the format used by Claude Code:
	// ~/.claude/claude_mcp_config.json (or the equivalent inside CLAUDE_CONFIG_DIR).
	MCPConfigFormatClaudeJSON MCPConfigFormat = "claude-json"

	// MCPConfigFormatOpencodeJSON is the format used by opencode.
	MCPConfigFormatOpencodeJSON MCPConfigFormat = "opencode-json"

	// MCPConfigFormatCursorJSON is the format used by cursor-agent:
	// ~/.cursor/mcp.json (or .cursor/mcp.json), shape {"mcpServers": {...}}.
	// Structurally identical to MCPConfigFormatClaudeJSON's mcpServers map, but
	// kept as its own constant because the two agents' configs are declared
	// independently and may diverge; nothing here assumes they stay identical.
	//
	// As with MCPConfigFormatOpencodeJSON, declaring this constant does not by
	// itself wire up host-side MCP credential sanitization (mcpdefs.go /
	// mcpcred.go): that machinery is gated to MCPConfigFormatClaudeJSON only
	// and returns a zero value for every other format, cursor included. That
	// gap predates this constant and is not specific to cursor.
	MCPConfigFormatCursorJSON MCPConfigFormat = "cursor-json"
)

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

	// SettingsPath is the host path to the agent's user settings file
	// (e.g. "~/.claude/settings.json" for Claude Code). The tilde is a
	// conventional prefix; callers must expand it with os.UserHomeDir before
	// use. The zero value (empty string) means this agent has no settings file.
	SettingsPath string

	// CredDirEnvVar is the name of the environment variable that redirects the
	// agent's CREDENTIAL directory — the directory that contains the file (or
	// files) holding the agent's authentication secrets. When non-empty,
	// pointing this variable at an isolated directory gives the agent a fresh
	// credential context with no access to the operator's real tokens.
	//
	// This is separate from [AgentProfile.ConfigDirEnvVar] because some agents
	// use different environment variables for their credential store and their
	// settings store. Cursor is the proof: XDG_CONFIG_HOME governs where
	// cursor-agent reads its credential file from (typically resolving to
	// ~/.config/cursor/auth.json), while CURSOR_CONFIG_DIR governs where it
	// reads its settings file cli-config.json from. An agent that uses the same
	// variable for both must set both fields to the same value
	// (e.g. ClaudeCodeProfile sets both to "CLAUDE_CONFIG_DIR").
	//
	// The zero value (empty string) means the agent has no credential-dir
	// redirect available via an environment variable.
	CredDirEnvVar string

	// ConfigDirEnvVar is the name of the environment variable that redirects
	// the agent's SETTINGS (config) directory to an arbitrary path
	// (e.g. "CLAUDE_CONFIG_DIR" for Claude Code, "CURSOR_CONFIG_DIR" for
	// cursor-agent). When non-empty, pointing this variable at an isolated
	// directory separates per-sandbox agent settings from the user's global
	// config.
	//
	// This field governs settings only. For the credential directory, see
	// [AgentProfile.CredDirEnvVar]. The zero value (empty string) means the
	// agent has no settings-dir redirect.
	ConfigDirEnvVar string

	// CredentialFile is the path to the agent's credential file, relative to
	// the credential directory (the directory that CredDirEnvVar points to, or
	// the user's XDG config home when CredDirEnvVar is unset). Empty means the
	// agent conveys its authentication entirely via an environment variable
	// (PlaceholderEnvVar or APIKeyEnvVar) and has no file-based credential.
	//
	// Example: "cursor/auth.json" for cursor-agent, whose credential lives at
	// $XDG_CONFIG_HOME/cursor/auth.json (typically ~/.config/cursor/auth.json).
	//
	// This is a DESCRIPTOR field only. The seeding slice (S8) consumes it to
	// copy the credential file into an isolated credential directory for the
	// sandbox; this package declares the shape, not the mechanic.
	CredentialFile string

	// CredentialFileKey is the JSON field name inside
	// [AgentProfile.CredentialFile] that carries the authentication token.
	// Empty when CredentialFile is empty.
	//
	// Example: "accessToken" for cursor-agent — auth.json holds both
	// accessToken and refreshToken; nexus3 brokers the accessToken field
	// statically (see D-MAC-09: no CURSOR_API_KEY available; static JWT).
	CredentialFileKey string

	// SkillsPath is the host path to the agent's user skills directory
	// (e.g. "~/.claude/skills" for Claude Code). Skills are
	// user-authored reusable prompt fragments; they are not credentials.
	// The zero value (empty string) means this agent has no skills concept.
	SkillsPath string

	// MCPConfigFormat identifies the file format the agent uses for its MCP
	// server configuration. The zero value [MCPConfigFormatNone] means the
	// agent has no MCP config concept and no file needs to be projected.
	MCPConfigFormat MCPConfigFormat

	// MountAllowlist is the curated list of path globs, relative to the
	// agent's config directory, that are SAFE to share read-only into a
	// sandbox. Only portable, non-sensitive files belong here.
	//
	// Secrets — .credentials.json, .claude.json, settings.local.json — are
	// excluded BY OMISSION: the absence of a glob is the refusal. Never add a
	// pattern that would match a secrets file.
	//
	// The zero value (nil or empty) means no files may be shared.
	MountAllowlist []string

	// SettingsAllowlist is the ALLOWLIST of top-level keys, in the settings
	// file named by the basename of [AgentProfile.SettingsPath], that are safe
	// to share into a guest. This is a deliberate allowlist, not a denylist: a
	// key this profile has never vetted — including a FUTURE key that carries
	// a credential — is dropped by default rather than leaked. Adding a key
	// here is an explicit decision that it is non-secret and portable.
	//
	// This is the generalisation of what was, before cursor, a single
	// package-level allowlist hardcoded to Claude's settings.json shape. It
	// exists independently of which credential path (OAuth vs API-key) this
	// agent's broker seeding uses: cursor's cli-config.json can carry an
	// authInfo blob from an operator's own interactive `cursor-agent login`
	// entirely independently of how nexus3 authenticates the guest, so this
	// filter is required for every agent that has a SettingsPath, not just
	// ones nexus3 brokers OAuth for.
	//
	// The zero value (nil) means every key is dropped: a profile that sets
	// SettingsPath but forgets this field stages an empty settings file rather
	// than leaking everything by default.
	SettingsAllowlist map[string]bool

	// BypassConsentKey, when non-empty, names a boolean settings key that
	// AssembleCuratedConfig forces to true in the staged lower-overlay
	// settings file, regardless of the host value, so the guest never blocks
	// on a permission-bypass consent prompt. The zero value (empty string)
	// means this agent has no such mechanism — its autonomous/skip-permissions
	// posture is a launch-time flag instead (see the herdr dispatch launch
	// descriptor), which is the case for cursor's --force/--yolo.
	BypassConsentKey string
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
	// Config-sharing descriptor fields (used by future mount/share slices).
	SettingsPath:    "~/.claude/settings.json",
	CredDirEnvVar:   "CLAUDE_CONFIG_DIR",
	ConfigDirEnvVar: "CLAUDE_CONFIG_DIR",
	SkillsPath:      "~/.claude/skills",
	MCPConfigFormat: MCPConfigFormatClaudeJSON,
	// Portable, non-sensitive files safe to project read-only into sandboxes.
	// Secrets (.credentials.json, .claude.json, settings.local.json) are
	// excluded BY OMISSION — their absence is the refusal.
	MountAllowlist: []string{
		"CLAUDE.md",
		"skills/**",
		"settings.json",
	},
	// portableSettingsTopKeys, generalized: the same allowlist previously
	// hardcoded in internal/core/service/agentconfig.go now lives on the
	// profile it describes. Deliberately EXCLUDED (dropped): apiKeyHelper/
	// awsCredentialExport/gcpAuthRefresh/otelHeadersHelper (credential
	// helpers), env (may hold secrets), hooks (reference host filesystem
	// paths), permissions (host-specific allowlists), sandbox (its
	// credentials subtree is secret and its network policy is irrelevant
	// inside a nexus3 perimeter), and anything unrecognised.
	SettingsAllowlist: map[string]bool{
		"model":                  true,
		"advisorModel":           true,
		"availableModels":        true,
		"theme":                  true,
		"tui":                    true,
		"defaultShell":           true,
		"attribution":            true,
		"enabledPlugins":         true,
		"extraKnownMarketplaces": true,
		"autoMode":               true,
		"effortLevel":            true,
		"autoUpdatesChannel":     true,
		"enableWorkflows":        true,
		// Bypass-permissions consent: always carried into the lower overlayfs
		// layer so plugins (enabledPlugins) and this key coexist. The overlay
		// is file-granular: an upper-layer write of a single-key settings.json
		// would shadow the entire lower file, dropping enabledPlugins and
		// extraKnownMarketplaces. Carrying the key here keeps them all in one
		// layer.
		"skipDangerousModePermissionPrompt": true,
	},
	// See SettingsAllowlist's skipDangerousModePermissionPrompt entry above:
	// this is the key AssembleCuratedConfig forces to true in the lower layer.
	BypassConsentKey: "skipDangerousModePermissionPrompt",
}

// ClaudeCodeProfileName is the registered name of [ClaudeCodeProfile]. It is
// also the default when no agent is named explicitly.
const ClaudeCodeProfileName = "claude-code"

// DefaultProfileName is the agent used when a sandbox does not name one.
const DefaultProfileName = ClaudeCodeProfileName

// CursorAgentProfileName is the registered name of [CursorAgentProfile].
const CursorAgentProfileName = "cursor"

// CursorAgentProfile is the canonical AgentProfile for cursor-agent (the
// `cursor-agent` / `agent` CLI) running inside a nexus3 sandbox.
//
// # Auth path: API key only, not OAuth
//
// cursor-agent has two auth paths: an interactive `cursor-agent login` device
// flow that writes a session JWT into ~/.config/cursor/auth.json (a separate
// credentials file containing {accessToken, refreshToken} — analogous to
// Claude Code's .credentials.json), and a direct `--api-key` /
// CURSOR_API_KEY env var path that requires no file at all.
//
// This profile brokers ONLY the second path (mirroring Claude's APIKeyEnvVar /
// ANTHROPIC_AUTH_TOKEN direct-SDK path), and deliberately leaves
// PlaceholderEnvVar empty: there is no OAuth-subscription placeholder for
// cursor to broker. The OAuth device-flow path is out of scope for this
// profile because cursor reads its credential from a JSON file
// (~/.config/cursor/auth.json), whereas every nexus3 placeholder today is
// env-var shaped — there is no environment variable cursor accepts that
// redirects it to an alternative credential file. A cursor sandbox created
// under this profile authenticates purely via the brokered CURSOR_API_KEY
// placeholder; an operator's own interactive `cursor-agent login` session on
// the host is never brokered or copied into the guest.
//
// Verified empirically (2026-08-30, cursor-agent 2026.08.25-3e8eec8): a fake
// CURSOR_API_KEY, captured via a local CONNECT proxy, causes cursor-agent to
// contact exactly one host — api2.cursor.sh (the documented default
// CURSOR_API_ENDPOINT) — for both the one-shot (`-p`) and interactive paths.
//
// # Curated config still requires the settings filter regardless of auth path
//
// Choosing the API-key path removes the need to BROKER cursor's credential.
// It does NOT remove authInfo from cli-config.json on an operator's real host:
// that file is copied into the guest by AssembleCuratedConfig for its portable
// keys (model, approvalMode, display, editor, permissions, notifications,
// etc.) regardless of which credential path this profile uses, and an
// operator who has ALSO run `cursor-agent login` interactively for their own
// use carries authInfo (account identity and PII: email, displayName, userId,
// authId — NOT a token; the session JWT lives in auth.json, not here) in that
// same file. SettingsAllowlist is therefore load-bearing here independently of
// PlaceholderEnvVar/APIKeyEnvVar — removing it as apparently-dead code would
// silently ship that operator's identity and PII into every cursor sandbox.
var CursorAgentProfile = AgentProfile{
	Name:             CursorAgentProfileName,
	CredentialedHost: "api2.cursor.sh",
	EgressHosts:      []string{"api2.cursor.sh"},
	APIKeyEnvVar:     "CURSOR_API_KEY",
	// cursor-agent is Node.js-based (bundled index.js + native .node addons),
	// so it reads NODE_EXTRA_CA_CERTS directly, same as Claude Code.
	CACertEnvVars:   []string{"NODE_EXTRA_CA_CERTS"},
	SettingsPath: "~/.cursor/cli-config.json",
	// Cursor uses two distinct env vars for two distinct directories.
	// XDG_CONFIG_HOME governs the credential directory: relocating it makes
	// `cursor-agent status` report "Not logged in" because auth.json is no
	// longer found. CURSOR_CONFIG_DIR governs the settings directory only:
	// relocating it does not affect the credential (empirically verified
	// 2026-08-30, cursor-agent 2026.08.25-3e8eec8). They must be set
	// independently; they cannot be collapsed into one field.
	CredDirEnvVar:   "XDG_CONFIG_HOME",
	ConfigDirEnvVar: "CURSOR_CONFIG_DIR",
	// File-based credential descriptor (S8 seeding; D-MAC-09 static-JWT path).
	// The credential lives at $XDG_CONFIG_HOME/cursor/auth.json (typically
	// ~/.config/cursor/auth.json) and contains {accessToken, refreshToken}.
	CredentialFile:    "cursor/auth.json",
	CredentialFileKey: "accessToken",
	MountAllowlist: []string{
		"cli-config.json",
	},
	// See the profile doc comment: required regardless of auth path.
	// Deliberately excludes authInfo (account identity and PII: email,
	// displayName, userId, authId — NOT a token; the credential lives in
	// auth.json, not cli-config.json), privacyCache and
	// serverConfigCache (host-specific caches), suggestNextPrompt (ephemeral
	// UI state), network (ambiguous — may gain a host-specific proxy setting
	// in a future release; excluded by default per the allowlist's own
	// rationale), and sandbox (cursor's own local sandbox policy, irrelevant
	// and potentially host-path-bearing inside a nexus3 perimeter — same
	// rationale as ClaudeCodeProfile's sandbox exclusion).
	SettingsAllowlist: map[string]bool{
		"version":                           true,
		"editor":                            true,
		"display":                           true,
		"notifications":                     true,
		"hints":                             true,
		"modelSlashCommands":                true,
		"rewind":                            true,
		"hasChangedDefaultModel":            true,
		"exploreSubagentModel":              true,
		"permissions":                       true,
		"approvalMode":                      true,
		"autoAcceptWebSearch":               true,
		"attribution":                       true,
		"model":                             true,
		"maxMode":                           true,
		"selectedModel":                     true,
		"modelParameters":                   true,
		"runEverythingSettingsPromptStreak": true,
	},
	// cursor-agent has no settings-file bypass-consent key: the skip-
	// permissions posture is a launch-time flag (--force/--yolo), not a
	// persisted setting. Zero value (empty string) is correct here.
	MCPConfigFormat: MCPConfigFormatCursorJSON,
}

// profiles is the registry of every agent nexus3 can seed credentials for,
// keyed by [AgentProfile.Name]. Adding an agent type means adding one entry
// here; no call site branches on the name.
var profiles = map[string]AgentProfile{
	ClaudeCodeProfileName:  ClaudeCodeProfile,
	CursorAgentProfileName: CursorAgentProfile,
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
