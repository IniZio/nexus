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

// DepsFactory builds runtime Deps; a non-nil Handler is registered as the /link command handler.
type DepsFactory func(cfg *controllerconfig.Config, appToken, botToken string, v vault.Vault) (Deps, Handler, error)

type serveState struct {
	vaultOpener  func() (vault.Vault, error)
	factory      DepsFactory
	clock        func() time.Time
	tickInterval *time.Duration
}

// ServeOpt customises Serve; use the With* functions.
type ServeOpt func(*serveState)

// WithDepsFactory injects a custom dependency builder.
func WithDepsFactory(f DepsFactory) ServeOpt {
	return func(s *serveState) { s.factory = f }
}

// WithVaultOpener injects a custom vault opener.
func WithVaultOpener(f func() (vault.Vault, error)) ServeOpt {
	return func(s *serveState) { s.vaultOpener = f }
}

func WithServeClock(f func() time.Time) ServeOpt {
	return func(s *serveState) { s.clock = f }
}

func WithServeTickInterval(d time.Duration) ServeOpt {
	return func(s *serveState) { s.tickInterval = &d }
}

// Serve loads config, wires adapters, and runs the controller until ctx is cancelled.
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

	cfg, err := controllerconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	if len(cfg.Channels) == 0 {
		return fmt.Errorf("serve: config: at least one channel must be configured")
	}

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

	v, err := ss.vaultOpener()
	if err != nil {
		return fmt.Errorf("serve: vault: %w", err)
	}

	deps, linkHandler, err := ss.factory(cfg, appToken, botToken, v)
	if err != nil {
		return fmt.Errorf("serve: build deps: %w", err)
	}
	if deps.IdleFor == nil {
		deps.IdleFor = IdleForFromConfig(cfg)
	}
	if deps.PermMode == nil {
		deps.PermMode = PermModeFromConfig(cfg)
	}
	if deps.Model == nil {
		deps.Model = ModelFromConfig(cfg)
	}

	var routerOpts []RouterOption
	if linkHandler != nil {
		routerOpts = append(routerOpts, WithCommandHandler(linkHandler))
	}
	if rErr := deps.Store.ResetStuck(ctx); rErr != nil {
		slog.Warn("controller: reset stuck tasks on startup", "err", rErr)
	} else {
		slog.Info("controller: stuck tasks reset to failed")
	}

	ctrl := New(deps)
	router := NewRouter(deps.Store, ctrl, routerOpts...)
	defer router.Close()

	idlePause := SmallestPause(cfg)
	tickInterval := TickIntervalFor(idlePause)
	if ss.tickInterval != nil {
		tickInterval = *ss.tickInterval
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

	slog.Info("nexus-controller: ready", "channels", len(cfg.Channels))
	return deps.Chat.Run(ctx, router.Handle)
}

// PermModeFromConfig returns the permission_mode for a channel from cfg, or ""
// when the channel is not configured or has no permission_mode set.
func PermModeFromConfig(cfg *controllerconfig.Config) func(string) string {
	return func(channel string) string {
		ch, ok := cfg.Channels[channel]
		if !ok {
			return ""
		}
		return ch.PermissionMode
	}
}

// ModelFromConfig returns the model for a channel from cfg, or "" when the
// channel is not configured or has no model set (callers treat "" as "use backend default").
func ModelFromConfig(cfg *controllerconfig.Config) func(string) string {
	return func(channel string) string {
		ch, ok := cfg.Channels[channel]
		if !ok {
			return ""
		}
		return ch.Model
	}
}

// IdleForFromConfig maps channel name to IdleThresholds, falling back to DefaultIdle.
func IdleForFromConfig(cfg *controllerconfig.Config) func(string) IdleThresholds {
	return func(channel string) IdleThresholds {
		ch, ok := cfg.Channels[channel]
		if !ok {
			return DefaultIdle
		}
		t := IdleThresholds{Pause: ch.IdlePause, Stop: ch.IdleStop}
		if t.Pause <= 0 {
			t.Pause = DefaultIdle.Pause
		}
		if t.Stop <= 0 {
			t.Stop = DefaultIdle.Stop
		}
		return t
	}
}

// SmallestPause returns the smallest effective idle_pause across all configured channels.
func SmallestPause(cfg *controllerconfig.Config) time.Duration {
	pause := DefaultIdle.Pause
	for _, ch := range cfg.Channels {
		effective := ch.IdlePause
		if effective <= 0 {
			effective = DefaultIdle.Pause
		}
		if effective < pause {
			pause = effective
		}
	}
	return pause
}

// TickIntervalFor returns half minPause, clamped to [1s, 30m].
func TickIntervalFor(minPause time.Duration) time.Duration {
	d := minPause / 2
	switch {
	case d < time.Second:
		return time.Second
	case d > 30*time.Minute:
		return 30 * time.Minute
	default:
		return d
	}
}

// ControllerStateDir returns the XDG state directory for controller SQLite data.
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
