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

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

// GuestCredEnvPath is the well-known path for credential seed env file.
const GuestCredEnvPath = "/run/nexus/cred.env"

// GuestCACertPath is the well-known path for MITM proxy CA cert (PEM-encoded).
const GuestCACertPath = "/usr/local/share/ca-certificates/nexus-mitm.crt"

// AnthropicAPIHost is the primary Anthropic API hostname.
const AnthropicAPIHost = "api.anthropic.com"

// ClaudePlatformHost is the Claude platform hostname for OAuth subscription auth.
const ClaudePlatformHost = "platform.claude.com"

func AgentEgressHosts(profile cred.AgentProfile) []string {
	return profile.Egress()
}

type GuestSeeder func(ctx context.Context, id domain.SandboxID, payload []byte) error

func NewAgentCopySeeder(c *agent.Client) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		eb := int64(len(payload))
		return c.Copy(ctx, agent.CopyOptions{
			Direction:     agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath:     GuestCredEnvPath,
			Src:           bytes.NewReader(payload),
			ExpectedBytes: &eb,
		})
	}
}

func NewGuestFileSeeder(c *agent.Client, guestPath string) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		eb := int64(len(payload))
		return c.Copy(ctx, agent.CopyOptions{
			Direction:     agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath:     guestPath,
			Src:           bytes.NewReader(payload),
			ExpectedBytes: &eb,
		})
	}
}

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

// /** Security: payload cannot contain real token; PlaceholderRecord holds only placeholder. */
func buildSeedPayload(records []cred.PlaceholderRecord) []byte {
	var buf bytes.Buffer
	for _, rec := range records {
		key := hostToEnvKey(rec.Host)
		fmt.Fprintf(&buf, "NEXUS_CRED_%s_TOKEN=%s\n", key, rec.Placeholder)
		fmt.Fprintf(&buf, "NEXUS_CRED_%s_EXPIRES_AT=%s\n", key, rec.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return buf.Bytes()
}

func hostToEnvKey(host string) string {
	r := strings.NewReplacer(".", "_", "-", "_", ":", "_")
	return strings.ToUpper(r.Replace(host))
}

func NewAgentCACopySeeder(c *agent.Client) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		eb := int64(len(payload))
		return c.Copy(ctx, agent.CopyOptions{
			Direction:     agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath:     GuestCACertPath,
			Src:           bytes.NewReader(payload),
			ExpectedBytes: &eb,
		})
	}
}

func SeedCANodeEnv(ctx context.Context, id domain.SandboxID, seeder GuestSeeder) error {
	if seeder == nil {
		return nil
	}
	payload := []byte("NODE_EXTRA_CA_CERTS=" + GuestCACertPath + "\n")
	return seeder(ctx, id, payload)
}

func SeedCA(ctx context.Context, cert *x509.Certificate, id domain.SandboxID, seeder GuestSeeder) error {
	if cert == nil || seeder == nil {
		return nil
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	return seeder(ctx, id, pemBytes)
}

const GuestAuthorizedKeysPath = "/root/.ssh/authorized_keys"

func NewAgentSSHKeyCopySeeder(c *agent.Client) GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)

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

func SeedSSHAuthorizedKeys(ctx context.Context, pubKey string, id domain.SandboxID, seeder GuestSeeder) error {
	if pubKey == "" || seeder == nil {
		return nil
	}
	keyBytes := []byte(pubKey)
	if len(keyBytes) > 0 && keyBytes[len(keyBytes)-1] != '\n' {
		keyBytes = append(keyBytes, '\n')
	}
	return seeder(ctx, id, keyBytes)
}

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

// agentCredKind selects which guest env var carries API placeholder.
type agentCredKind int

const (
	// kindUnset: resolve from host env via resolveAgentCredKind.
	kindUnset agentCredKind = iota
	// kindOAuth: CLAUDE_CODE_OAUTH_TOKEN (D-TP-08 path).
	kindOAuth
	// kindAuthToken: ANTHROPIC_AUTH_TOKEN (D-P4-02 direct API-key path).
	kindAuthToken
)

// resolveAgentCredKind returns kind from host environment or defaults to kindOAuth.
func resolveAgentCredKind(profile cred.AgentProfile) agentCredKind {
	if profile.APIKeyEnvVar != "" && os.Getenv(profile.APIKeyEnvVar) != "" {
		return kindAuthToken
	}
	return kindOAuth
}

func SeedGuestAgent(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	seeder GuestSeeder,
) ([]cred.PlaceholderRecord, error) {
	return seedGuestAgent(ctx, broker, id, seeder, cred.MustProfileByName(cred.ClaudeCodeProfileName), kindUnset)
}

func SeedGuestAgentForProfile(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	seeder GuestSeeder,
	profile cred.AgentProfile,
) ([]cred.PlaceholderRecord, error) {
	return seedGuestAgent(ctx, broker, id, seeder, profile, kindUnset)
}

func seedGuestAgent(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	seeder GuestSeeder,
	profile cred.AgentProfile,
	kind agentCredKind,
) ([]cred.PlaceholderRecord, error) {
	return seedGuestAgentForProfiles(ctx, broker, id, seeder, profile, kind, nil)
}

func seedGuestAgentForProfiles(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	seeder GuestSeeder,
	primary cred.AgentProfile,
	primaryKind agentCredKind,
	extras []cred.AgentProfile,
) ([]cred.PlaceholderRecord, error) {
	if broker == nil || seeder == nil {
		return nil, nil
	}

	allRecs, payload, err := prepareAgentCredPayload(broker, id, primary, primaryKind)
	if err != nil {
		return nil, err
	}
	for _, extra := range extras {
		extraRecs, extraPayload, extraErr := prepareAgentCredPayload(broker, id, extra, kindUnset)
		if extraErr != nil {
			return nil, extraErr
		}
		allRecs = append(allRecs, extraRecs...)
		payload = append(payload, extraPayload...)
	}
	// B-SEED (D-PP-04): stdio MCP credential vars reach cred.env via routeAgent.
	stdioPayload := resolveMCPStdioPayload(primary)
	combined := append(payload, stdioPayload...)
	if err := seeder(ctx, id, combined); err != nil {
		return nil, fmt.Errorf("seed agent: deliver to guest: %w", err)
	}
	return allRecs, nil
}

func SeedGuestAgentForProfiles(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	seeder GuestSeeder,
	primary cred.AgentProfile,
	extras []cred.AgentProfile,
) ([]cred.PlaceholderRecord, error) {
	return seedGuestAgentForProfiles(ctx, broker, id, seeder, primary, kindUnset, extras)
}

func prepareAgentCredPayload(
	broker *cred.Broker,
	id domain.SandboxID,
	profile cred.AgentProfile,
	kind agentCredKind,
) ([]cred.PlaceholderRecord, []byte, error) {
	if kind == kindUnset {
		kind = resolveAgentCredKind(profile)
	}

	hosts := AgentEgressHosts(profile)
	records := make([]cred.PlaceholderRecord, 0, len(hosts))
	for _, host := range hosts {
		var rec cred.PlaceholderRecord
		var err error
		if profile.PlaceholderIsJWT {
			rec, err = broker.RegisterJWTPlaceholder(id, host, "")
		} else {
			rec, err = broker.RegisterPlaceholder(id, host, "")
		}
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

func SeedGuestAgentAndSecrets(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	specs []string,
	seeder GuestSeeder,
) ([]cred.PlaceholderRecord, error) {
	return seedGuestAgentAndSecrets(ctx, broker, id, specs, seeder, cred.MustProfileByName(cred.ClaudeCodeProfileName), kindUnset)
}

func SeedGuestAgentAndSecretsForProfile(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	specs []string,
	seeder GuestSeeder,
	profile cred.AgentProfile,
) ([]cred.PlaceholderRecord, error) {
	return seedGuestAgentAndSecrets(ctx, broker, id, specs, seeder, profile, kindUnset)
}

func SeedGuestAgentAndSecretsForProfiles(
	ctx context.Context,
	broker *cred.Broker,
	id domain.SandboxID,
	specs []string,
	seeder GuestSeeder,
	primary cred.AgentProfile,
	extras []cred.AgentProfile,
) ([]cred.PlaceholderRecord, error) {
	if broker == nil || seeder == nil {
		return nil, nil
	}

	allRecs, agentPayload, err := prepareAgentCredPayload(broker, id, primary, kindUnset)
	if err != nil {
		return nil, err
	}
	for _, extra := range extras {
		extraRecs, extraPayload, extraErr := prepareAgentCredPayload(broker, id, extra, kindUnset)
		if extraErr != nil {
			return nil, extraErr
		}
		allRecs = append(allRecs, extraRecs...)
		agentPayload = append(agentPayload, extraPayload...)
	}

	var secretPayload []byte
	if len(specs) > 0 {
		binds, resolveErr := ResolveEnvelopeSecrets(ctx, specs)
		if resolveErr != nil {
			return nil, fmt.Errorf("seed combined: resolve secrets: %w", resolveErr)
		}
		secretPayload, _, err = applySecrets(broker, id, binds)
		if err != nil {
			return nil, fmt.Errorf("seed combined: apply secrets: %w", err)
		}
	}

	stdioPayload := resolveMCPStdioPayload(primary)
	combined := make([]byte, 0, len(agentPayload)+len(secretPayload)+len(stdioPayload))
	combined = append(combined, agentPayload...)
	combined = append(combined, secretPayload...)
	combined = append(combined, stdioPayload...)

	if err := seeder(ctx, id, combined); err != nil {
		return nil, fmt.Errorf("seed combined: deliver to guest: %w", err)
	}
	return allRecs, nil
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

	agentRecords, agentPayload, err := prepareAgentCredPayload(broker, id, profile, kind)
	if err != nil {
		return nil, err
	}

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

	// Compose ONE payload and write ONCE (second write would silently overwrite).
	// B-SEED (D-PP-04): stdio MCP vars appended.
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

// buildAgentSeedPayload adds agent-specific env vars (credential + CA cert + config).
// /** Security: payload cannot contain real token; PlaceholderRecord holds only placeholder. */
func buildAgentSeedPayload(records []cred.PlaceholderRecord, kind agentCredKind, profile cred.AgentProfile) ([]byte, error) {
	credEnvVar := profile.PlaceholderEnvVar
	if kind == kindAuthToken {
		credEnvVar = profile.APIKeyEnvVar
	}
	// File-based agents use SeedGuestCredFile; OAuth agents use PlaceholderEnvVar.
	if credEnvVar == "" && profile.CredentialFile == "" {
		return nil, fmt.Errorf("agent %q declares no credential env var for the selected path", profile.Name)
	}

	var buf bytes.Buffer
	buf.Write(buildSeedPayload(records))

	if credEnvVar != "" {
		found := false
		for _, rec := range records {
			if rec.Host == profile.CredentialedHost {
				fmt.Fprintf(&buf, "%s=%s\n", credEnvVar, rec.Placeholder)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("agent %q: no placeholder minted for credentialed host %q",
				profile.Name, profile.CredentialedHost)
		}
	}

	for _, name := range profile.CACertEnvVars {
		fmt.Fprintf(&buf, "%s=%s\n", name, GuestCACertPath)
	}

	if profile.CredentialFile != "" && profile.CredDirEnvVar != "" {
		fmt.Fprintf(&buf, "%s=%s\n", profile.CredDirEnvVar, GuestCredDirPath)
	}

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

const GuestCredDirPath = "/run/nexus/cred-dir"

func GuestCredFilePath(profile cred.AgentProfile) string {
	if profile.CredentialFile == "" {
		return ""
	}
	return GuestCredDirPath + "/" + profile.CredentialFile
}

// /** Security: JSON contains only placeholder strings, never real tokens. */
func buildCredFileSeedPayload(records []cred.PlaceholderRecord, profile cred.AgentProfile) ([]byte, error) {
	if profile.CredentialFile == "" {
		return nil, nil
	}
	var placeholder string
	for _, rec := range records {
		if rec.Host == profile.CredentialedHost {
			placeholder = rec.Placeholder
			break
		}
	}
	if placeholder == "" {
		return nil, fmt.Errorf("agent %q: no placeholder minted for credentialed host %q",
			profile.Name, profile.CredentialedHost)
	}
	m := make(map[string]string, 1+len(profile.CredentialFileExtraKeys))
	m[profile.CredentialFileKey] = placeholder
	for _, k := range profile.CredentialFileExtraKeys {
		m[k] = placeholder
	}
	content, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("agent %q: marshal credential file: %w", profile.Name, err)
	}
	return content, nil
}

func SeedGuestCredFile(
	ctx context.Context,
	id domain.SandboxID,
	records []cred.PlaceholderRecord,
	profile cred.AgentProfile,
	seeder GuestSeeder,
) error {
	if seeder == nil || profile.CredentialFile == "" {
		return nil
	}
	content, err := buildCredFileSeedPayload(records, profile)
	if err != nil {
		return fmt.Errorf("seed cred file: %w", err)
	}
	if content == nil {
		return nil
	}
	if err := seeder(ctx, id, content); err != nil {
		return fmt.Errorf("seed cred file: deliver to guest: %w", err)
	}
	return nil
}

const GuestShellProfilePath = "/etc/profile.d/nexus-cred.sh"

// GuestHostUIDEnvPath is written by SeedGuestHostUID for non-login exec sessions.
// The guest agent's guestBaselineEnv reads it so every exec'd process sees
// NEXUS_HOST_UID and NEXUS_HOST_GID without requiring a login shell.
const GuestHostUIDEnvPath = "/etc/nexus/hostuid.env"

func buildGuestShellProfileScript(uid, gid int) string {
	return fmt.Sprintf(`# nexus: credential and sandbox marker for login shells.
# Written by SeedGuestShellProfile; do not edit.
if [ -r `+GuestCredEnvPath+` ]; then
    set -a
    . `+GuestCredEnvPath+`
    set +a
fi
# Mark this as a sandbox environment. Required by claude when running as root.
export IS_SANDBOX=1
# Wire the SSH shim for git operations. /sbin/nexus-agent is the boot
# contract (init=/sbin/nexus-agent) and therefore present in every guest;
# /usr/local/bin/nexus-agent exists only in builder images. This env var
# overrides the core.sshCommand written by git_identity.go, so both must agree.
export GIT_SSH_COMMAND='/sbin/nexus-agent git-ssh'
# Host uid/gid owning virtiofs-shared dirs. Non-root container users must match.
export NEXUS_HOST_UID=%d
export NEXUS_HOST_GID=%d
`, uid, gid)
}

func SeedGuestShellProfile(ctx context.Context, id domain.SandboxID, uid, gid int, seeder GuestSeeder) error {
	if seeder == nil {
		return nil
	}
	if err := seeder(ctx, id, []byte(buildGuestShellProfileScript(uid, gid))); err != nil {
		return fmt.Errorf("seed guest shell profile: deliver to guest: %w", err)
	}
	return nil
}

// SeedGuestHostUID writes NEXUS_HOST_UID and NEXUS_HOST_GID to GuestHostUIDEnvPath
// (/etc/nexus/hostuid.env) in KEY=VALUE format. The guest agent merges this file
// into every exec's baseline environment so non-login sessions see the values too.
func SeedGuestHostUID(ctx context.Context, id domain.SandboxID, uid, gid int, seeder GuestSeeder) error {
	if seeder == nil {
		return nil
	}
	content := fmt.Sprintf("NEXUS_HOST_UID=%d\nNEXUS_HOST_GID=%d\n", uid, gid)
	if err := seeder(ctx, id, []byte(content)); err != nil {
		return fmt.Errorf("seed guest host uid: deliver to guest: %w", err)
	}
	return nil
}

const GuestAgentOnboardingPath = "/root/.claude.json"

type GuestExecer func(ctx context.Context, id domain.SandboxID, argv []string, stdin io.Reader) (int32, error)

func NewAgentExecer(c *agent.Client) GuestExecer {
	return func(ctx context.Context, _ domain.SandboxID, argv []string, stdin io.Reader) (int32, error) {
		return c.Exec(ctx, agent.ExecOptions{Argv: argv, Stdin: stdin})
	}
}

const guestAgentOnboardingScript = `set -e
dst='` + GuestAgentOnboardingPath + `'
[ -e "$dst" ] && exit 0
tmp="${dst}.nexus.tmp.$$"
cat > "$tmp"
mv "$tmp" "$dst"
`

type claudeOnboardingConfig struct {
	HasCompletedOnboarding bool                          `json:"hasCompletedOnboarding"`
	Theme                  string                        `json:"theme"`
	Projects               map[string]claudeProjectEntry `json:"projects,omitempty"`
	// MCPServers: json.RawMessage preserves placeholder refs for MITM/refresher.
	MCPServers map[string]json.RawMessage `json:"mcpServers,omitempty"`
}

type claudeProjectEntry struct {
	HasTrustDialogAccepted        bool     `json:"hasTrustDialogAccepted"`
	HasCompletedProjectOnboarding bool     `json:"hasCompletedProjectOnboarding"`
	AllowedTools                  []string `json:"allowedTools"`
}

func SeedGuestAgentOnboarding(ctx context.Context, id domain.SandboxID, projectDir string, servers map[string]json.RawMessage, execer GuestExecer) error {
	if execer == nil {
		return nil
	}

	cfg := claudeOnboardingConfig{
		HasCompletedOnboarding: true,
		Theme:                  "dark",
		MCPServers:             servers,
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

const GuestUserMountsProfilePath = "/etc/profile.d/nexus-usermounts.sh"

const GuestNativePATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

const GuestUserMountsFarmReport = "/run/nexus/hostbin.report"

func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func SeedGuestUserMounts(ctx context.Context, id domain.SandboxID, manifest UserMountManifest, execer GuestExecer) error {
	if execer == nil || len(manifest.Mounts) == 0 {
		return nil
	}

	script := buildUserMountScript(manifest)
	code, err := execer(ctx, id, []string{"/bin/sh", "-c", script}, nil)
	if err != nil {
		return fmt.Errorf("usermount seed: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("usermount seed script exited %d", code)
	}
	return nil
}

func buildUserMountScript(manifest UserMountManifest) string {
	var b strings.Builder
	b.WriteString("set -eu\n\n")

	// Step 2: PATH drop-in.
	qProfile := shSingleQuote(GuestUserMountsProfilePath)
	fmt.Fprintf(&b, "# 2. PATH drop-in\n")
	fmt.Fprintf(&b, "if [ ! -f %s ]; then\n", qProfile)
	fmt.Fprintf(&b, "cat > %s << 'NEXUSUMEOF'\n", qProfile)
	fmt.Fprintf(&b, "# nexus: user-mount PATH for login shells.\n")
	fmt.Fprintf(&b, "# Written by SeedGuestUserMounts; do not edit.\n")
	pathParts := append([]string{}, GuestCuratedPATHDirs...)
	pathParts = append(pathParts, manifest.ExtraPathDirs...)
	fmt.Fprintf(&b, "export PATH=\"$PATH:%s\"\n", strings.Join(pathParts, ":"))
	fmt.Fprintf(&b, "NEXUSUMEOF\n")
	fmt.Fprintf(&b, "fi\n\n")

	// Step 3: overlay mounts for overlay=true rows.
	for _, m := range manifest.Mounts {
		if !m.Overlay {
			continue
		}
		name := m.StagingGuestPath
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" {
			name = "um"
		}
		qStaging := shSingleQuote(m.StagingGuestPath)
		qGuest := shSingleQuote(m.GuestPath)
		qUp := shSingleQuote("/run/nexus/ovl-um/" + name + "/up")
		qWork := shSingleQuote("/run/nexus/ovl-um/" + name + "/work")
		fmt.Fprintf(&b, "# 3. Overlay: %s\n", m.GuestPath)
		fmt.Fprintf(&b, "if [ -d %s ] && ! mountpoint -q %s 2>/dev/null; then\n", qStaging, qGuest)
		fmt.Fprintf(&b, "  mkdir -p %s\n", qGuest)
		fmt.Fprintf(&b, "  mkdir -p %s %s\n", qUp, qWork)
		fmt.Fprintf(&b, "  mount -t overlay overlay -o lowerdir=%s,upperdir=%s,workdir=%s %s\n",
			qStaging, qUp, qWork, qGuest)
		fmt.Fprintf(&b, "fi\n\n")
	}

	// Step 4: curated symlink farm (exclude shadowed/dangling; atomic updates).
	reportInit := false
	for _, m := range manifest.Mounts {
		if !m.Curated {
			continue
		}
		if !reportInit {
			qReport := shSingleQuote(GuestUserMountsFarmReport)
			fmt.Fprintf(&b, "# 4. Curated farm: truncate report\n")
			fmt.Fprintf(&b, ": > %s\n\n", qReport)
			reportInit = true
		}
		qReport := shSingleQuote(GuestUserMountsFarmReport)

		if m.CuratedSubPath != "" {
			qStaging := shSingleQuote(m.StagingGuestPath)
			qGuest := shSingleQuote(m.GuestPath)
			qSubPath := shSingleQuote(m.CuratedSubPath)
			qFarmGuest := shSingleQuote(m.GuestPath + "/" + m.CuratedSubPath)
			qFarmStaging := shSingleQuote(m.StagingGuestPath + "/" + m.CuratedSubPath)
			fmt.Fprintf(&b, "# 4. Curated farm (containment): %s [sub: %s]\n", m.GuestPath, m.CuratedSubPath)
			fmt.Fprintf(&b, "if [ -d %s ]; then\n", qStaging)
			fmt.Fprintf(&b, "  mkdir -p %s\n", qGuest)
			fmt.Fprintf(&b, "  for _d in %s/*; do\n", qStaging)
			fmt.Fprintf(&b, "    [ -e \"$_d\" ] || [ -L \"$_d\" ] || continue\n")
			fmt.Fprintf(&b, "    _n=$(basename \"$_d\")\n")
			fmt.Fprintf(&b, "    [ \"$_n\" = %s ] && continue\n", qSubPath)
			fmt.Fprintf(&b, "    if [ ! -e %s/\"$_n\" ] && [ ! -L %s/\"$_n\" ]; then ln -sf \"$_d\" %s/\"$_n\"; fi\n", qGuest, qGuest, qGuest)
			fmt.Fprintf(&b, "  done\n")
			fmt.Fprintf(&b, "  rm -rf %s && mkdir -p %s\n", qFarmGuest, qFarmGuest)
			fmt.Fprintf(&b, "  for _e in %s/*; do\n", qFarmStaging)
			fmt.Fprintf(&b, "    [ -e \"$_e\" ] || [ -L \"$_e\" ] || continue\n")
			fmt.Fprintf(&b, "    _n=$(basename \"$_e\")\n")
			fmt.Fprintf(&b, "    if PATH='%s' command -v \"$_n\" >/dev/null 2>&1; then\n", GuestNativePATH)
			fmt.Fprintf(&b, "      printf 'shadowed %%s\\n' \"$_n\" >> %s\n", qReport)
			fmt.Fprintf(&b, "    elif [ ! -e \"$_e\" ]; then\n")
			fmt.Fprintf(&b, "      printf 'dangling %%s\\n' \"$_n\" >> %s\n", qReport)
			fmt.Fprintf(&b, "    else\n")
			fmt.Fprintf(&b, "      ln -sf \"$_e\" %s/\"$_n\"\n", qFarmGuest)
			fmt.Fprintf(&b, "      printf 'linked %%s\\n' \"$_n\" >> %s\n", qReport)
			fmt.Fprintf(&b, "    fi\n")
			fmt.Fprintf(&b, "  done\n")
			fmt.Fprintf(&b, "fi\n\n")
		} else {
			qStaging := shSingleQuote(m.StagingGuestPath)
			qGuest := shSingleQuote(m.GuestPath)
			fmt.Fprintf(&b, "# 4. Curated symlink farm: %s\n", m.GuestPath)
			fmt.Fprintf(&b, "if [ -d %s ]; then\n", qStaging)
			fmt.Fprintf(&b, "  rm -rf %s && mkdir -p %s\n", qGuest, qGuest)
			fmt.Fprintf(&b, "  for _e in %s/*; do\n", qStaging)
			fmt.Fprintf(&b, "    [ -e \"$_e\" ] || [ -L \"$_e\" ] || continue\n")
			fmt.Fprintf(&b, "    _n=$(basename \"$_e\")\n")
			fmt.Fprintf(&b, "    if PATH='%s' command -v \"$_n\" >/dev/null 2>&1; then\n", GuestNativePATH)
			fmt.Fprintf(&b, "      printf 'shadowed %%s\\n' \"$_n\" >> %s\n", qReport)
			fmt.Fprintf(&b, "    elif [ ! -e \"$_e\" ]; then\n")
			fmt.Fprintf(&b, "      printf 'dangling %%s\\n' \"$_n\" >> %s\n", qReport)
			fmt.Fprintf(&b, "    else\n")
			fmt.Fprintf(&b, "      ln -sf \"$_e\" %s/\"$_n\"\n", qGuest)
			fmt.Fprintf(&b, "      printf 'linked %%s\\n' \"$_n\" >> %s\n", qReport)
			fmt.Fprintf(&b, "    fi\n")
			fmt.Fprintf(&b, "  done\n")
			fmt.Fprintf(&b, "fi\n\n")
		}
	}

	return b.String()
}
