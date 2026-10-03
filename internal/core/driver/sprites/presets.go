package sprites

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/IniZio/nexus/internal/core/domain"
)

// PresetDocker installs docker.io and runs dockerd as a sprite service. Opt-in:
// without it nothing docker-related is installed.
const PresetDocker = "docker"

// ErrUnknownPreset marks a --preset value this backend does not provide.
var ErrUnknownPreset = errors.New("sprites: unknown preset")

// DockerRegistryHosts stay allowed after install so containers can pull images.
// https://docs.docker.com/desktop/setup/allow-list/
var DockerRegistryHosts = []string{"registry-1.docker.io", "auth.docker.io", "production.cloudflare.docker.com", "production.cloudfront.docker.com"}

// DockerAptHosts serve the apt install and are open only during the install window.
// https://documentation.ubuntu.com/server/explanation/software/package-management/
var DockerAptHosts = []string{"archive.ubuntu.com", "security.ubuntu.com"}

const dockerInstallScript = `set -e
export DEBIAN_FRONTEND=noninteractive
sudo -n apt-get update -qq
sudo -n apt-get install -y -qq docker.io docker-buildx
sudo -n "$(command -v sprite-env)" services create dockerd --cmd sudo --args -n,dockerd,-G,sprite >/dev/null
for i in $(seq 1 60); do docker info >/dev/null 2>&1 && exit 0; sleep 1; done
echo "dockerd did not become ready" >&2
sudo -n "$(command -v sprite-env)" services list >&2 || true
docker info >&2 || true
exit 1`

// NormalizePresets validates, lowercases, de-duplicates and sorts presets.
func NormalizePresets(in []string) ([]string, error) {
	var out []string
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != PresetDocker {
			return nil, fmt.Errorf("%w %q (known: %s)", ErrUnknownPreset, p, PresetDocker)
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

// installDocker opens the apt window, installs docker and starts dockerd, then
// restores the policy on every path. A failed restore is an error (fail closed).
func (d *Driver) installDocker(ctx context.Context, id domain.SandboxID, s Spec) (err error) {
	if s.OpenEgress {
		return d.runDockerInstall(ctx, id)
	}
	name := SpriteName(id)
	base := append(slices.Clone(s.AllowedHosts), GoToolchainHosts...)
	if serr := d.api.SetNetworkPolicy(ctx, name, BuildPolicy(append(slices.Clone(base), DockerAptHosts...), s.IncludeDefaults, false)); serr != nil {
		return fmt.Errorf("sprites docker: open apt egress window: %w", serr)
	}
	defer func() {
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tightenTimeout)
		defer cancel()
		if terr := d.api.SetNetworkPolicy(tctx, name, BuildPolicy(base, s.IncludeDefaults, false)); terr != nil {
			err = errors.Join(err, fmt.Errorf("sprites docker: tighten egress (apt hosts may still be open): %w", terr))
		}
	}()
	return d.runDockerInstall(ctx, id)
}

func (d *Driver) runDockerInstall(ctx context.Context, id domain.SandboxID) error {
	if err := d.guestRun(ctx, id, ExecRequest{Argv: []string{"bash", "-c", dockerInstallScript}}); err != nil {
		return fmt.Errorf("sprites docker: install: %w", err)
	}
	return nil
}
