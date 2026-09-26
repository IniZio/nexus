package controller

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	controllerconfig "github.com/IniZio/nexus/internal/controller/config"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vaulthost"
)

// DepsFactory builds runtime Deps plus an optional /link command Handler from
// a validated Config, resolved Slack tokens, and an open Vault. Returning a
// non-nil Handler registers it as the command handler for the router.
type DepsFactory func(cfg *controllerconfig.Config, appToken, botToken string, v vault.Vault) (Deps, Handler, error)

type serveState struct {
	vaultOpener  func() (vault.Vault, error)
	factory      DepsFactory
	clock        func() time.Time
	tickInterval *time.Duration
}

// ServeOpt customises Serve; use the With* functions.
type ServeOpt func(*serveState)

// WithDepsFactory injects a custom dependency builder. Tests use this to
// substitute fakes and assert that each Deps field is the expected type.
func WithDepsFactory(f DepsFactory) ServeOpt {
	return func(s *serveState) { s.factory = f }
}

// WithVaultOpener injects a custom vault opener. Tests use this to simulate a
// missing vault key without a real keyring.
func WithVaultOpener(f func() (vault.Vault, error)) ServeOpt {
	return func(s *serveState) { s.vaultOpener = f }
}

func WithServeClock(f func() time.Time) ServeOpt {
	return func(s *serveState) { s.clock = f }
}

func WithServeTickInterval(d time.Duration) ServeOpt {
	return func(s *serveState) { s.tickInterval = &d }
}

// Serve loads configuration from cfgPath, wires every adapter, and runs the
// controller until ctx is cancelled. It returns nil on clean shutdown and a
// descriptive error on any fail-closed condition.
//
// Fail-closed conditions (Serve returns non-nil before starting):
//   - config file missing or invalid YAML
//   - slack.app_token or slack.bot_token refs don't resolve
//   - no channels are configured
//   - inline secret present (caught by config.Parse)
//   - vault key unavailable
func Serve(ctx context.Context, cfgPath string, opts ...ServeOpt) error {
	ss := &serveState{
		vaultOpener: func() (vault.Vault, error) { return vaulthost.Open() },
		clock:       time.Now,
	}
	for _, o := range opts {
		o(ss)
	}
	if ss.factory == nil {
		return fmt.Errorf("serve: no DepsFactory configured; pass one via WithDepsFactory")
	}

	// 1. Load and validate config.
	cfg, err := controllerconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	if len(cfg.Channels) == 0 {
		return fmt.Errorf("serve: config: at least one channel must be configured")
	}

	// 2. Resolve Slack tokens — fail closed when env/file is absent.
	appToken, err := cfg.Slack.AppToken.Resolve()
	if err != nil {
		return fmt.Errorf("serve: slack app_token: %w", err)
	}
	if appToken == "" {
		return fmt.Errorf("serve: slack app_token: resolved to empty string")
	}
	botToken, err := cfg.Slack.BotToken.Resolve()
	if err != nil {
		return fmt.Errorf("serve: slack bot_token: %w", err)
	}
	if botToken == "" {
		return fmt.Errorf("serve: slack bot_token: resolved to empty string")
	}

	// 3. Open vault — fail closed when the key is unavailable.
	v, err := ss.vaultOpener()
	if err != nil {
		return fmt.Errorf("serve: vault: %w", err)
	}

	// 4. Build runtime dependencies.
	deps, linkHandler, err := ss.factory(cfg, appToken, botToken, v)
	if err != nil {
		return fmt.Errorf("serve: build deps: %w", err)
	}

	// 5. Wire router with optional /link command handler.
	var routerOpts []RouterOption
	if linkHandler != nil {
		routerOpts = append(routerOpts, WithCommandHandler(linkHandler))
	}
	ctrl := New(deps)
	router := NewRouter(deps.Store, ctrl, routerOpts...)
	defer router.Close()

	idlePause, _ := minIdleDurations(cfg)
	tickInterval := idlePause / 2
	if ss.tickInterval != nil {
		tickInterval = *ss.tickInterval
	} else {
		switch {
		case tickInterval < time.Second:
			tickInterval = time.Second
		case tickInterval > 30*time.Minute:
			tickInterval = 30 * time.Minute
		}
	}

	tickCtx, cancelTick := context.WithCancel(ctx)
	defer cancelTick()
	go func() {
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		for {
			select {
			case <-tickCtx.Done():
				return
			case <-t.C:
				idleBefore := ss.clock().Add(-idlePause)
				if tErr := router.Tick(tickCtx, idleBefore); tErr != nil {
					slog.Warn("controller: idle tick error", "err", tErr)
				}
			}
		}
	}()

	// 7. Run chat adapter — blocks until ctx is cancelled.
	slog.Info("nexus-controller: ready", "channels", len(cfg.Channels))
	return deps.Chat.Run(ctx, router.Handle)
}

// minIdleDurations returns the smallest positive idle_pause and idle_stop
// across all configured channels. Falls back to 30m / 4h when no channel
// has either value set.
func minIdleDurations(cfg *controllerconfig.Config) (pause, stop time.Duration) {
	pause = 30 * time.Minute
	stop = 4 * time.Hour
	for _, ch := range cfg.Channels {
		if ch.IdlePause > 0 && ch.IdlePause < pause {
			pause = ch.IdlePause
		}
		if ch.IdleStop > 0 && ch.IdleStop < stop {
			stop = ch.IdleStop
		}
	}
	return
}

// ControllerStateDir returns the XDG state directory for controller data
// (e.g. the SQLite task store).
func ControllerStateDir() (string, error) {
	xdg := os.Getenv("XDG_STATE_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("controller: resolve home dir: %w", err)
		}
		xdg = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(xdg, "nexus", "controller"), nil
}
