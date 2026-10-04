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
	"time"
)

// Tier A credential projection: values are resolved host-side per exec and
// merged into the exec env only. They are agent-visible; only names persist.
const (
	SecretClaudeOAuth = "CLAUDE_CODE_OAUTH_TOKEN"
	SecretGitHub      = "GH_TOKEN"

	CredentialsNotice = "credentials: tier A (agent-visible env)"
)

// EnvResolver returns env values for the requested secret names.
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
			return nil, fmt.Errorf("sprites: secret kind %s unsupported on tier A (supported: %s)", n, strings.Join(supportedSecrets, ", "))
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (d *Driver) projectedEnv(ctx context.Context, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if d.cfg.EnvResolver == nil {
		return nil, fmt.Errorf("sprites: secrets %s requested but no resolver configured", strings.Join(names, ","))
	}
	return d.cfg.EnvResolver(ctx, names)
}

// mergeEnv layers explicit over projected over base.
func mergeEnv(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, l := range layers {
		for k, v := range l {
			out[k] = v
		}
	}
	return out
}

// CredModeBroker routes credentials through the host-side broker: the guest
// only ever sees placeholders.
const CredModeBroker = "broker"

// brokerEnv builds the broker-mode exec env from the broker's GuestEnv. It
// fails closed: a dead broker that cannot be ensured never falls back to tier A.
func (d *Driver) brokerEnv(ctx context.Context, id domain.SandboxID, spec Spec, caller map[string]string) (map[string]string, error) {
	m := d.cfg.Broker
	if m == nil {
		return nil, errors.New("sprites: broker mode but no broker configured")
	}
	if !m.Alive(id.String()) {
		ectx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := m.Ensure(ectx, id.String()); err != nil || !m.Alive(id.String()) {
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
