package relay

import (
	"strings"

	"github.com/inizio/nexus/packages/nexus/internal/creds/agentprofile"
)

// RelayEnv builds an environment variable map for the given auth binding.
func RelayEnv(binding, value string) map[string]string {
	out := map[string]string{
		"NEXUS_AUTH_BINDING": binding,
		"NEXUS_AUTH_VALUE":   value,
	}
	p := agentprofile.Lookup(binding)
	if p == nil {
		return out
	}
	if p.APIKeyPrefix != "" && !strings.HasPrefix(strings.TrimSpace(value), p.APIKeyPrefix) {
		return out
	}
	for _, k := range p.EnvVars {
		out[k] = value
	}
	return out
}
