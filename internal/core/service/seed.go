package service

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
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

// AgentEgressHosts returns the minimal set of outbound hostnames an in-guest
// claude process requires. Each call returns a fresh slice so callers may
// safely assign it to AllowedHosts without aliasing the package-level value.
func AgentEgressHosts() []string {
	return []string{AnthropicAPIHost, ClaudePlatformHost}
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
// mechanism. The payload is wrapped in a single-entry tar archive, as required
// by the agent copy protocol.
//
// Live VM verification of the sourcing convention is deferred to the in-guest
// validation slice; this seeder requires a running guest agent.
func NewAgentCopySeeder(c *agent.Client) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		hdr := &tar.Header{
			Name: GuestCredEnvPath,
			Mode: 0600,
			Size: int64(len(payload)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("seed: tar header: %w", err)
		}
		if _, err := tw.Write(payload); err != nil {
			return fmt.Errorf("seed: tar write: %w", err)
		}
		if err := tw.Close(); err != nil {
			return fmt.Errorf("seed: tar close: %w", err)
		}
		return c.Copy(ctx, agent.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath: GuestCredEnvPath,
			Src:       &archive,
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
// The payload is wrapped in a single-entry tar archive, as required by the
// agent copy protocol.
//
// Use this seeder with [SeedCA] to install the MITM proxy trust anchor into the
// guest. After delivery, the guest must run update-ca-certificates (or
// equivalent) before HTTPS clients will trust the proxy's leaf certificates.
func NewAgentCACopySeeder(c *agent.Client) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		hdr := &tar.Header{
			Name: GuestCACertPath,
			Mode: 0644,
			Size: int64(len(payload)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("seed CA: tar header: %w", err)
		}
		if _, err := tw.Write(payload); err != nil {
			return fmt.Errorf("seed CA: tar write: %w", err)
		}
		if err := tw.Close(); err != nil {
			return fmt.Errorf("seed CA: tar close: %w", err)
		}
		return c.Copy(ctx, agent.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath: GuestCACertPath,
			Src:       &archive,
		})
	}
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
	// kindOAuth is the Milestone-A OAuth subscription path: the guest env
	// receives CLAUDE_CODE_OAUTH_TOKEN=<placeholder>. This is the default when
	// ANTHROPIC_AUTH_TOKEN is not set in the host environment.
	kindOAuth agentCredKind = iota

	// kindAuthToken is the direct-SDK API-key path (D-P4-02 / D-P4-05 ToS
	// rail): the guest env receives ANTHROPIC_AUTH_TOKEN=<placeholder>. The in-
	// guest agent sends Authorization: Bearer <placeholder>; the MITM proxy
	// swaps the placeholder for the real token exactly as for the OAuth path.
	kindAuthToken
)

// resolveAgentCredKind returns kindAuthToken when ANTHROPIC_AUTH_TOKEN is set
// in the host environment (direct-SDK API key present), and kindOAuth
// otherwise. The same env var is the source of truth for [WireAnthropicAuthToken].
func resolveAgentCredKind() agentCredKind {
	if os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		return kindAuthToken
	}
	return kindOAuth
}

// SeedGuestAgent mints placeholder credentials for [AgentEgressHosts] via
// broker, builds an agent-specific env-file payload that includes both the
// generic NEXUS3_CRED_* vars and the credential-kind-specific var
// (CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_AUTH_TOKEN, selected by
// [resolveAgentCredKind]), and delivers the payload to the guest exactly once
// via seeder.
//
// The credential kind is resolved at call time from the host environment:
// kindAuthToken when ANTHROPIC_AUTH_TOKEN is set, kindOAuth otherwise.
// The placeholder env-var name for the kindOAuth path comes from
// [cred.ClaudeCodeProfile].PlaceholderEnvVar ("CLAUDE_CODE_OAUTH_TOKEN").
// For per-sandbox profile control use [seedGuestAgent] directly.
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
	return seedGuestAgent(ctx, broker, id, seeder, cred.ClaudeCodeProfile)
}

// seedGuestAgent is the internal implementation of [SeedGuestAgent] that
// accepts an explicit [cred.AgentProfile] for per-sandbox credential-kind
// resolution. The profile drives the placeholder env-var name emitted in the
// kindOAuth path (profile.PlaceholderEnvVar).
//
// Callers inside this package (e.g. CreateAndBoot) use this directly so they
// can thread opts.AgentProfile through; external callers use [SeedGuestAgent].
func seedGuestAgent(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	seeder GuestSeeder,
	profile cred.AgentProfile,
) ([]cred.PlaceholderRecord, error) {
	if broker == nil || seeder == nil {
		return nil, nil
	}

	hosts := AgentEgressHosts()
	records := make([]cred.PlaceholderRecord, 0, len(hosts))
	for _, host := range hosts {
		rec, err := broker.RegisterPlaceholder(id, host, "")
		if err != nil {
			return nil, fmt.Errorf("seed agent: register placeholder for %q: %w", host, err)
		}
		records = append(records, rec)
	}

	payload := buildAgentSeedPayload(records, resolveAgentCredKind(), profile)
	if err := seeder(ctx, id, payload); err != nil {
		return nil, fmt.Errorf("seed agent: deliver to guest: %w", err)
	}
	return records, nil
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
func buildAgentSeedPayload(records []cred.PlaceholderRecord, kind agentCredKind, profile cred.AgentProfile) []byte {
	var buf bytes.Buffer
	buf.Write(buildSeedPayload(records))

	// Emit exactly one credential-kind var for api.anthropic.com.
	// Both vars produce Authorization: Bearer <placeholder> on outbound requests;
	// the MITM proxy swaps the placeholder host-side on each forwarded request.
	for _, rec := range records {
		if rec.Host == AnthropicAPIHost {
			switch kind {
			case kindAuthToken:
				// Direct-SDK API-key path (D-P4-02 / ToS rail).
				fmt.Fprintf(&buf, "ANTHROPIC_AUTH_TOKEN=%s\n", rec.Placeholder)
			default:
				// OAuth subscription path (Milestone A default).
				// The var name comes from the profile so different agent types
				// (future) can use their own credential env var.
				fmt.Fprintf(&buf, "%s=%s\n", profile.PlaceholderEnvVar, rec.Placeholder)
			}
			break
		}
	}

	// NODE_EXTRA_CA_CERTS makes Node.js (claude's runtime) trust the MITM
	// proxy CA cert directly, without running update-ca-certificates.
	fmt.Fprintf(&buf, "NODE_EXTRA_CA_CERTS=%s\n", GuestCACertPath)

	// Disable telemetry and auto-update traffic that would hit non-allowlisted
	// hosts and be blocked by the egress perimeter.
	fmt.Fprintf(&buf, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1\n")

	return buf.Bytes()
}

// WireAnthropicAuthToken configures opts for an agent sandbox that
// authenticates to Anthropic via a direct API auth token (Bearer token, not
// OAuth subscription). This is the ToS-compliant path for the N-way
// multiplexer (D-P4-02 / D-P4-05).
//
// It mirrors [WireClaudeEgress] but reads ANTHROPIC_AUTH_TOKEN from the host
// environment. The same env var gates [resolveAgentCredKind] inside
// [SeedGuestAgent], so calling this function with the env var set is self-
// consistent: seeding emits ANTHROPIC_AUTH_TOKEN=<placeholder> in the guest,
// and opts.AgentEgressToken carries the real token for broker.SetRealToken.
//
// The caller owns broker and seeder; WireAnthropicAuthToken does not retain
// them beyond writing them into opts.
func WireAnthropicAuthToken(opts *CreateAndBootOptions, broker *cred.Broker, seeder GuestSeeder) {
	opts.AllowedHosts = AgentEgressHosts()
	opts.Broker = broker
	opts.Seeder = seeder
	opts.UseAgentSeed = true
	opts.AgentEgressToken = os.Getenv("ANTHROPIC_AUTH_TOKEN")
}
