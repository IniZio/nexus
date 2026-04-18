// Package daemon is the composition root for the Nexus daemon.
package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	apppty "github.com/inizio/nexus/packages/nexus/internal/app/pty"
	appspotlight "github.com/inizio/nexus/packages/nexus/internal/app/spotlight"
	appworkspace "github.com/inizio/nexus/packages/nexus/internal/app/workspace"
	"github.com/inizio/nexus/packages/nexus/internal/creds/relay"
	domainruntime "github.com/inizio/nexus/packages/nexus/internal/domain/runtime"
	"github.com/inizio/nexus/packages/nexus/internal/infra/runtime/firecracker"
	"github.com/inizio/nexus/packages/nexus/internal/infra/runtime/sandbox"
	"github.com/inizio/nexus/packages/nexus/internal/infra/store"
	rpcauth "github.com/inizio/nexus/packages/nexus/internal/rpc/auth"
	rpcdaemon "github.com/inizio/nexus/packages/nexus/internal/rpc/daemon"
	rpcfs "github.com/inizio/nexus/packages/nexus/internal/rpc/fs"
	rpcproject "github.com/inizio/nexus/packages/nexus/internal/rpc/project"
	rpcpty "github.com/inizio/nexus/packages/nexus/internal/rpc/pty"
	rpcregistry "github.com/inizio/nexus/packages/nexus/internal/rpc/registry"
	rpcspotlight "github.com/inizio/nexus/packages/nexus/internal/rpc/spotlight"
	rpcworkspace "github.com/inizio/nexus/packages/nexus/internal/rpc/workspace"
	"github.com/inizio/nexus/packages/nexus/internal/transport"
)

// NetworkConfig holds optional remote-listener settings.
type NetworkConfig struct {
	Enabled          bool
	BindAddress      string
	Port             int
	TLSMode          string // "off" | "auto" | "required"
	TokenAuthEnabled bool
	Token            string
	TLSCertFile      string
	TLSKeyFile       string
}

// ValidateNetworkConfig enforces policy rules for remote listener configuration.
func ValidateNetworkConfig(cfg NetworkConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if !cfg.TokenAuthEnabled {
		return fmt.Errorf("network listener requires token auth (--token must be set when --network is enabled)")
	}
	isLoopback := cfg.BindAddress == "127.0.0.1" || cfg.BindAddress == "::1"
	if !isLoopback && cfg.TLSMode == "off" {
		return fmt.Errorf("tls-mode \"off\" is not allowed for non-loopback bind address %q; use \"auto\" or \"required\"", cfg.BindAddress)
	}
	if len(cfg.Token) < 16 {
		log.Printf("daemon: WARNING: network auth token is fewer than 16 characters; use a longer token in production")
	}
	return nil
}

// Config holds all configuration for building a Daemon.
type Config struct {
	DBPath             string
	SocketPath         string
	FirecrackerEnabled bool
	FirecrackerBin     string
	KernelPath         string
	RootFSPath         string
	WorkDirRoot        string
	NodeName           string
	NodeTags           []string
	Network            NetworkConfig
}

// Daemon is the assembled daemon with all services wired together.
type Daemon struct {
	cfg             Config
	db              *store.DB
	registry        *rpcregistry.MapRegistry
	listener        *transport.Listener
	networkListener *transport.NetworkListener
}

// New constructs a Daemon by wiring all layers.
func New(cfg Config) (*Daemon, error) {
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(defaultDataDir(), "nexus.db")
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = filepath.Join(defaultDataDir(), "nexusd.sock")
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	wsStore := store.NewWorkspaceStore(db)
	projStore := store.NewProjectStore(db)
	fwdStore := store.NewForwardStore(db)

	var rtDriver domainruntime.Driver
	if cfg.FirecrackerEnabled {
		rtDriver = firecracker.NewAdapter(buildFirecrackerDriver(cfg))
		log.Printf("daemon: firecracker runtime driver wired")
	} else {
		rtDriver = sandbox.NewAdapter(sandbox.NewDriver())
		log.Printf("daemon: sandbox (process) runtime driver wired")
	}

	wsSvc := appworkspace.NewService(wsStore, projStore, rtDriver)

	spotlightSvc := appspotlight.New(fwdStore, wsStore)
	ptyReg := apppty.NewRegistry()
	broker := relay.NewBroker()

	reg := rpcregistry.NewMapRegistry()

	rpcworkspace.NewHandler(wsSvc).Register(reg)
	if err := rpcspotlight.New(spotlightSvc).Register(reg); err != nil {
		db.Close()
		return nil, fmt.Errorf("register spotlight rpc: %w", err)
	}
	rpcpty.New(ptyReg).Register(reg)
	rpcfs.New("/").Register(reg)
	rpcdaemon.New(newNodeInfo(cfg)).Register(reg)
	rpcproject.New(projStore).Register(reg)
	rpcauth.New(wsStore, broker).Register(reg)

	lst := transport.NewListener(cfg.SocketPath, reg)

	d := &Daemon{
		cfg:      cfg,
		db:       db,
		registry: reg,
		listener: lst,
	}

	if cfg.Network.Enabled {
		nl, err := transport.NewNetworkListener(transport.NetworkListenerConfig{
			BindAddress: cfg.Network.BindAddress,
			Port:        cfg.Network.Port,
			TLSMode:     cfg.Network.TLSMode,
			Token:       cfg.Network.Token,
			TLSCertFile: cfg.Network.TLSCertFile,
			TLSKeyFile:  cfg.Network.TLSKeyFile,
		}, reg)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("network listener: %w", err)
		}
		d.networkListener = nl
	}

	return d, nil
}

// Start begins accepting connections. It blocks until ctx is cancelled or an error occurs.
func (d *Daemon) Start(ctx context.Context) error {
	log.Printf("daemon: listening on %s", d.cfg.SocketPath)

	if d.networkListener == nil {
		return d.listener.Serve(ctx)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- d.listener.Serve(ctx) }()
	go func() { errCh <- d.networkListener.Serve(ctx) }()

	err := <-errCh
	return err
}

// Stop shuts down the daemon, closing transport and database.
func (d *Daemon) Stop() error {
	var errs []error
	if err := d.listener.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close listener: %w", err))
	}
	if d.networkListener != nil {
		if err := d.networkListener.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close network listener: %w", err))
		}
	}
	if err := d.db.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close db: %w", err))
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func buildFirecrackerDriver(cfg Config) *firecracker.FCDriver {
	type runner struct{}
	// CommandRunner is satisfied by a local exec-based runner.
	// This wraps exec.CommandContext inline.
	mgr := firecracker.NewManager(firecracker.ManagerConfig{
		FirecrackerBin: orDefault(cfg.FirecrackerBin, "firecracker"),
		KernelPath:     orDefault(cfg.KernelPath, os.Getenv("NEXUS_FIRECRACKER_KERNEL")),
		RootFSPath:     orDefault(cfg.RootFSPath, os.Getenv("NEXUS_FIRECRACKER_ROOTFS")),
		WorkDirRoot:    cfg.WorkDirRoot,
	})
	return firecracker.New(execRunner{}, firecracker.WithManager(mgr))
}

// execRunner satisfies firecracker.CommandRunner using os/exec.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir string, cmd string, args ...string) error {
	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = dir
	return c.Run()
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func defaultDataDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "nexus")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/var/lib/nexus"
	}
	return filepath.Join(home, ".local", "state", "nexus")
}

// nodeInfo wraps Config to satisfy rpcdaemon.NodeInfoProvider.
type nodeInfo struct{ cfg Config }

func newNodeInfo(cfg Config) *nodeInfo { return &nodeInfo{cfg: cfg} }

func (n *nodeInfo) NodeName() string   { return n.cfg.NodeName }
func (n *nodeInfo) NodeTags() []string { return n.cfg.NodeTags }
func (n *nodeInfo) Capabilities() []rpcdaemon.Capability {
	return []rpcdaemon.Capability{
		{Name: "runtime.process", Available: true},
		{Name: "runtime.firecracker", Available: n.cfg.FirecrackerEnabled},
	}
}
