package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

func init() {
	Register(Command{
		Name:    "__sprites-broker",
		Summary: "Run the host-side credential broker for a sprite sandbox (internal)",
		Hidden:  true,
		Run:     runSpritesBrokerCmd,
	})
}

// spritesBrokerDeps are the seams of the broker command; tests inject fakes.
type spritesBrokerDeps struct {
	stateDir string
	loadSpec func(id domain.SandboxID) (sprites.Spec, error)
	resolve  sprites.EnvResolver
	exec     broker.ExecFunc
	run      func(ctx context.Context, cfg broker.RunConfig) error
	logf     func(format string, args ...any)
}

func runSpritesBrokerCmd(ctx context.Context, args []string, _ *Output) error {
	root, err := store.DefaultRoot()
	if err != nil {
		return err
	}
	drv, err := sprites.New(sprites.Config{StateDir: root})
	if err != nil {
		return err
	}
	deps := spritesBrokerDeps{
		stateDir: root,
		loadSpec: drv.Spec,
		resolve:  spritesEnvResolver,
		exec:     drv.BrokerExec(),
		run:      broker.Run,
		logf:     log.New(os.Stderr, "", log.LstdFlags).Printf,
	}
	return runSpritesBroker(ctx, args, deps)
}

// runSpritesBroker validates args, resolves host secrets into the broker's
// CredsConfig (never env/argv) and runs the broker until SIGTERM/SIGINT.
func runSpritesBroker(ctx context.Context, args []string, d spritesBrokerDeps) error {
	if len(args) != 1 {
		return errors.New("usage: nexus __sprites-broker <sandbox-id>")
	}
	id, err := domain.ParseSandboxID(args[0])
	if err != nil {
		return fmt.Errorf("sprites-broker: invalid sandbox id: %w", err)
	}
	spec, err := d.loadSpec(id)
	if err != nil {
		return err
	}
	secrets, err := spritesBrokerSecrets(ctx, spec, d)
	if err != nil {
		return err
	}
	cc := broker.CredsConfig{SandboxID: id, Secrets: secrets, AllowedBranches: spec.AllowedBranches}
	for _, s := range secrets {
		if s.Name == sprites.SecretClaudeOAuth {
			prof := cred.MustProfileByName(cred.ClaudeCodeProfileName)
			cc.RefreshStore = service.DedicatedCredStorePathForProfile(prof)
			cc.RefreshSecret = s.Name
		}
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return d.run(ctx, broker.RunConfig{
		StateDir: d.stateDir,
		Sprite:   sprites.SpriteName(id),
		Creds:    cc,
		Exec:     d.exec,
		Logf:     d.logf,
	})
}

func spritesBrokerSecrets(ctx context.Context, spec sprites.Spec, d spritesBrokerDeps) ([]broker.Secret, error) {
	var secrets []broker.Secret
	var names []string
	repo := spec.GitHubRepo
	if repo == "" {
		repo = githubOwnerName(spec.Repo)
	}
	for _, n := range spec.SecretNames {
		if n == sprites.SecretGitHub && repo == "" {
			if d.logf != nil {
				d.logf("sprites-broker: no GitHub owner/name for repo %q; omitting %s", spec.Repo, n)
			}
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return nil, nil
	}
	vals, err := d.resolve(ctx, names)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		switch n {
		case sprites.SecretGitHub:
			secrets = append(secrets, broker.Secret{Name: n, Value: vals[n], Hosts: []string{"github.com", "api.github.com"}, GitHubRepo: repo})
		case sprites.SecretClaudeOAuth:
			prof := cred.MustProfileByName(cred.ClaudeCodeProfileName)
			secrets = append(secrets, broker.Secret{Name: n, Value: vals[n], Hosts: []string{prof.CredentialedHost}})
		default:
			return nil, fmt.Errorf("sprites-broker: secret kind %s unsupported", n)
		}
	}
	return secrets, nil
}

// githubOwnerName extracts "owner/name" from a github.com URL, scp-style
// remote or bare owner/name; anything else yields "" (fail closed).
func githubOwnerName(repo string) string {
	r := strings.TrimSpace(repo)
	for _, p := range []string{"https://github.com/", "http://github.com/", "ssh://git@github.com/", "git@github.com:", "github.com/"} {
		if strings.HasPrefix(r, p) {
			r = strings.TrimPrefix(r, p)
			goto trim
		}
	}
	if strings.Contains(r, "://") || strings.Contains(r, "@") || strings.Contains(r, ":") {
		return ""
	}
trim:
	r = strings.TrimSuffix(strings.TrimSuffix(r, "/"), ".git")
	owner, name, ok := strings.Cut(r, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return r
}
