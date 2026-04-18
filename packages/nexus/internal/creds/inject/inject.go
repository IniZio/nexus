package inject

import (
	"github.com/inizio/nexus/packages/nexus/internal/creds/relay"
)

// EnvFromBinding builds an env-var map for the given auth binding and secret
// value. It delegates to relay.RelayEnv to resolve agent-specific variable names
// (e.g. ANTHROPIC_API_KEY for the "claude" binding) and always includes the
// canonical NEXUS_AUTH_BINDING / NEXUS_AUTH_VALUE pair.
func EnvFromBinding(binding, value string) map[string]string {
	return relay.RelayEnv(binding, value)
}

// EnvFromBindings merges env maps for all provided bindings. Later entries win
// on key collision, which is consistent with the order they are declared.
func EnvFromBindings(bindings map[string]string) map[string]string {
	out := make(map[string]string)
	for binding, value := range bindings {
		for k, v := range relay.RelayEnv(binding, value) {
			out[k] = v
		}
	}
	return out
}
