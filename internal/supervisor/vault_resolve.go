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
// sandbox's principal and registers it with the broker. Non-fatal: failures
// are logged and the sandbox continues without a GitHub credential.
func resolveGitHubFromVault(ctx context.Context, v vault.Vault, broker *cred.Broker, sb domain.Sandbox) {
	if v == nil || sb.Principal == "" {
		return
	}
	bind, ok, err := service.GitHubSecretFromVault(ctx, v, sb.Principal, sb.Project)
	if err != nil || !ok {
		if err != nil {
			slog.Info("supervisor.vault_github_skip", "err", err)
		}
		return
	}
	if _, _, regErr := service.ApplyVaultSecretsToBroker(broker, sb.ID, []service.SecretBind{bind}); regErr != nil {
		slog.Warn("supervisor.vault_github_register_failed", "err", regErr)
	}
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
