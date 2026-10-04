package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/driver/sprites/tunnel"
	"github.com/IniZio/nexus/internal/core/hostbin"
)

// RunConfig configures [Run].
type RunConfig struct {
	StateDir string
	// Sprite is the sprite name the relay runs in.
	Sprite string
	// Creds holds the real tokens; they flow only into the host-side stack.
	Creds CredsConfig
	Exec  ExecFunc
	// Agent overrides the embedded amd64 nexus-agent (tests).
	Agent []byte
	// RefreshInterval defaults to 1 minute.
	RefreshInterval time.Duration
	// BackoffMin/BackoffMax bound the reconnect delay (defaults 1s/30s).
	BackoffMin, BackoffMax time.Duration
	// Logf receives diagnostics; may be nil.
	Logf func(format string, args ...any)
}

func (c *RunConfig) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

// Run serves the broker until ctx ends or a fatal error occurs. broker.json is
// removed on every exit (fail closed).
func Run(ctx context.Context, cfg RunConfig) error {
	id := cfg.Creds.SandboxID.String()
	dir, err := Dir(cfg.StateDir, id)
	if err != nil {
		return err
	}
	if cfg.Exec == nil || cfg.Sprite == "" {
		return errors.New("broker: Exec and Sprite are required")
	}
	if cfg.Creds.PersistDir == "" {
		cfg.Creds.PersistDir = dir
	}
	creds, err := NewCreds(cfg.Creds)
	if err != nil {
		return err
	}
	agent := cfg.Agent
	if agent == nil {
		if agent, err = hostbin.AgentFor("amd64"); err != nil {
			return err
		}
	}
	bmin, bmax := cfg.BackoffMin, cfg.BackoffMax
	if bmin <= 0 {
		bmin = time.Second
	}
	if bmax < bmin {
		bmax = 30 * time.Second
	}
	defer func() { _ = RemoveState(cfg.StateDir, id) }()

	if err := Install(ctx, cfg.Exec, cfg.Sprite, agent, creds.CACertPEM); err != nil {
		return err
	}
	st, err := newState(cfg.Sprite, creds)
	if err != nil {
		return err
	}

	refreshCtx, stopRefresh := context.WithCancel(ctx)
	defer stopRefresh()
	interval := cfg.RefreshInterval
	if interval <= 0 {
		interval = time.Minute
	}
	go creds.RunRefresh(refreshCtx, interval, func(e error) { cfg.logf("broker: refresh: %v", e) })

	backoff := bmin
	for {
		start := time.Now()
		err := serveOnce(ctx, cfg, creds, st, id)
		if ctx.Err() != nil {
			return nil
		}
		cfg.logf("broker: tunnel ended: %v; reconnecting in %s", err, backoff)
		if time.Since(start) > 10*time.Second {
			backoff = bmin
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > bmax {
			backoff = bmax
		}
	}
}

func newState(sprite string, c *Creds) (State, error) {
	exe, err := os.Executable()
	if err != nil {
		return State{}, err
	}
	cmdline, err := ProcessCmdline(os.Getpid())
	if err != nil {
		return State{}, fmt.Errorf("broker: read own cmdline: %w", err)
	}
	proxy := "http://" + GuestProxyAddr
	env := map[string]string{
		"HTTPS_PROXY":         proxy,
		"https_proxy":         proxy,
		"NODE_EXTRA_CA_CERTS": GuestCAPath,
		"NO_PROXY":            "localhost,127.0.0.1",
		"no_proxy":            "localhost,127.0.0.1",
	}
	for k, v := range c.Env {
		env[k] = v
	}
	return State{
		PID:           os.Getpid(),
		Exe:           exe,
		Cmdline:       cmdline,
		Started:       time.Now().UTC(),
		CAFingerprint: hexSHA(c.CACertPEM),
		Listen:        "exec-tunnel:" + sprite,
		GuestEnv:      env,
		GuestCAPath:   GuestCAPath,
	}, nil
}

// serveOnce runs one relay exec + tunnel session until either dies.
func serveOnce(ctx context.Context, cfg RunConfig, creds *Creds, st State, id string) error {
	if err := KillStaleRelay(ctx, cfg.Exec, cfg.Sprite); err != nil {
		return err
	}
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	inR, inW := io.Pipe()   // host -> relay stdin
	outR, outW := io.Pipe() // relay stdout -> host
	execDone := make(chan error, 1)
	go func() {
		code, err := cfg.Exec(execCtx, cfg.Sprite, ExecRequest{
			Argv: []string{GuestAgentPath, "sprite-relay",
				"-listen", GuestProxyAddr,
				"-pidfile", GuestPidfile,
				"-secret-hosts", strings.Join(creds.Hosts, ",")},
			Stdin: inR, Stdout: outW,
		})
		if err == nil {
			err = fmt.Errorf("relay exited with code %d", code)
		}
		_ = outW.CloseWithError(err)
		_ = inR.CloseWithError(err)
		execDone <- err
	}()

	sess, err := tunnel.Host(tunnel.Join(inW, outR))
	if err != nil {
		cancel()
		<-execDone
		return err
	}
	srv := &http.Server{Handler: creds.Handler}
	go func() { _ = srv.Serve(sess) }()
	defer func() {
		_ = srv.Close()
		_ = sess.Close()
		cancel()
		<-execDone
	}()

	// broker.json means "exec may use the proxy": publish it only once the
	// guest relay is listening (it writes its pidfile after Listen).
	if err := waitRelayReady(ctx, cfg, execDone, sess); err != nil {
		return err
	}
	if err := WriteState(cfg.StateDir, id, st); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sess.Closed():
		return errors.New("tunnel session closed")
	case err := <-execDone:
		execDone <- err // let the deferred drain proceed
		return err
	}
}

const relayReadyTimeout = 30 * time.Second

// waitRelayReady polls the guest pidfile until the relay is up, failing early
// when the relay exec or the tunnel dies.
func waitRelayReady(ctx context.Context, cfg RunConfig, execDone chan error, sess interface{ Closed() <-chan struct{} }) error {
	deadline := time.NewTimer(relayReadyTimeout)
	defer deadline.Stop()
	for {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		code, err := cfg.Exec(pctx, cfg.Sprite, ExecRequest{
			Argv: []string{"sh", "-c", `[ -s "$1" ] && kill -0 "$(cat "$1")" 2>/dev/null`, "sh", GuestPidfile},
		})
		cancel()
		if err == nil && code == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-execDone:
			execDone <- err
			return fmt.Errorf("broker: relay exited before ready: %w", err)
		case <-sess.Closed():
			return errors.New("broker: tunnel closed before relay ready")
		case <-deadline.C:
			return fmt.Errorf("broker: guest relay not listening after %s", relayReadyTimeout)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
