package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// BuiltinGitHubEnv is the guest env var that receives the GitHub placeholder.
// gh and git HTTPS both honour it; the MITM swaps the placeholder host-side.
const BuiltinGitHubEnv = "GH_TOKEN"

// GitHubSecretHosts is the host set covered by the builtin gh bind.
// github.com is git HTTPS; api.github.com is the gh CLI. One placeholder
// (minted for github.com) is emitted as GH_TOKEN/GITHUB_TOKEN; ResolveScoped
// keys on placeholder+sandbox, so api.github.com requests still swap.
var GitHubSecretHosts = []string{"github.com", "api.github.com"}

// SecretBind is one host-side secret bound to a guest env var and a set of
// egress hosts. The guest sees only a placeholder; the real token stays in
// the broker (D-PD-23).
type SecretBind struct {
	// Env is the guest environment variable that receives the placeholder
	// (e.g. "GH_TOKEN"). Required.
	Env string
	// Hosts are the hostnames this secret may be swapped for. Required.
	Hosts []string
	// Token is the host-side real credential. Never written to the guest.
	Token string
}

// ParseSecretSpec parses one `--secret ENV@host[,host…]` argument.
func ParseSecretSpec(spec string) (SecretBind, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return SecretBind{}, fmt.Errorf("secret: empty spec")
	}
	env, hostsRaw, ok := strings.Cut(spec, "@")
	if !ok || env == "" || hostsRaw == "" {
		return SecretBind{}, fmt.Errorf("secret: %q: want ENV@host[,host…]", spec)
	}
	env = strings.TrimSpace(env)
	if strings.ContainsAny(env, "= \t") {
		return SecretBind{}, fmt.Errorf("secret: %q: env name must be a single identifier", spec)
	}
	var hosts []string
	for _, h := range strings.Split(hostsRaw, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			return SecretBind{}, fmt.Errorf("secret: %q: empty host", spec)
		}
		hosts = append(hosts, h)
	}
	return SecretBind{Env: env, Hosts: hosts}, nil
}

// LookupGitHubToken returns the host's `gh auth token`. Empty token + nil
// error means gh is missing or not logged in — callers treat that as "no builtin".
var lookupGitHubToken = lookupGitHubTokenImpl

func lookupGitHubTokenImpl(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", nil
	}
	tok := strings.TrimSpace(stdout.String())
	if tok == "" {
		return "", nil
	}
	return tok, nil
}

// BuiltinGitHubSecret binds GH_TOKEN to GitHubSecretHosts using the host
// `gh auth token`. ok is false when gh is absent or not logged in.
func BuiltinGitHubSecret(ctx context.Context) (bind SecretBind, ok bool, err error) {
	tok, err := lookupGitHubToken(ctx)
	if err != nil {
		return SecretBind{}, false, err
	}
	if tok == "" {
		return SecretBind{}, false, nil
	}
	return SecretBind{
		Env:   BuiltinGitHubEnv,
		Hosts: append([]string(nil), GitHubSecretHosts...),
		Token: tok,
	}, true, nil
}

// SecretTouchesGitHub reports whether any host in the bind is a GitHub host.
func SecretTouchesGitHub(b SecretBind) bool {
	for _, h := range b.Hosts {
		if isGitHubHost(h) {
			return true
		}
	}
	return false
}

// ErrAgentGitHubSecret is returned when an agent create path is asked to
// bind a GitHub secret (D-PD-22 / D-PD-23 / D-PD-25).
var ErrAgentGitHubSecret = fmt.Errorf("service: agent sandbox must not bind a GitHub secret")

// MergeSecrets appends extra binds that do not collide on Env. An explicit
// bind for the same env wins over a builtin.
func MergeSecrets(explicit []SecretBind, extra ...SecretBind) []SecretBind {
	seen := make(map[string]struct{}, len(explicit)+len(extra))
	out := make([]SecretBind, 0, len(explicit)+len(extra))
	for _, b := range explicit {
		if b.Env == "" {
			continue
		}
		seen[b.Env] = struct{}{}
		out = append(out, b)
	}
	for _, b := range extra {
		if b.Env == "" {
			continue
		}
		if _, ok := seen[b.Env]; ok {
			continue
		}
		seen[b.Env] = struct{}{}
		out = append(out, b)
	}
	return out
}

// ResolveEnvelopeSecrets rebuilds SecretBinds from frozen ENV@hosts specs.
// Tokens are re-resolved at call time: GH_TOKEN from host `gh auth token`,
// every other env from the process environment. The store never holds tokens.
func ResolveEnvelopeSecrets(ctx context.Context, specs []string) ([]SecretBind, error) {
	var binds []SecretBind
	for _, spec := range specs {
		b, err := ParseSecretSpec(spec)
		if err != nil {
			return nil, err
		}
		if b.Env == BuiltinGitHubEnv || b.Env == "GITHUB_TOKEN" {
			tok, lerr := lookupGitHubToken(ctx)
			if lerr != nil {
				return nil, lerr
			}
			b.Token = tok
		} else if b.Token == "" {
			b.Token = strings.TrimSpace(os.Getenv(b.Env))
		}
		if b.Token == "" {
			continue
		}
		binds = append(binds, b)
	}
	return binds, nil
}

// SeedGuestSecrets re-resolves frozen ENV@hosts specs, mints placeholders
// into broker, and appends KEY=VALUE lines to the guest cred.env. Tokens
// never leave the supervisor process.
func SeedGuestSecrets(ctx context.Context, broker *cred.Broker, id domain.SandboxID, specs []string, seeder GuestSeeder) error {
	if broker == nil || len(specs) == 0 {
		return nil
	}
	binds, err := ResolveEnvelopeSecrets(ctx, specs)
	if err != nil {
		return err
	}
	extra, _, err := applySecrets(broker, id, binds)
	if err != nil {
		return err
	}
	if seeder == nil || len(extra) == 0 {
		return nil
	}
	return seeder(ctx, id, extra)
}



// applySecrets mints placeholders for each bind, wires real tokens into the
// broker, and returns the extra KEY=VALUE lines to append to cred.env.
// The first host of each bind owns the placeholder emitted as Env (and, for
// GH_TOKEN, also as GITHUB_TOKEN).
func applySecrets(broker *cred.Broker, id domain.SandboxID, binds []SecretBind) (extra []byte, hosts []string, err error) {
	if broker == nil || len(binds) == 0 {
		return nil, nil, nil
	}
	var buf bytes.Buffer
	seenHost := make(map[string]struct{})
	for _, b := range binds {
		if b.Env == "" || len(b.Hosts) == 0 {
			return nil, nil, fmt.Errorf("secret: %q: env and at least one host required", b.Env)
		}
		var primary string
		for i, h := range b.Hosts {
			rec, regErr := broker.RegisterPlaceholder(id, h, b.Token)
			if regErr != nil {
				return nil, nil, fmt.Errorf("secret: register %s@%s: %w", b.Env, h, regErr)
			}
			if i == 0 {
				primary = rec.Placeholder
			}
			if _, ok := seenHost[h]; !ok {
				seenHost[h] = struct{}{}
				hosts = append(hosts, h)
			}
		}
		fmt.Fprintf(&buf, "%s=%s\n", b.Env, primary)
		if b.Env == BuiltinGitHubEnv {
			fmt.Fprintf(&buf, "GITHUB_TOKEN=%s\n", primary)
		}
	}
	return buf.Bytes(), hosts, nil
}
