package supervisor

import (
	"context"
	"log/slog"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vaulthost"
)

// openSupervisorVaultFn is the production vault opener used by RunDetached.
// Tests override it to inject a fake vault.
var openSupervisorVaultFn = func() (vault.Vault, error) {
	return vaulthost.Open()
}

// resolveGitHubFromVault resolves the GitHub token from the vault for the
// sandbox's principal, registers the placeholder with the broker, and returns
// the cred.env payload (GH_TOKEN=<placeholder>\nGITHUB_TOKEN=<placeholder>\n)
// for seeding the guest. Returns nil on any failure (non-fatal).
func resolveGitHubFromVault(ctx context.Context, v vault.Vault, broker *cred.Broker, sb domain.Sandbox) []byte {
	if v == nil {
		slog.Info("supervisor.vault_github_skip", "reason", "vault_nil")
		return nil
	}
	if sb.Principal == "" {
		slog.Info("supervisor.vault_github_skip", "reason", "no_principal")
		return nil
	}
	bind, ok, err := service.GitHubSecretFromVault(ctx, v, sb.Principal, sb.Project)
	if err != nil {
		slog.Info("supervisor.vault_github_skip", "principal", sb.Principal, "err", err)
		return nil
	}
	if !ok {
		slog.Info("supervisor.vault_github_skip", "principal", sb.Principal, "reason", "no_token")
		return nil
	}
	payload, _, regErr := service.ApplyVaultSecretsToBroker(broker, sb.ID, []service.SecretBind{bind})
	if regErr != nil {
		slog.Warn("supervisor.vault_github_register_failed", "err", regErr)
		return nil
	}
	slog.Info("supervisor.vault_github_placeholder_ready", "principal", sb.Principal, "bytes", len(payload))
	return payload
}

// resolveVaultMCPBindsForBroker calls BuildMCPOAuthBindsFromVault and registers
// each bind with the broker. Returns a serverName→placeholder map for seeding
// the guest credential env (mirrors registerMCPOAuthPlaceholders shape).
func resolveVaultMCPBindsForBroker(ctx context.Context, v vault.Vault, broker *cred.Broker, sandboxID domain.SandboxID, principal, project string) map[string]string {
	if v == nil || principal == "" {
		return nil
	}
	binds, err := service.BuildMCPOAuthBindsFromVault(ctx, v, principal, project, "")
	if err != nil {
		slog.Warn("supervisor.vault_mcp_resolve_failed", "err", err)
		return nil
	}
	seeds := make(map[string]string, len(binds))
	for _, b := range binds {
		if b.Bind.Token == "" || len(b.Bind.Hosts) == 0 {
			continue
		}
		rec, regErr := broker.RegisterPlaceholder(sandboxID, b.Bind.Hosts[0], b.Bind.Token)
		if regErr != nil {
			slog.Warn("supervisor.vault_mcp_placeholder_failed", "host", b.Bind.Hosts[0], "err", regErr)
			continue
		}
		slog.Info("supervisor.vault_mcp_placeholder_registered", "host", b.Bind.Hosts[0], "server", b.ServerName)
		seeds[b.ServerName] = rec.Placeholder
	}
	return seeds
}
