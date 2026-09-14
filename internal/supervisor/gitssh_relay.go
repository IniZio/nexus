package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/gitssh"
)

// startGitSSHRelay starts a per-sandbox git SSH relay goroutine. It accepts
// guest-initiated connections on the CH vsock UDS path and relays authorised
// git-upload-pack / git-receive-pack sessions to the host ssh binary.
//
// Parameters:
//
//	ctx       — supervisor context; relay stops when cancelled
//	socketDir — supervisor cfg.SocketDir (dir for CH API and vsock sockets)
//	sb        — the sandbox record (ID, Envelope.PathPolicies, Envelope.AllowedBranches)
//	onEgress  — shared egress decisions log callback (may be nil)
//
// Returns immediately; the relay runs in a background goroutine.
func startGitSSHRelay(
	ctx context.Context,
	socketDir string,
	sb domain.Sandbox,
	onEgress func(host, verdict, reason string, ts time.Time),
) {
	// Build HostPolicy list from Envelope.PathPolicies[""] (generic path policies,
	// stored under the empty placeholder key by buildWorktreeEgressArgs).
	var policies []gitssh.HostPolicy
	if generic, ok := sb.Envelope.PathPolicies[""]; ok {
		for host, pol := range generic {
			policies = append(policies, gitssh.HostPolicy{Host: host, Paths: pol.Paths})
		}
	}
	allowlist := gitssh.DeriveAllowlist(policies)
	if len(allowlist) == 0 {
		// No SSH-mappable egress entries; relay will deny all git SSH sessions.
		// Still start: fail-closed behaviour is correct here.
		slog.Info("supervisor.gitssh_relay.no_allowlist", "sandboxID", sb.ID,
			"note", "relay will deny all git SSH sessions")
	}

	// Construct the vsock UDS path: <socketDir>/<id>.vsock_<port>
	// (underscore naming confirmed by T0b probe — guest-initiated connections).
	vsockPath := fmt.Sprintf("%s/%s.vsock_%s",
		socketDir, sb.ID.String(), strconv.FormatUint(uint64(driver.GitSSHRelayPort), 10))

	cfg := gitssh.RelayConfig{
		SandboxID:       sb.ID.String(),
		VsockUDSPath:    vsockPath,
		Allowlist:       allowlist,
		AllowedBranches: sb.Envelope.ResolvedAllowedBranches(),
		OnEgress:        onEgress,
		UID:             os.Getuid(),
		SSHAuthSock:     os.Getenv("SSH_AUTH_SOCK"),
	}

	go func() {
		if err := gitssh.RunRelay(ctx, cfg); err != nil && ctx.Err() == nil {
			slog.Error("supervisor.gitssh_relay.error", "sandboxID", sb.ID, "err", err)
		}
	}()

	slog.Info("supervisor.gitssh_relay.started",
		"sandboxID", sb.ID,
		"udsPath", vsockPath,
		"allowlistSize", len(allowlist),
	)
}
