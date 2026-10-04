package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
)

// spritesSecretNames collects requested secret env names from --secret,
// egress.secrets and the claude-code agent; unsupported kinds fail closed.
func spritesSecretNames(cfg config.Config, f sandboxCreateFlags) ([]string, error) {
	var names []string
	for _, spec := range f.secrets {
		env, _, _ := strings.Cut(spec, "@")
		names = append(names, env)
	}
	for _, s := range cfg.Egress.Secrets {
		names = append(names, s.Env)
	}
	if f.agentName == cred.ClaudeCodeProfileName {
		names = append(names, sprites.SecretClaudeOAuth)
	}
	return sprites.NormalizeSecretNames(names)
}

// spritesEnvResolver resolves real secret values on the host for the broker process.
func spritesEnvResolver(ctx context.Context, names []string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	for _, n := range names {
		var v string
		var err error
		switch n {
		case sprites.SecretClaudeOAuth:
			v, err = hostClaudeOAuthToken(ctx)
		case sprites.SecretGitHub:
			v, err = hostGitHubToken(ctx)
		default:
			err = fmt.Errorf("sprites: secret kind %s unsupported", n)
		}
		if err != nil {
			return nil, err
		}
		out[n] = v
	}
	return out, nil
}

type noopTokenSetter struct{}

func (noopTokenSetter) SetRealToken(domain.SandboxID, string, string) error { return nil }

// hostClaudeOAuthToken vends the dedicated store's access token, refreshing it
// (cross-process locked, persisted to the host store) when stale.
func hostClaudeOAuthToken(ctx context.Context) (string, error) {
	prof := cred.MustProfileByName(cred.ClaudeCodeProfileName)
	r, err := cred.NewRefresher(service.DedicatedCredStorePathForProfile(prof), prof.CredentialedHost, noopTokenSetter{})
	if err != nil {
		return "", fmt.Errorf("sprites: claude credential: %w (run `nexus auth login`)", err)
	}
	tok, _, err := r.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("sprites: claude credential: %w (run `nexus auth login`)", err)
	}
	return tok, nil
}

func hostGitHubToken(ctx context.Context) (string, error) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
	}
	b, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "", errors.New("sprites: GH_TOKEN requested but no host token (set GH_TOKEN or `gh auth login`)")
	}
	return strings.TrimSpace(string(b)), nil
}
