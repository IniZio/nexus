package service

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

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
const GuestCACertPath = "/usr/local/share/ca-certificates/nexus3-mitm.crt"

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
