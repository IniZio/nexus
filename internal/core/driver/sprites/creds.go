package sprites

import (
	"context"
	"errors"
	"fmt"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
	"slices"
	"sort"
	"strings"
)

// Secret kinds the broker can bind. Only names persist; real values never
// reach the sprite (see the sprites delegation reference).
const (
	SecretClaudeOAuth = "CLAUDE_CODE_OAUTH_TOKEN"
	SecretGitHub      = "GH_TOKEN"

	CredentialsNotice = "credentials: brokered (placeholders in sprite; real tokens stay on host)"
)

// EnvResolver returns host-side real values for the requested secret names; the
// broker process consumes it, the driver never does.
type EnvResolver func(ctx context.Context, names []string) (map[string]string, error)

var supportedSecrets = []string{SecretClaudeOAuth, SecretGitHub}

// NormalizeSecretNames maps aliases, dedups and sorts; an unsupported kind
// fails closed.
func NormalizeSecretNames(names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "GITHUB_TOKEN" {
			n = SecretGitHub
		}
		if !slices.Contains(supportedSecrets, n) {
			return nil, fmt.Errorf("sprites: secret kind %s unsupported (supported: %s)", n, strings.Join(supportedSecrets, ", "))
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// mergeEnv layers later maps over earlier ones.
func mergeEnv(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, l := range layers {
		for k, v := range l {
			out[k] = v
		}
	}
	return out
}

// brokerEnv builds the exec env from the broker's GuestEnv (placeholders and
// proxy vars only). It fails closed: a dead broker that cannot be ensured
// refuses the exec.
func (d *Driver) brokerEnv(ctx context.Context, id domain.SandboxID, spec Spec, caller map[string]string) (map[string]string, error) {
	m := d.cfg.Broker
	if m == nil {
		return nil, errors.New("sprites: broker mode but no broker configured")
	}
	if !m.Alive(id.String()) {
		if err := m.Ensure(ctx, id.String()); err != nil || !m.Alive(id.String()) {
			return nil, fmt.Errorf("sprites: credential broker for %s is not running; refusing to exec: %v", id, err)
		}
	}
	st, err := broker.ReadState(m.StateDir, id.String())
	if err != nil {
		return nil, fmt.Errorf("sprites: read broker state: %w", err)
	}
	env := mergeEnv(caller)
	for _, n := range append(slices.Clone(supportedSecrets), spec.SecretNames...) {
		delete(env, n)
	}
	for k, v := range st.GuestEnv {
		env[k] = v
	}
	return env, nil
}
