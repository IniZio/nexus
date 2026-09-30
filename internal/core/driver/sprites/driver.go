// Package sprites implements a driver whose sandbox is a Fly.io Sprite.
package sprites

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

const (
	backendName   = "sprites"
	nameSuffixLen = 12
	cloneDir      = "/home/sprite/work" // absolute: exec Dir is not resolved against $HOME
	stderrTail    = 2048
)

// Config configures a [Driver]. A non-nil API is used as-is (tests); otherwise
// the API is built from the token via newSDKAPI.
type Config struct {
	Token    string
	Org      string
	StateDir string
	API      API
}

// Spec is the per-sandbox provisioning record. GitToken is never persisted.
type Spec struct {
	Repo            string   `json:"repo,omitempty"`
	CloneDir        string   `json:"clone_dir,omitempty"`
	AllowedHosts    []string `json:"allowed_hosts,omitempty"`
	OpenEgress      bool     `json:"open_egress,omitempty"`
	IncludeDefaults bool     `json:"include_defaults,omitempty"`
	GitToken        string   `json:"-"`
}

// Driver is the Sprites substrate. Keep-alive and the Tasks API are not
// implemented: an open exec connection keeps a sprite awake, and idle sprites
// auto-suspend.
type Driver struct {
	cfg Config
	api API
}

var _ driver.Driver = (*Driver)(nil)

// New builds a Driver, resolving credentials from cfg then the environment.
func New(cfg Config) (*Driver, error) {
	if cfg.StateDir == "" {
		return nil, errors.New("sprites: StateDir is required")
	}
	api := cfg.API
	if api == nil {
		token := cfg.Token
		if token == "" {
			token = os.Getenv("SPRITES_TOKEN")
		}
		if token == "" {
			token = os.Getenv("SPRITES_API_TOKEN")
		}
		if token == "" {
			return nil, errors.New("sprites: no API token; set SPRITES_TOKEN or SPRITES_API_TOKEN")
		}
		org := cfg.Org
		if org == "" {
			org = os.Getenv("SPRITES_ORG")
		}
		var err error
		api, err = newSDKAPI(token, org)
		if err != nil {
			return nil, fmt.Errorf("sprites: %w", err)
		}
	}
	return &Driver{cfg: cfg, api: api}, nil
}

// SpriteName is deterministic: NamePrefix plus the tail of the id, restricted
// to [a-z0-9-].
func SpriteName(id domain.SandboxID) string {
	s := strings.ToLower(id.String())
	if len(s) > nameSuffixLen {
		s = s[len(s)-nameSuffixLen:]
	}
	var b strings.Builder
	b.WriteString(NamePrefix)
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (d *Driver) dir(id domain.SandboxID) string {
	return filepath.Join(d.cfg.StateDir, backendName, id.String())
}
func (d *Driver) marker(id domain.SandboxID) string   { return filepath.Join(d.dir(id), "running") }
func (d *Driver) specPath(id domain.SandboxID) string { return filepath.Join(d.dir(id), "spec.json") }
func instanceID(id domain.SandboxID) string           { return backendName + ":" + id.String() }

func checkName(name string) error {
	if !strings.HasPrefix(name, NamePrefix) {
		return fmt.Errorf("sprites: refusing sprite %q without %q prefix", name, NamePrefix)
	}
	return nil
}

// Provision creates the sprite, applies egress policy, optionally clones the
// repo, and persists the spec (without GitToken).
func (d *Driver) Provision(ctx context.Context, id domain.SandboxID, s Spec) (err error) {
	name := SpriteName(id)
	if err := checkName(name); err != nil {
		return err
	}
	if err := d.api.CreateSprite(ctx, name); err != nil {
		return fmt.Errorf("sprites: create %s: %w", name, err)
	}
	defer func() {
		if err != nil {
			_ = d.api.DeleteSprite(context.WithoutCancel(ctx), name)
		}
	}()
	if !s.OpenEgress {
		if p := BuildPolicy(s.AllowedHosts, s.IncludeDefaults, false); p != nil {
			if err := d.api.SetNetworkPolicy(ctx, name, p); err != nil {
				return fmt.Errorf("sprites: set network policy: %w", err)
			}
		}
	}
	if s.Repo != "" {
		if err := d.clone(ctx, name, s); err != nil {
			return err
		}
		s.CloneDir = cloneDir
	}
	return d.writeSpec(id, s)
}

func (d *Driver) clone(ctx context.Context, name string, s Spec) error {
	argv := []string{"git"}
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	if s.GitToken != "" {
		// Weaker than Sprites Connectors: the token sits in the clone process env only.
		env["GH_TOKEN"] = s.GitToken
		argv = append(argv, "-c", `credential.helper=!f() { echo username=x-access-token; echo password=$GH_TOKEN; }; f`)
	}
	argv = append(argv, "clone", "--", s.Repo, cloneDir)
	var stderr bytes.Buffer
	code, err := d.api.Exec(ctx, name, ExecRequest{Argv: argv, Env: env, Stderr: &stderr})
	if err != nil {
		return fmt.Errorf("sprites: clone: %s", scrub(err.Error(), s.GitToken))
	}
	if code != 0 {
		msg := scrub(stderr.String(), s.GitToken)
		if len(msg) > stderrTail {
			msg = msg[len(msg)-stderrTail:]
		}
		return fmt.Errorf("sprites: git clone exited %d: %s", code, strings.TrimSpace(msg))
	}
	return nil
}

func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}

func (d *Driver) writeSpec(id domain.SandboxID, s Spec) error {
	if err := os.MkdirAll(d.dir(id), 0o700); err != nil {
		return fmt.Errorf("sprites: create state dir: %w", err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.dir(id), "spec-*.tmp")
	if err != nil {
		return fmt.Errorf("sprites: write spec: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("sprites: write spec: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sprites: write spec: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("sprites: write spec: %w", err)
	}
	if err := os.Rename(tmp.Name(), d.specPath(id)); err != nil {
		return fmt.Errorf("sprites: write spec: %w", err)
	}
	return nil
}

// Spec returns the persisted spec for id.
func (d *Driver) Spec(id domain.SandboxID) (Spec, error) {
	b, err := os.ReadFile(d.specPath(id))
	if err != nil {
		return Spec{}, fmt.Errorf("sprites: read spec for %s: %w", id, err)
	}
	var s Spec
	if err := json.Unmarshal(b, &s); err != nil {
		return Spec{}, fmt.Errorf("sprites: decode spec for %s: %w", id, err)
	}
	return s, nil
}

// Deprovision deletes the sprite and removes its state dir; idempotent.
func (d *Driver) Deprovision(ctx context.Context, id domain.SandboxID) error {
	name := SpriteName(id)
	if err := checkName(name); err != nil {
		return err
	}
	exists, err := d.api.SpriteExists(ctx, name)
	if err != nil {
		return fmt.Errorf("sprites: deprovision %s: %w", name, err)
	}
	if exists {
		if err := d.api.DeleteSprite(ctx, name); err != nil {
			return fmt.Errorf("sprites: delete %s: %w", name, err)
		}
	}
	if err := os.RemoveAll(d.dir(id)); err != nil {
		return fmt.Errorf("sprites: remove state: %w", err)
	}
	return nil
}

func (d *Driver) Name() string { return backendName }

func (d *Driver) Observe(ctx context.Context, id domain.SandboxID) (driver.Observation, error) {
	ok, err := d.api.SpriteExists(ctx, SpriteName(id))
	if err != nil {
		return driver.Observation{State: driver.Unknown, Detail: err.Error()}, fmt.Errorf("sprites: observe: %w", err)
	}
	if !ok {
		return driver.Observation{State: driver.Absent}, nil
	}
	if _, err := os.Stat(d.marker(id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return driver.Observation{State: driver.Absent, Detail: "stopped"}, nil
		}
		return driver.Observation{State: driver.Unknown, Detail: err.Error()}, fmt.Errorf("sprites: observe marker: %w", err)
	}
	return driver.Observation{State: driver.Running, InstanceID: instanceID(id)}, nil
}

func (d *Driver) Start(ctx context.Context, req driver.StartRequest) (string, error) {
	ok, err := d.api.SpriteExists(ctx, SpriteName(req.SandboxID))
	if err != nil {
		return "", fmt.Errorf("sprites: start: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("sprites: sandbox %s not provisioned", req.SandboxID)
	}
	if err := os.MkdirAll(d.dir(req.SandboxID), 0o700); err != nil {
		return "", fmt.Errorf("sprites: start: %w", err)
	}
	if err := os.WriteFile(d.marker(req.SandboxID), nil, 0o600); err != nil {
		return "", fmt.Errorf("sprites: write running marker: %w", err)
	}
	return instanceID(req.SandboxID), nil
}

// Stop clears the running marker; idempotent. The sprite is kept: it auto-suspends.
func (d *Driver) Stop(_ context.Context, id domain.SandboxID) error {
	if err := os.Remove(d.marker(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sprites: clear running marker: %w", err)
	}
	return nil
}

func (d *Driver) Exec(ctx context.Context, id domain.SandboxID, opts driver.ExecOptions) (int32, error) {
	if len(opts.Argv) == 0 {
		return 0, errors.New("sprites: exec: empty argv")
	}
	req := ExecRequest{
		Argv:   opts.Argv,
		Env:    opts.Env,
		Dir:    opts.Cwd,
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
		Resize: opts.WinsizeCh,
	}
	if req.Dir == "" {
		if s, err := d.Spec(id); err == nil {
			req.Dir = s.CloneDir
		}
	}
	if opts.Pty != nil {
		req.TTY = true
		req.Term = opts.Pty.GetTerm()
		sz := opts.Pty.GetInitialSize()
		req.Rows, req.Cols = uint16(sz.GetRows()), uint16(sz.GetCols())
	}
	return d.api.Exec(ctx, SpriteName(id), req)
}

func (d *Driver) Copy(ctx context.Context, id domain.SandboxID, opts driver.CopyOptions) error {
	return copyViaExec(ctx, d.api, SpriteName(id), opts)
}

func (d *Driver) Capabilities() driver.CapabilitySet {
	return driver.CapabilitySet{GuestOS: driver.GuestOSLinux, Egress: driver.EgressEnforced}
}
