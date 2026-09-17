package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/gitssh"
)

func startGitSSHRelay(
	ctx context.Context,
	socketDir string,
	sb domain.Sandbox,
	onEgress func(host, verdict, reason string, ts time.Time),
) {
	// PathPolicies[""] holds generic path policies stored by buildWorktreeEgressArgs.
	var policies []gitssh.HostPolicy
	if generic, ok := sb.Envelope.PathPolicies[""]; ok {
		for host, pol := range generic {
			policies = append(policies, gitssh.HostPolicy{Host: host, Paths: pol.Paths})
		}
	}
	allowlist := gitssh.DeriveAllowlist(policies)
	if len(allowlist) == 0 {
		// Still start: fail-closed behaviour is correct — relay denies all sessions.
		slog.Info("supervisor.gitssh_relay.no_allowlist", "sandboxID", sb.ID,
			"note", "relay will deny all git SSH sessions")
	}

	// Underscore naming (not dot) confirmed by T0b probe for guest-initiated vsock connections.
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
