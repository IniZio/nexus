package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// Guest-side locations owned by nexus.
const (
	GuestAgentPath = "/usr/local/lib/nexus/nexus-agent"
	GuestCAPath    = "/usr/local/lib/nexus/broker-ca.pem"
	GuestPidfile   = "/tmp/nexus-sprite-relay.pid"
	GuestProxyAddr = "127.0.0.1:3128"
)

// ExecRequest is one command run inside the sprite. It mirrors the sprites
// driver's request without importing it (the driver imports this package).
type ExecRequest struct {
	Argv   []string
	Env    map[string]string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// ExecFunc runs to completion and returns the remote exit code.
type ExecFunc func(ctx context.Context, sprite string, req ExecRequest) (int32, error)

// Sprites exec as an unprivileged user with passwordless sudo: the trust-store
// refresh must run privileged or update-ca-certificates silently skips /etc/ssl/certs.
const trustScript = `S=; [ "$(id -u)" = 0 ] || S="sudo -n"
if command -v update-ca-certificates >/dev/null 2>&1; then
  $S mkdir -p /usr/local/share/ca-certificates && $S cp "$1" /usr/local/share/ca-certificates/nexus-broker.crt && $S update-ca-certificates
elif command -v update-ca-trust >/dev/null 2>&1; then
  $S mkdir -p /etc/pki/ca-trust/source/anchors && $S cp "$1" /etc/pki/ca-trust/source/anchors/nexus-broker.crt && $S update-ca-trust
else
  echo "neither update-ca-certificates nor update-ca-trust found: cannot trust the broker CA (git ignores NODE_EXTRA_CA_CERTS)" >&2
  exit 1
fi`

func hexSHA(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func runSh(ctx context.Context, ex ExecFunc, sprite string, stdin io.Reader, script string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	argv := append([]string{"sh", "-c", script, "sh"}, args...)
	code, err := ex(ctx, sprite, ExecRequest{Argv: argv, Stdin: stdin, Stdout: &out, Stderr: &errb})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return out.String(), fmt.Errorf("exit %d: %s", code, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// uploadIfChanged writes data to path unless the guest copy already matches
// its sha256, and verifies the hash after an upload.
func uploadIfChanged(ctx context.Context, ex ExecFunc, sprite, path, mode string, data []byte) error {
	want := hexSHA(data)
	remote := func() string {
		out, err := runSh(ctx, ex, sprite, nil, `sha256sum -- "$1" 2>/dev/null`, path)
		if err != nil {
			return ""
		}
		f := strings.Fields(out)
		if len(f) == 0 {
			return ""
		}
		return f[0]
	}
	if remote() == want {
		return nil
	}
	if _, err := runSh(ctx, ex, sprite, bytes.NewReader(data),
		`mkdir -p -- "$(dirname -- "$2")" && cat > "$2.new" && chmod "$1" "$2.new" && mv -f -- "$2.new" "$2"`, mode, path); err != nil {
		return fmt.Errorf("broker: upload %s: %w", path, err)
	}
	if got := remote(); got != want {
		return fmt.Errorf("broker: upload %s: sha256 mismatch after upload (got %q, want %s)", path, got, want)
	}
	return nil
}

// Install uploads the relay agent (sha-checked) and the broker CA, installs
// the CA into the guest trust store and kills any stale relay. Only the agent
// binary and the public CA certificate are ever uploaded.
func Install(ctx context.Context, ex ExecFunc, sprite string, agent, caPEM []byte) error {
	if len(agent) == 0 || len(caPEM) == 0 {
		return fmt.Errorf("broker: install needs agent and CA bytes")
	}
	if err := uploadIfChanged(ctx, ex, sprite, GuestAgentPath, "755", agent); err != nil {
		return err
	}
	if err := uploadIfChanged(ctx, ex, sprite, GuestCAPath, "644", caPEM); err != nil {
		return err
	}
	if _, err := runSh(ctx, ex, sprite, nil, trustScript, GuestCAPath); err != nil {
		return fmt.Errorf("broker: install CA into guest trust: %w", err)
	}
	return KillStaleRelay(ctx, ex, sprite)
}

// KillStaleRelay kills the relay recorded in the guest pidfile, if any.
func KillStaleRelay(ctx context.Context, ex ExecFunc, sprite string) error {
	_, err := runSh(ctx, ex, sprite, nil,
		`if [ -f "$1" ]; then kill "$(cat "$1")" 2>/dev/null; rm -f "$1"; fi; true`, GuestPidfile)
	if err != nil {
		return fmt.Errorf("broker: kill stale relay: %w", err)
	}
	return nil
}
