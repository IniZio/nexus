package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

func isolationNotice(i driver.Isolation) string {
	if i == driver.IsolationGuest {
		return "isolation: guest (whole sprite is the boundary; credentials projected into it are agent-visible)"
	}
	return "isolation: " + string(i)
}

// SpritesGuestArgv is the guest argv a herdr pane runs through `nexus shell`
// to get an interactive TTY shell in the sprite's clone dir.
func SpritesGuestArgv() []string {
	return append([]string(nil), sprites.ShellArgv...)
}

var newSpritesDriver = func() (driver.Driver, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return registry.New(registry.Sprites, sprites.Config{StateDir: root, EnvResolver: spritesEnvResolver, Broker: spritesBrokerManager(root)})
}

// spritesBrokerManager builds the production broker manager. The executable is
// resolved at spawn time. First start uploads the ~8 MB agent over Fly, so the
// ready wait is 60s. The broker process itself never gets one (cmd_sprites_broker.go).
func spritesBrokerManager(root string) *broker.Manager {
	return &broker.Manager{StateDir: root, Launcher: broker.ExecLauncher{StateDir: root}, ReadyTimeout: 60 * time.Second}
}

// spritesOriginURL returns dir's origin remote URL, or "" when there is none.
var spritesOriginURL = func(ctx context.Context, dir string) string {
	b, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// spritesGitHubBinding is the owner/name GH_TOKEN is bound to: the explicit
// repo, else (when no repo was given) the origin of the worktree dir.
func spritesGitHubBinding(ctx context.Context, repo, dir string) string {
	if repo != "" {
		return githubOwnerName(repo)
	}
	return githubOwnerName(spritesOriginURL(ctx, dir))
}

const spritesNoGitHubBindingNotice = "note: GH_TOKEN not brokered: no GitHub owner/name (give --repo owner/name or run from a worktree with a github.com origin)."

// backendDriverFactory builds drivers for sandboxes recorded under a backend
// other than the process default.
func backendDriverFactory(backend string) (driver.Driver, error) {
	if backend == registry.Sprites {
		return newSpritesDriver()
	}
	return nil, fmt.Errorf("backend %q: %w", backend, registry.ErrNoBackend)
}

func spritesEgress(cfg config.Config, f sandboxCreateFlags) (hosts []string, open bool) {
	seen := map[string]bool{}
	add := func(hs ...string) {
		for _, h := range hs {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" && !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	add(f.allowHosts...)
	add(cfg.Egress.Allow...)
	for _, p := range cfg.Egress.Policy {
		add(p.Host)
	}
	for _, sec := range cfg.Egress.Secrets {
		add(sec.Hosts...)
	}
	for _, m := range cfg.Egress.MCP {
		add(m.Host)
	}
	sort.Strings(hosts)
	open = f.egressExplicit && !f.egressClosed
	return hosts, open
}

// spritesRepo returns the clone URL: a full URL as given, owner/name as a GitHub https URL.
func spritesRepo(f sandboxCreateFlags) (string, error) {
	switch {
	case f.repoURL != "":
		return f.repoURL, nil
	case f.allowedRepo != "":
		return "https://github.com/" + strings.TrimSuffix(f.allowedRepo, ".git") + ".git", nil
	}
	return "", fmt.Errorf("--repo <git-url|owner/name> is required")
}

func spritesRepoSuffix(repo string) string {
	if repo == "" {
		return ""
	}
	return " (repo " + repo + ")"
}

func runSpritesCreate(ctx context.Context, f sandboxCreateFlags, out *Output, svc *service.Service) error {
	const verb = "sandbox create"
	if len(f.positionals) != 1 {
		return &UsageError{Msg: "sandbox create: usage: sandbox create <project>/<name> [--repo <git-url|owner/name>] [--allow-host <host>] [--preset docker] [--sync bundle|push] [--egress open|closed] [--label KEY=VALUE] [--rm]"}
	}
	project, name, err := domain.ParseHandle(f.positionals[0])
	if err != nil {
		return &UsageError{Code: sandboxErrCodeInvalidArgument, Msg: fmt.Sprintf("%s: %v", verb, err)}
	}
	for flag, set := range map[string]bool{
		"--image": f.imageRef != "", "--rootfs": f.rootfsPath != "", "--file": f.filePath != "",
		"--dockerfile": f.dockerfilePath != "", "--mount": len(f.mountLive) > 0,
		"--mount-named": len(f.mountNamed) > 0, "--memory": f.memoryMiB > 0, "--vcpus": f.vcpus > 0,
		"--nested-virt": f.nestedVirt, "--workspace": f.workspacePath != "",
	} {
		if set {
			return errSandbox(verb, registry.Unsupported(registry.Sprites, flag))
		}
	}
	presets, err := sprites.NormalizePresets(f.presets)
	if err != nil {
		return &UsageError{Code: sandboxErrCodeInvalidArgument, Msg: fmt.Sprintf("%s: --preset: %v", verb, err)}
	}

	syncMode, err := sprites.NormalizeSyncMode(f.syncMode)
	if err != nil {
		return &UsageError{Code: sandboxErrCodeInvalidArgument, Msg: fmt.Sprintf("%s: --sync: %v", verb, err)}
	}

	repo, rerr := spritesRepo(f)
	if rerr != nil && syncMode == sprites.SyncPush {
		return &UsageError{Code: sandboxErrCodeInvalidArgument, Msg: fmt.Sprintf("%s: --sync push: %v", verb, rerr)}
	}

	var cfg config.Config
	if cwd, werr := os.Getwd(); werr == nil {
		if cfg, _, err = config.Load(cwd); err != nil {
			return errSandbox(verb, err)
		}
	}
	hosts, open := spritesEgress(cfg, f)
	secretNames, err := spritesSecretNames(cfg, f)
	if err != nil {
		return errSandbox(verb, err)
	}
	if slices.Contains(secretNames, sprites.SecretClaudeOAuth) {
		hosts = append(hosts, cred.MustProfileByName(cred.ClaudeCodeProfileName).EgressHosts...)
	}

	// Broker mode: the host token never goes into the spec or the sprite; the
	// broker resolves it. GH_TOKEN is brokered only when bound to a repo.
	var ghRepo string
	cwd, _ := os.Getwd()
	if syncMode == sprites.SyncPush {
		if secretNames, err = sprites.NormalizeSecretNames(append(secretNames, sprites.SecretGitHub)); err != nil {
			return errSandbox(verb, err)
		}
	}
	if slices.Contains(secretNames, sprites.SecretGitHub) {
		if ghRepo = spritesGitHubBinding(ctx, repo, cwd); ghRepo == "" {
			secretNames = slices.DeleteFunc(secretNames, func(n string) bool { return n == sprites.SecretGitHub })
			fmt.Fprintln(out.Stderr(), spritesNoGitHubBindingNotice)
		}
	}

	drv, err := newSpritesDriver()
	if err != nil {
		return errSandbox(verb, err)
	}
	prov, ok := drv.(interface {
		Provision(context.Context, domain.SandboxID, sprites.Spec) error
	})
	if !ok {
		return errSandbox(verb, registry.Unsupported(registry.Sprites, "provision"))
	}

	sb, err := svc.Create(ctx, project, name, service.CreateOptions{Labels: f.labels, RemoveOnExit: f.rm, Backend: registry.Sprites})
	if err != nil {
		return errSandbox(verb, err)
	}
	rollback := func(cause error) error {
		if errors.Is(cause, sprites.ErrSpriteExists) {
			// Pre-existing sprite: drop only the local record, never the remote sprite.
			_ = svc.ForgetRecord(context.WithoutCancel(ctx), sb.ID.String())
		} else {
			_ = svc.Remove(context.WithoutCancel(ctx), sb.ID.String())
		}
		return errSandbox(verb, cause)
	}
	spec := sprites.Spec{Repo: repo, AllowedHosts: hosts, OpenEgress: open, SecretNames: secretNames, Presets: presets, Sync: syncMode, CredMode: sprites.CredModeBroker, GitHubRepo: ghRepo}
	if err := prov.Provision(ctx, sb.ID, spec); err != nil {
		return rollback(err)
	}
	if sb, err = svc.Start(ctx, sb.ID.String()); err != nil {
		return rollback(err)
	}
	out.EmitSuccess("sandbox.created", toSandboxInfoJSON(sb),
		fmt.Sprintf("created sandbox %s (%s) on sprite%s", sb.Handle(), sb.ID, spritesRepoSuffix(repo)))
	fmt.Fprintln(out.Stderr(), isolationNotice(drv.Capabilities().Isolation))
	fmt.Fprintln(out.Stderr(), sprites.CredentialsNotice)
	if slices.Contains(secretNames, sprites.SecretGitHub) {
		fmt.Fprintln(out.Stderr(), spritesGitHubTTLNotice)
	}
	return nil
}
