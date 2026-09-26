package service

import (
	"context"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/vault"
)

var vaultIntegrationHosts = map[string][]string{
	"github": {"github.com", "api.github.com", "uploads.github.com"},
	"linear": {"api.linear.app", "mcp.linear.app"},
}

func buildVaultForceRefreshFns(v vault.Vault, sandboxID domain.SandboxID, principal, project string, broker *cred.Broker) map[string]cred.ForceRefreshFn {
	if v == nil || principal == "" {
		return nil
	}
	fns := make(map[string]cred.ForceRefreshFn)
	for intID, hosts := range vaultIntegrationHosts {
		key := vault.Key{Principal: principal, Integration: intID}
		for _, h := range hosts {
			h, key := h, key
			fns[h] = func(ctx context.Context) (string, error) {
				rec, err := v.ForceRefresh(ctx, key)
				if err != nil {
					return "", err
				}
				_ = broker.SetRealToken(sandboxID, h, rec.AccessToken)
				return rec.AccessToken, nil
			}
		}
	}
	return fns
}
