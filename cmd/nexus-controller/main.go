package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/IniZio/nexus/internal/controller"
	herdrbackend "github.com/IniZio/nexus/internal/controller/backend/herdr"
	slackadapter "github.com/IniZio/nexus/internal/controller/chat/slack"
	controllerconfig "github.com/IniZio/nexus/internal/controller/config"
	"github.com/IniZio/nexus/internal/controller/sandbox"
	"github.com/IniZio/nexus/internal/controller/store/sqlite"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vaulthost"
)

// slackChat is the subset of *slackadapter.Adapter needed by realDepsFactory.
type slackChat interface {
	controller.ChatAdapter
	TeamID() string
}

// Package-level vars are the seams that let tests replace network-bound or
// substrate-bound constructors without touching production call paths.
var (
	// newSlackAdapter constructs the Slack Socket Mode adapter; tests replace
	// this to avoid the auth.test network call.
	newSlackAdapter = func(appToken, botToken string) (slackChat, error) {
		return slackadapter.New(appToken, botToken)
	}

	// newSandboxLifecycle returns a CLI-based lifecycle; tests replace this.
	newSandboxLifecycle = func() controller.SandboxLifecycle {
		return sandbox.NewCLILifecycle(sandbox.CLIConfig{})
	}
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: nexus-controller <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  serve   start the controller with real adapters")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags:")
	fmt.Fprintln(os.Stderr, "  -h, --help   show this help")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "nexus-controller:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "nexus-controller: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "path to controller config file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return fmt.Errorf("--config is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return controller.Serve(ctx, *configPath, controller.WithDepsFactory(realDepsFactory))
}

// realDepsFactory builds production dependencies from a validated config and
// an open vault.
func realDepsFactory(cfg *controllerconfig.Config, appToken, botToken string, v vault.Vault) (controller.Deps, controller.Handler, error) {
	// Slack Socket Mode adapter — calls auth.test; fails closed on bad tokens.
	chat, err := newSlackAdapter(appToken, botToken)
	if err != nil {
		return controller.Deps{}, nil, fmt.Errorf("slack adapter: %w", err)
	}

	// SQLite task store under $XDG_STATE_HOME/nexus/controller/tasks.db.
	stateDir, err := controller.ControllerStateDir()
	if err != nil {
		return controller.Deps{}, nil, fmt.Errorf("state dir: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return controller.Deps{}, nil, fmt.Errorf("create state dir: %w", err)
	}
	taskStore, err := sqlite.Open(filepath.Join(stateDir, "tasks.db"))
	if err != nil {
		return controller.Deps{}, nil, fmt.Errorf("task store: %w", err)
	}

	// Herdr backend — model defaults to claude-haiku-4-5; socket from env.
	herdrModel := "claude-haiku-4-5"
	if m := os.Getenv("NEXUS_CONTROLLER_MODEL"); m != "" {
		herdrModel = m
	}
	backend := herdrbackend.New(herdrbackend.Config{
		Model:           herdrModel,
		HerdrSocketPath: os.Getenv("HERDR_SOCKET_PATH"),
	})

	svcLifecycle := newSandboxLifecycle()

	// VaultLinker — principal = slack:<team>:<user>.
	reg, err := vaulthost.DefaultConnectorRegistry()
	if err != nil {
		return controller.Deps{}, nil, fmt.Errorf("connector registry: %w", err)
	}
	linker := controller.NewVaultLinker(v, reg, chat, chat.TeamID(), cfg.DeploymentMode)

	// ProjectResolver — static mapping from config.
	projects := controllerconfig.NewResolver(cfg)

	deps := controller.Deps{
		Chat:      chat,
		Store:     taskStore,
		Backend:   backend,
		Lifecycle: svcLifecycle,
		Linker:    linker,
		Projects:  projects,
	}
	return deps, linker.CommandHandler(), nil
}
