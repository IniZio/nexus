package service

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// GuestCredEnvPath is the well-known path inside the guest where the credential
// seed env file is written. Guest shells and the in-guest agent source this
// file at startup to obtain placeholder credentials.
//
// The path is under /run (volatile tmpfs) so it is not persisted across guest
// reboots; the host re-seeds on each CreateAndBoot call.
const GuestCredEnvPath = "/run/nexus3/cred.env"

// GuestCACertPath is the well-known path inside the guest where the MITM proxy
// CA certificate is written as a PEM-encoded file.
//
// Writing this path alone is insufficient to make the guest trust the CA; the
// guest must also run update-ca-certificates (Debian/Ubuntu) or equivalent to
// incorporate the new anchor into the system CA bundle. In P1-S5 that step is
// not automated; the agent bootstrap sequence in a future slice must issue the
// command after seeding. Until then HTTPS clients in the guest will see
// certificate-validation failures when connecting through the MITM proxy.
//
// NODE_EXTRA_CA_CERTS (used in agent egress seeding) sidesteps the
// update-ca-certificates gap: Node.js reads the PEM file directly, so claude
// (a Node.js process) trusts the MITM proxy without a system CA bundle update.
const GuestCACertPath = "/usr/local/share/ca-certificates/nexus3-mitm.crt"

// AnthropicAPIHost is the primary Anthropic API hostname that the in-guest
// claude process reaches for inference. It is the authoritative source of the
// CLAUDE_CODE_OAUTH_TOKEN placeholder that the MITM proxy swaps for the real
// bearer token.
const AnthropicAPIHost = "api.anthropic.com"

// ClaudePlatformHost is the Claude platform hostname required for OAuth
// subscription authentication (used by claude's login flow).
const ClaudePlatformHost = "platform.claude.com"

// AgentEgressHosts returns the minimal set of outbound hostnames the given
// agent requires. The profile is the single source of truth: a new agent type
// gets its allowlist by declaring [cred.AgentProfile.EgressHosts], not by
// editing this function.
//
// The profile is a required argument rather than an optional one so that no
// call site can silently apply Claude Code's allowlist to a different agent.
// Callers with no profile in hand should pass [cred.ClaudeCodeProfile]
// explicitly, which makes the assumption visible in the diff.
//
// Each call returns a fresh slice, so callers may assign it to AllowedHosts
// without aliasing the package-level profile value.
func AgentEgressHosts(profile cred.AgentProfile) []string {
	return profile.Egress()
}

// GuestSeeder delivers the credential seed payload into the guest environment.
// The production implementation writes GuestCredEnvPath via the agent's Copy
// path (see NewAgentCopySeeder). Tests inject a stub that captures the payload
// for assertion without requiring a live VM.
//
// The payload is a newline-delimited sequence of KEY=VALUE lines safe for
// shell sourcing. It contains ONLY placeholder values and synthetic far-future
// expiries — never the real token.
type GuestSeeder func(ctx context.Context, id domain.SandboxID, payload []byte) error

// NewAgentCopySeeder returns a GuestSeeder that delivers the credential seed
// payload to the guest by PUSHing it as GuestCredEnvPath via the agent's Copy
// mechanism. The raw payload bytes are sent directly; IsDirectory=false so the
// guest agent calls pushFile which writes the bytes verbatim (tar wrapping is
// NOT used — that is for directory pushes where pushDir extracts the archive).
//
// Live VM verification of the sourcing convention is deferred to the in-guest
// validation slice; this seeder requires a running guest agent.
func NewAgentCopySeeder(c *agent.Client) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		return c.Copy(ctx, agent.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath: GuestCredEnvPath,
			Src:       bytes.NewReader(payload),
		})
	}
}

// NewGuestFileSeeder returns a GuestSeeder that pushes payload bytes to an
// arbitrary path inside the guest via the agent's Copy mechanism. Use this
// to build per-path seeders (e.g. GuestGitconfigPath for SeedGitIdentity)
// from a live agent client.
//
// The path must be absolute. The bytes are written verbatim (IsDirectory=false).
func NewGuestFileSeeder(c *agent.Client, guestPath string) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		return c.Copy(ctx, agent.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath: guestPath,
			Src:       bytes.NewReader(payload),
		})
	}
}

// SeedGuest mints one placeholder credential per allowed host via broker,
// builds a guest-safe env-file payload (placeholder + far-future expiresAt,
// never the real token), and delivers it to the guest exactly once via seeder.
//
// The returned PlaceholderRecords allow the caller (or tests) to correlate
// placeholder strings with hosts for subsequent host-side operations such as
// broker.SetRealToken.
//
// If broker, seeder, or hosts is nil/empty, SeedGuest is a no-op and returns
// nil records and nil error.
func SeedGuest(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	hosts []string,
	seeder GuestSeeder,
) ([]cred.PlaceholderRecord, error) {
	if broker == nil || seeder == nil || len(hosts) == 0 {
		return nil, nil
	}

	records := make([]cred.PlaceholderRecord, 0, len(hosts))
	for _, host := range hosts {
		// Register with empty realToken. The token is provided later via
		// broker.SetRealToken when the upstream credential is provisioned.
		// Empty realToken is explicitly blessed by cred.Broker (cred.go:108).
		rec, err := broker.RegisterPlaceholder(id, host, "")
		if err != nil {
			return nil, fmt.Errorf("seed: register placeholder for %q: %w", host, err)
		}
		records = append(records, rec)
	}

	payload := buildSeedPayload(records)
	if err := seeder(ctx, id, payload); err != nil {
		return nil, fmt.Errorf("seed: deliver to guest: %w", err)
	}
	return records, nil
}

// buildSeedPayload constructs a shell-sourceable env file from a slice of
// PlaceholderRecords.
//
// # Security invariant
//
// PlaceholderRecord carries ONLY Placeholder, ExpiresAt, SandboxID, and Host.
// The real token is held exclusively inside cred.Broker's unexported entry
// and is structurally unreachable from this function. The produced payload
// therefore cannot contain the real token regardless of what realToken was
// passed to RegisterPlaceholder.
//
// File format (one credential, host "api.github.com"):
//
//	NEXUS3_CRED_API_GITHUB_COM_TOKEN=<64-hex-char placeholder>
//	NEXUS3_CRED_API_GITHUB_COM_EXPIRES_AT=2099-12-31T23:59:59Z
func buildSeedPayload(records []cred.PlaceholderRecord) []byte {
	var buf bytes.Buffer
	for _, rec := range records {
		key := hostToEnvKey(rec.Host)
		fmt.Fprintf(&buf, "NEXUS3_CRED_%s_TOKEN=%s\n", key, rec.Placeholder)
		fmt.Fprintf(&buf, "NEXUS3_CRED_%s_EXPIRES_AT=%s\n", key, rec.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return buf.Bytes()
}

// hostToEnvKey converts a hostname to an env-var-safe uppercase segment.
// Examples: "api.github.com" → "API_GITHUB_COM", "my-proxy:8080" → "MY_PROXY_8080".
func hostToEnvKey(host string) string {
	r := strings.NewReplacer(".", "_", "-", "_", ":", "_")
	return strings.ToUpper(r.Replace(host))
}

// NewAgentCACopySeeder returns a GuestSeeder that delivers a PEM-encoded CA
// certificate to the guest at [GuestCACertPath] via the agent's Copy mechanism.
// The raw PEM bytes are sent directly; IsDirectory=false so the guest agent
// calls pushFile which writes the bytes verbatim. Tar wrapping is NOT used —
// that is for directory pushes where pushDir extracts the archive.
//
// Use this seeder with [SeedCA] to install the MITM proxy trust anchor into the
// guest. After delivery, run update-ca-certificates in the guest so that system
// HTTPS clients (git, wget) also trust the proxy's leaf certificates. Node.js
// (claude) trusts it via NODE_EXTRA_CA_CERTS without update-ca-certificates.
func NewAgentCACopySeeder(c *agent.Client) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		return c.Copy(ctx, agent.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath: GuestCACertPath,
			Src:       bytes.NewReader(payload),
		})
	}
}

// SeedCANodeEnv delivers a minimal credential env file to the guest at
// [GuestCredEnvPath] containing only NODE_EXTRA_CA_CERTS=[GuestCACertPath].
//
// Use this on the persistent supervisor path where [SeedGuestAgent] is not
// called: it ensures claude (a Node.js process) trusts the MITM proxy CA
// without requiring update-ca-certificates. If seeder is nil, SeedCANodeEnv
// is a no-op and returns nil.
func SeedCANodeEnv(ctx context.Context, id domain.SandboxID, seeder GuestSeeder) error {
	if seeder == nil {
		return nil
	}
	payload := []byte("NODE_EXTRA_CA_CERTS=" + GuestCACertPath + "\n")
	return seeder(ctx, id, payload)
}

// SeedCA encodes cert as PEM and delivers it to the guest at [GuestCACertPath]
// via seeder. The write is idempotent; repeated calls overwrite the file.
//
// If cert or seeder is nil, SeedCA is a no-op and returns nil.
//
// Trust-store gap: writing [GuestCACertPath] is necessary but not sufficient.
// The guest must run update-ca-certificates (Debian/Ubuntu) or equivalent to
// incorporate the certificate into the system CA bundle. In P1-S5 this step is
// not automated; the agent bootstrap sequence in a future slice must issue that
// command after seeding.
func SeedCA(ctx context.Context, cert *x509.Certificate, id domain.SandboxID, seeder GuestSeeder) error {
	if cert == nil || seeder == nil {
		return nil
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	return seeder(ctx, id, pemBytes)
}

// GuestAuthorizedKeysPath is the well-known path inside the guest where the
// SSH authorized_keys file is written. sshd reads this file to authenticate
// key-based logins for root.
const GuestAuthorizedKeysPath = "/root/.ssh/authorized_keys"

// NewAgentSSHKeyCopySeeder returns a GuestSeeder that injects a caller-supplied
// OpenSSH public key into the guest at [GuestAuthorizedKeysPath] via the
// agent's Copy mechanism.
//
// The tar archive sent to the guest contains two entries:
//   - .ssh/  (TypeDir, mode 0700) — ensures parent dir exists with strict perms
//   - .ssh/authorized_keys  (TypeReg, mode 0600) — the public key line
//
// The archive is extracted under /root so the full paths resolve correctly.
// IsDirectory=true is set on the Copy call so the guest agent uses pushDir,
// which correctly processes the directory mode from the tar header.
func NewAgentSSHKeyCopySeeder(c *agent.Client) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)

		// Root directory entry: "." with mode 0700 — sets /root itself to
		// strict perms so sshd StrictModes accepts the authorized_keys chain.
		rootHdr := &tar.Header{
			Typeflag: tar.TypeDir,
			Name:     "./",
			Mode:     0700,
			Uid:      0,
			Gid:      0,
		}
		if err := tw.WriteHeader(rootHdr); err != nil {
			return fmt.Errorf("seed ssh: tar root dir header: %w", err)
		}

		// Directory entry: .ssh/ with mode 0700.
		dirHdr := &tar.Header{
			Typeflag: tar.TypeDir,
			Name:     ".ssh/",
			Mode:     0700,
			Uid:      0,
			Gid:      0,
		}
		if err := tw.WriteHeader(dirHdr); err != nil {
			return fmt.Errorf("seed ssh: tar dir header: %w", err)
		}

		// File entry: .ssh/authorized_keys with mode 0600.
		fileHdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     ".ssh/authorized_keys",
			Mode:     0600,
			Uid:      0,
			Gid:      0,
			Size:     int64(len(payload)),
		}
		if err := tw.WriteHeader(fileHdr); err != nil {
			return fmt.Errorf("seed ssh: tar file header: %w", err)
		}
		if _, err := tw.Write(payload); err != nil {
			return fmt.Errorf("seed ssh: tar write: %w", err)
		}
		if err := tw.Close(); err != nil {
			return fmt.Errorf("seed ssh: tar close: %w", err)
		}

		return c.Copy(ctx, agent.CopyOptions{
			Direction:   agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath:   "/root",
			IsDirectory: true,
			Src:         &archive,
		})
	}
}

// SeedSSHAuthorizedKeys delivers pubKey (an OpenSSH authorized_keys line) into
// the guest at [GuestAuthorizedKeysPath] via seeder.
//
// The write is idempotent; repeated calls overwrite the file.
// If pubKey is empty or seeder is nil, SeedSSHAuthorizedKeys is a no-op and
// returns nil.
func SeedSSHAuthorizedKeys(ctx context.Context, pubKey string, id domain.SandboxID, seeder GuestSeeder) error {
	if pubKey == "" || seeder == nil {
		return nil
	}
	// Ensure the key line ends with a newline, as sshd requires.
	keyBytes := []byte(pubKey)
	if len(keyBytes) > 0 && keyBytes[len(keyBytes)-1] != '\n' {
		keyBytes = append(keyBytes, '\n')
	}
	return seeder(ctx, id, keyBytes)
}

// GenerateEphemeralSSHKeypair generates a fresh ed25519 keypair and returns
// the public key in OpenSSH authorized_keys format and the private key in
// OpenSSH PEM format. The caller is responsible for storing the private key
// securely and passing the public key to [CreateAndBootOptions.SSHPublicKey].
func GenerateEphemeralSSHKeypair() (publicKey, privateKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("ssh keygen: generate ed25519: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("ssh keygen: marshal public key: %w", err)
	}
	pubLine := strings.TrimRight(string(ssh.MarshalAuthorizedKey(sshPub)), "\n")

	privPEM, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", "", fmt.Errorf("ssh keygen: marshal private key: %w", err)
	}
	privPEMBytes := pem.EncodeToMemory(privPEM)

	return pubLine, string(privPEMBytes), nil
}

// agentCredKind selects which guest env var carries the Anthropic API
// placeholder. The two kinds are mutually exclusive at runtime.
type agentCredKind int

const (
	// kindUnset is the zero value. When CreateAndBootOptions.AgentCredKind is
	// kindUnset the kind is resolved from the host environment at seed time via
	// resolveAgentCredKind: kindAuthToken when ANTHROPIC_AUTH_TOKEN is set,
	// kindOAuth otherwise. This preserves the pre-per-sandbox default behaviour
	// for callers that do not set an explicit kind.
	kindUnset agentCredKind = iota

	// kindOAuth is the Milestone-A OAuth subscription path: the guest env
	// receives CLAUDE_CODE_OAUTH_TOKEN=<placeholder>. This is the default when
	// ANTHROPIC_AUTH_TOKEN is not set in the host environment.
	kindOAuth

	// kindAuthToken is the direct-SDK API-key path (D-P4-02 / D-P4-05 ToS
	// rail): the guest env receives ANTHROPIC_AUTH_TOKEN=<placeholder>. The in-
	// guest agent sends Authorization: Bearer <placeholder>; the MITM proxy
	// swaps the placeholder for the real token exactly as for the OAuth path.
	kindAuthToken
)

// resolveAgentCredKind returns kindAuthToken when ANTHROPIC_AUTH_TOKEN is set
// in the host environment (direct-SDK API key present), and kindOAuth
// otherwise. It is the default resolver used when no explicit per-sandbox kind
// is set in [CreateAndBootOptions.AgentCredKind].
func resolveAgentCredKind(profile cred.AgentProfile) agentCredKind {
	if profile.APIKeyEnvVar != "" && os.Getenv(profile.APIKeyEnvVar) != "" {
		return kindAuthToken
	}
	return kindOAuth
}

// SeedGuestAgent mints placeholder credentials for [AgentEgressHosts] via
// broker, builds an agent-specific env-file payload that includes both the
// generic NEXUS3_CRED_* vars and the credential-kind-specific var
// (CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_AUTH_TOKEN), and delivers the payload
// to the guest exactly once via seeder.
//
// The credential kind is resolved at call time from the host environment via
// [resolveAgentCredKind]: kindAuthToken when ANTHROPIC_AUTH_TOKEN is set,
// kindOAuth otherwise. For explicit per-sandbox kind control use
// [CreateAndBootOptions.AgentCredKind] and [CreateAndBoot] instead.
//
// The returned PlaceholderRecords allow the caller to call
// broker.SetRealToken(id, AnthropicAPIHost, realToken) after seeding.
//
// # Security invariant
//
// Like [SeedGuest], the real token is structurally unreachable from the
// payload. PlaceholderRecord carries no real-token field; the MITM proxy swaps
// the placeholder for the real token host-side on each proxied request.
//
// If broker or seeder is nil, SeedGuestAgent is a no-op and returns nil records
// and nil error.
func SeedGuestAgent(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	seeder GuestSeeder,
) ([]cred.PlaceholderRecord, error) {
	return seedGuestAgent(ctx, broker, id, seeder, cred.ClaudeCodeProfile, kindUnset)
}

// seedGuestAgent is the internal implementation of [SeedGuestAgent] that
// accepts an explicit [cred.AgentProfile] and [agentCredKind] for per-sandbox
// credential-kind resolution. The profile drives the placeholder env-var name
// emitted in the kindOAuth path (profile.PlaceholderEnvVar). When kind is
// [kindUnset] the kind is resolved from the host environment via
// [resolveAgentCredKind] at call time, preserving the default behaviour for
// callers that do not set an explicit per-sandbox kind.
//
// Callers inside this package (e.g. CreateAndBoot) use this directly so they
// can thread opts.AgentProfile and opts.AgentCredKind through; external callers
// use [SeedGuestAgent].
func seedGuestAgent(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	seeder GuestSeeder,
	profile cred.AgentProfile,
	kind agentCredKind,
) ([]cred.PlaceholderRecord, error) {
	if broker == nil || seeder == nil {
		return nil, nil
	}

	records, payload, err := prepareAgentCredPayload(broker, id, profile, kind)
	if err != nil {
		return nil, err
	}
	// B-SEED: append stdio MCP credential vars (D-PP-04 exemption) so they
	// reach cred.env even on the agent-only route (routeAgent → SeedLoop →
	// SeedGuestAgent). Mirror of what seedGuestAgentAndSecrets does.
	stdioPayload := resolveMCPStdioPayload(profile)
	combined := append(payload, stdioPayload...)
	if err := seeder(ctx, id, combined); err != nil {
		return nil, fmt.Errorf("seed agent: deliver to guest: %w", err)
	}
	return records, nil
}

// prepareAgentCredPayload registers placeholders with broker for each agent
// egress host and builds the seed payload, WITHOUT writing it to the guest.
// Use this when the payload must be composed with other credential sets before
// a single delivery (see [SeedGuestAgentAndSecrets]).
func prepareAgentCredPayload(
	broker *cred.Broker,
	id domain.SandboxID,
	profile cred.AgentProfile,
	kind agentCredKind,
) ([]cred.PlaceholderRecord, []byte, error) {
	// Resolve the credential kind: honour an explicit per-sandbox override;
	// fall back to the process-environment resolver for unset callers.
	if kind == kindUnset {
		kind = resolveAgentCredKind(profile)
	}

	hosts := AgentEgressHosts(profile)
	records := make([]cred.PlaceholderRecord, 0, len(hosts))
	for _, host := range hosts {
		rec, err := broker.RegisterPlaceholder(id, host, "")
		if err != nil {
			return nil, nil, fmt.Errorf("seed agent: register placeholder for %q: %w", host, err)
		}
		records = append(records, rec)
	}

	payload, err := buildAgentSeedPayload(records, kind, profile)
	if err != nil {
		return nil, nil, fmt.Errorf("seed agent: %w", err)
	}
	return records, payload, nil
}

// SeedGuestAgentAndSecrets seeds agent credentials (e.g. CLAUDE_CODE_OAUTH_TOKEN)
// AND human secret placeholders (e.g. GH_TOKEN) into the guest in ONE write.
// Use this for sandboxes that have both an attached agent (AgentName != "") and
// secret binds (SecretHosts non-empty). A second write would silently overwrite
// the first set of credentials; this function composes both into one payload
// and calls the seeder exactly once.
//
// # Security invariant
//
// Both the agent payload and the secret payload are built from [PlaceholderRecord]
// values. Neither path has access to a real token; the combined payload inherits
// the same structural guarantee as [SeedGuestAgent] and [SeedGuestSecrets].
func SeedGuestAgentAndSecrets(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	specs []string,
	seeder GuestSeeder,
) ([]cred.PlaceholderRecord, error) {
	return seedGuestAgentAndSecrets(ctx, broker, id, specs, seeder, cred.ClaudeCodeProfile, kindUnset)
}

func seedGuestAgentAndSecrets(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	specs []string,
	seeder GuestSeeder,
	profile cred.AgentProfile,
	kind agentCredKind,
) ([]cred.PlaceholderRecord, error) {
	if broker == nil || seeder == nil {
		return nil, nil
	}

	// Build agent payload: registers placeholders, returns payload bytes.
	agentRecords, agentPayload, err := prepareAgentCredPayload(broker, id, profile, kind)
	if err != nil {
		return nil, err
	}

	// Build secret payload: resolves specs, mints placeholders, returns bytes.
	var secretPayload []byte
	if len(specs) > 0 {
		binds, err := ResolveEnvelopeSecrets(ctx, specs)
		if err != nil {
			return nil, fmt.Errorf("seed combined: resolve secrets: %w", err)
		}
		secretPayload, _, err = applySecrets(broker, id, binds)
		if err != nil {
			return nil, fmt.Errorf("seed combined: apply secrets: %w", err)
		}
	}

	// Compose ONE payload and write it ONCE. A second write would silently
	// overwrite the first set of credentials (the second, subtler defect the
	// combined path was introduced to fix).
	//
	// B-SEED: stdio MCP credential vars are resolved from host env and
	// appended here (D-PP-04 exemption; see resolveMCPStdioPayload).
	stdioPayload := resolveMCPStdioPayload(profile)
	combined := make([]byte, 0, len(agentPayload)+len(secretPayload)+len(stdioPayload))
	combined = append(combined, agentPayload...)
	combined = append(combined, secretPayload...)
	combined = append(combined, stdioPayload...)

	if err := seeder(ctx, id, combined); err != nil {
		return nil, fmt.Errorf("seed combined: deliver to guest: %w", err)
	}
	return agentRecords, nil
}

// buildAgentSeedPayload extends [buildSeedPayload] with claude-specific env
// vars required for in-guest inference:
//
//   - CLAUDE_CODE_OAUTH_TOKEN (kindOAuth) or ANTHROPIC_AUTH_TOKEN (kindAuthToken):
//     the placeholder for [AnthropicAPIHost]; the MITM proxy swaps this for
//     the real bearer token on each request. Exactly one is emitted per call.
//   - NODE_EXTRA_CA_CERTS: path to the MITM proxy CA cert inside the guest;
//     Node.js reads this directly, sidestepping the update-ca-certificates gap.
//   - CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1: suppresses telemetry and
//     auto-update calls that would hit non-allowlisted hosts.
//
// # Security invariant
//
// PlaceholderRecord carries ONLY the placeholder string, ExpiresAt, SandboxID,
// and Host — never the real token. This function cannot embed the real token
// regardless of what was passed to RegisterPlaceholder or SetRealToken.
func buildAgentSeedPayload(records []cred.PlaceholderRecord, kind agentCredKind, profile cred.AgentProfile) ([]byte, error) {
	// Which env var carries the credential is a property of the agent and of
	// the chosen path, never a literal here: a hardcoded name would be handed
	// to every agent regardless of what it actually reads.
	credEnvVar := profile.PlaceholderEnvVar // OAuth subscription path
	if kind == kindAuthToken {
		credEnvVar = profile.APIKeyEnvVar // direct API-key path (D-P4-02 / ToS rail)
	}
	if credEnvVar == "" {
		return nil, fmt.Errorf("agent %q declares no credential env var for the selected path", profile.Name)
	}

	var buf bytes.Buffer
	buf.Write(buildSeedPayload(records))

	// Emit exactly one credential var, for the host the real token
	// authenticates to. Either name produces Authorization: Bearer
	// <placeholder> on outbound requests; the MITM proxy swaps the placeholder
	// host-side on each forwarded request.
	found := false
	for _, rec := range records {
		if rec.Host == profile.CredentialedHost {
			fmt.Fprintf(&buf, "%s=%s\n", credEnvVar, rec.Placeholder)
			found = true
			break
		}
	}
	if !found {
		// Seeding a guest with no credential at all is worse than failing: the
		// agent starts, reaches the API unauthenticated, and the cause shows up
		// as an opaque 401 from inside the VM.
		return nil, fmt.Errorf("agent %q: no placeholder minted for credentialed host %q",
			profile.Name, profile.CredentialedHost)
	}

	// Runtimes that read a CA bundle from the environment trust the MITM proxy
	// this way, without update-ca-certificates having run in the guest.
	for _, name := range profile.CACertEnvVars {
		fmt.Fprintf(&buf, "%s=%s\n", name, GuestCACertPath)
	}

	// Agent-specific fixed environment, sorted so the payload is byte-stable.
	keys := make([]string, 0, len(profile.GuestEnv))
	for k := range profile.GuestEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&buf, "%s=%s\n", k, profile.GuestEnv[k])
	}

	return buf.Bytes(), nil
}

// GuestShellProfilePath is the well-known path inside the guest where the
// login-shell drop-in that sources GuestCredEnvPath is written.
//
// /etc/profile.d is read by every LOGIN shell (`bash -l`, which is what
// `nexus3 exec --pty <ref> /usr/bin/bash -l` starts). Without this drop-in the
// placeholder credential reaches only commands launched through
// launchCredSourcedArgv — the headless `herdr launch` wrapper. An agent a human
// or an orchestrator starts INTERACTIVELY in a guest shell got no credential at
// all, so it fell back to its own login flow and never spoke through the
// perimeter. That gap is why GuestCredEnvPath's own doc comment ("Guest shells
// ... source this file at startup") was false until this drop-in existed.
const GuestShellProfilePath = "/etc/profile.d/nexus3-cred.sh"

// guestShellProfileScript sources GuestCredEnvPath into every login shell,
// exports IS_SANDBOX=1, and defines a `claude` shell function that adds
// --dangerously-skip-permissions automatically.
//
// IS_SANDBOX=1 — claude refuses --dangerously-skip-permissions when running as
// root (the standard in-guest user) unless this variable is set. Exporting it
// here in the profile means every login shell and its children see it, so
// `claude` in the guest always works without per-invocation boilerplate.
//
// The `claude` function — wraps the claude binary and adds the flag unless the
// caller already passed it. Idempotent: the case-match on `$*` detects the
// flag with surrounding spaces so substrings are not falsely matched. The flag
// is appended only when absent; a double occurrence is never emitted.
// Deliberately bypassable: `command claude` skips shell functions and reaches
// the raw binary without the flag, which is how the non-autonomous path
// (claudeReadyMatch "? for shortcuts") remains meaningful.
//
// IS_SANDBOX and the claude function are safe on sandboxes where the operator
// never intends to start an agent: IS_SANDBOX is a read-only marker and the
// claude function is inert until `claude` is typed.
//
// The existence guard for GuestCredEnvPath matters: it lives on tmpfs and is
// absent on a sandbox with no MITM proxy. A drop-in that errored there would
// break `bash -l` for every plain sandbox. `if`/`fi` is used (not `return`)
// because /etc/profile.d entries are sourced by dash as well as bash, and
// `return` outside a function is not portable.
//
// The script is POSIX sh — no bashisms; the guest may run dash.
const guestShellProfileScript = `# nexus3: credential, sandbox marker, and agent wrapper for login shells.
# Written by SeedGuestShellProfile; do not edit.
if [ -r ` + GuestCredEnvPath + ` ]; then
    set -a
    . ` + GuestCredEnvPath + `
    set +a
fi

# Mark this as a sandbox environment. Required by claude alongside
# --dangerously-skip-permissions when running as root.
export IS_SANDBOX=1

# claude(): add --dangerously-skip-permissions automatically.
# The flag is only added when absent, so callers that already pass it are
# unaffected (no double-flag). Use "command claude" to bypass this wrapper.
claude() {
    case " $* " in
        *" --dangerously-skip-permissions "*)
            command claude "$@"
            ;;
        *)
            command claude --dangerously-skip-permissions "$@"
            ;;
    esac
}
`

// SeedGuestShellProfile writes the login-shell drop-in that sources the
// credential env file into the guest.
//
// It carries NO credential itself — only the path of the file to source — so
// it is safe to write before, after, or independently of the credential seed,
// and safe on a guest whose cred.env never arrives.
//
// If seeder is nil this is a no-op, matching SeedGuest and SeedGuestAgent.
func SeedGuestShellProfile(ctx context.Context, id domain.SandboxID, seeder GuestSeeder) error {
	if seeder == nil {
		return nil
	}
	if err := seeder(ctx, id, []byte(guestShellProfileScript)); err != nil {
		return fmt.Errorf("seed guest shell profile: deliver to guest: %w", err)
	}
	return nil
}

// GuestAgentOnboardingPath is the well-known path inside the guest where the
// claude CLI stores its first-run onboarding state.
//
// The guest runs as root so the path is under /root. Seeding this file lets
// an interactively started `claude` skip the theme-picker and folder-trust
// wizards and go straight to its prompt. Without it the operator sees
// first-run dialogs on every freshly booted sandbox.
const GuestAgentOnboardingPath = "/root/.claude.json"

// GuestExecer runs an arbitrary command in the guest and returns its exit
// code. The production implementation delegates to (*agent.Client).Exec;
// tests inject a spy. argv must be non-empty. A non-zero exit code is
// treated as an error by callers (SeedGuestAgentOnboarding).
//
// stdin is forwarded to the guest process (may be nil for no stdin).
type GuestExecer func(ctx context.Context, id domain.SandboxID, argv []string, stdin io.Reader) (int32, error)

// NewAgentExecer returns a GuestExecer that runs commands in the guest via
// the agent's Exec mechanism. Use it to build a GuestExecer from a live
// agent client.
func NewAgentExecer(c *agent.Client) GuestExecer {
	return func(ctx context.Context, _ domain.SandboxID, argv []string, stdin io.Reader) (int32, error) {
		return c.Exec(ctx, agent.ExecOptions{Argv: argv, Stdin: stdin})
	}
}

// guestAgentOnboardingScript is run inside the guest as `sh -c SCRIPT` by
// SeedGuestAgentOnboarding. Passing the script as a -c argument (rather than
// via stdin) leaves stdin free for `cat > "$tmp"` to read the JSON payload.
//
// It is idempotent: if GuestAgentOnboardingPath already exists it exits 0
// immediately so real agent state (project history, granted allowedTools,
// userID) is never overwritten. The JSON is read from stdin and written
// atomically via a temp file so a killed write cannot leave a truncated file.
//
// The guard is implemented INSIDE the guest (not host-side) because a
// host-side exists-check followed by a write is a TOCTOU race: the file
// could be created between the check and the write by a concurrently
// running agent. The in-guest guard executes as a single atomic shell
// process.
const guestAgentOnboardingScript = `set -e
dst='` + GuestAgentOnboardingPath + `'
[ -e "$dst" ] && exit 0
tmp="${dst}.nexus3.tmp.$$"
cat > "$tmp"
mv "$tmp" "$dst"
`

// claudeOnboardingConfig is the structure marshalled into GuestAgentOnboardingPath.
type claudeOnboardingConfig struct {
	HasCompletedOnboarding bool                          `json:"hasCompletedOnboarding"`
	Theme                  string                        `json:"theme"`
	Projects               map[string]claudeProjectEntry `json:"projects,omitempty"`
}

type claudeProjectEntry struct {
	HasTrustDialogAccepted        bool     `json:"hasTrustDialogAccepted"`
	HasCompletedProjectOnboarding bool     `json:"hasCompletedProjectOnboarding"`
	AllowedTools                  []string `json:"allowedTools"`
}

// SeedGuestAgentOnboarding writes GuestAgentOnboardingPath inside the guest
// so that an interactively started `claude` skips the first-run wizards and
// lands directly at its prompt.
//
// The three keys required, measured in a live guest:
//   - hasCompletedOnboarding — skips the theme-picker wizard.
//   - theme — skips the colour-scheme prompt.
//   - projects[projectDir] — skips the per-directory folder-trust dialog.
//
// If projectDir is empty, no projects entry is written; claude will still
// skip the global wizards but will stop at the folder-trust dialog for
// whichever directory it is started in.
//
// The write is idempotent: if GuestAgentOnboardingPath already exists the
// guest script exits 0 immediately, so real agent state accumulated after
// first launch (project history, operator-granted allowedTools, userID) is
// never clobbered.
//
// If execer is nil this is a no-op, matching SeedGuestShellProfile.
func SeedGuestAgentOnboarding(ctx context.Context, id domain.SandboxID, projectDir string, execer GuestExecer) error {
	if execer == nil {
		return nil
	}

	cfg := claudeOnboardingConfig{
		HasCompletedOnboarding: true,
		Theme:                  "dark",
	}
	if projectDir != "" {
		cfg.Projects = map[string]claudeProjectEntry{
			projectDir: {
				HasTrustDialogAccepted:        true,
				HasCompletedProjectOnboarding: true,
				AllowedTools:                  []string{},
			},
		}
	}

	payload, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("seed guest agent onboarding: marshal config: %w", err)
	}

	// The guest script is passed as a -c argument (not via stdin) so that
	// stdin remains free for `cat > "$tmp"` to receive the JSON payload.
	// The JSON is piped via stdin; encoding/json handles all escaping so
	// projectDir (which may contain double quotes, backslashes, or $(...))
	// is already safe inside the JSON blob and never touches shell syntax.
	code, err := execer(ctx, id,
		[]string{"/bin/sh", "-c", guestAgentOnboardingScript},
		bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("seed guest agent onboarding: exec script: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("seed guest agent onboarding: script exited %d", code)
	}
	return nil
}

// GuestBypassConsentScript merges skipDangerousModePermissionPrompt:true into
// the guest's ~/.claude/settings.json.
//
// It MERGES rather than overwrites: settings.json is co-owned by the agent and
// the operator; clobbering it would silently drop settings accumulated at
// runtime (allowedTools, etc.).
//
// The script runs under node, not python3/python/jq: all three are absent from
// the claude agent image. node is at /usr/local/bin/node and is guaranteed
// present because claude is itself a node program.
//
// Idempotent: re-running sets the same key to the same value; existing settings
// are unaffected.
const GuestBypassConsentScript = `set -e
mkdir -p /root/.claude
node -e '
const fs = require("fs");
const path = "/root/.claude/settings.json";
let cfg = {};
try {
  const parsed = JSON.parse(fs.readFileSync(path, "utf8"));
  if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) cfg = parsed;
} catch (e) { /* absent or unparseable: start from an empty object */ }
cfg.skipDangerousModePermissionPrompt = true;
const tmp = path + ".nexus3.tmp";
fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
fs.renameSync(tmp, path);
'
`

// SeedGuestBypassConsent merges skipDangerousModePermissionPrompt:true into
// the guest's ~/.claude/settings.json so that a `claude` invocation (via the
// shell function added by SeedGuestShellProfile) does not block on the
// bypass-permissions consent wizard.
//
// This is seeded at boot alongside the onboarding seed so the wizard is
// pre-answered for any shell session, not only for sessions that go through
// space-agent.
//
// If execer is nil this is a no-op, matching SeedGuestShellProfile.
func SeedGuestBypassConsent(ctx context.Context, id domain.SandboxID, execer GuestExecer) error {
	if execer == nil {
		return nil
	}
	code, err := execer(ctx, id, []string{"/bin/sh", "-c", GuestBypassConsentScript}, nil)
	if err != nil {
		return fmt.Errorf("seed guest bypass consent: exec script: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("seed guest bypass consent: script exited %d", code)
	}
	return nil
}

// guestMCPServersScript merges an incoming mcpServers map into the guest's
// /root/.claude.json top-level mcpServers key. It reads the merge-payload
// ({"mcpServers":{...}}) from stdin via a temp file, then uses node to
// deep-merge into the existing config without clobbering unrelated keys or
// servers. Atomic write via temp-file rename.
//
// node is the only JSON-capable tool guaranteed present in the guest image
// (/usr/local/bin/node); python3/jq are absent.
const guestMCPServersScript = `set -e
payload_tmp="/tmp/.nexus3-mcpservers.$$"
cat > "$payload_tmp"
PAYLOAD_PATH="$payload_tmp" node -e '
const fs = require("fs");
const payloadPath = process.env.PAYLOAD_PATH;
const payload = JSON.parse(fs.readFileSync(payloadPath, "utf8"));
const dst = "/root/.claude.json";
let cfg = {};
try {
  const parsed = JSON.parse(fs.readFileSync(dst, "utf8"));
  if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) cfg = parsed;
} catch (e) { /* absent or unparseable: start from empty */ }
if (!cfg.mcpServers || typeof cfg.mcpServers !== "object" || Array.isArray(cfg.mcpServers)) {
  cfg.mcpServers = {};
}
Object.assign(cfg.mcpServers, payload.mcpServers || {});
const tmp = dst + ".nexus3.tmp";
fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
fs.renameSync(tmp, dst);
'
rm -f "$payload_tmp"
`

// SeedGuestMCPServers merges the given MCP servers map into the guest's
// /root/.claude.json top-level mcpServers key. Claude Code reads user-scope
// MCP definitions exclusively from that location.
//
// The merge is additive: existing servers under other names are left untouched;
// per-server keys from servers overwrite same-named guests entries (idempotent
// re-run produces identical output).
//
// Must be called AFTER SeedGuestAgentOnboarding so /root/.claude.json already
// exists. If servers is empty or execer is nil this is a no-op.
func SeedGuestMCPServers(ctx context.Context, id domain.SandboxID, servers map[string]json.RawMessage, execer GuestExecer) error {
	if execer == nil || len(servers) == 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return fmt.Errorf("seed guest mcp servers: marshal payload: %w", err)
	}
	code, err := execer(ctx, id, []string{"/bin/sh", "-c", guestMCPServersScript}, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("seed guest mcp servers: exec script: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("seed guest mcp servers: script exited %d", code)
	}
	return nil
}
