package herdrworktree

import (
	"fmt"
	"os"
	"strings"

	"github.com/IniZio/nexus/internal/core/config"
	_ "github.com/IniZio/nexus/internal/core/driver/backends"
	"github.com/IniZio/nexus/internal/core/driver/registry"
)

// ResolveBackend picks the backend: arg, then the repo's .nexus/config.yaml
// backend key, then NEXUS_BACKEND. "" means the default.
func ResolveBackend(arg, repoPath string) (string, error) {
	name := norm(arg)
	if name == "" {
		cfg, _, err := config.Load(repoPath)
		if err != nil {
			return "", err
		}
		name = norm(cfg.Backend)
	}
	if name == "" {
		name = norm(os.Getenv("NEXUS_BACKEND"))
	}
	if name != "" && !registry.Registered(name) {
		return "", fmt.Errorf("unknown backend %q (known: %s)", name, strings.Join(registry.Names(), ", "))
	}
	return name, nil
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
