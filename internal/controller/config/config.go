package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type DeploymentMode string

const (
	ModeLocal  DeploymentMode = "laptop"
	ModeShared DeploymentMode = "shared"
)

type TokenRef struct {
	EnvVar   string `yaml:"env"`
	FilePath string `yaml:"file"`
	Inline   string `yaml:"value"`
}

type SlackConfig struct {
	AppToken TokenRef `yaml:"app_token"`
	BotToken TokenRef `yaml:"bot_token"`
}

type ChannelConfig struct {
	Repo           string        `yaml:"repo"`
	IdlePause      time.Duration `yaml:"idle_pause"`
	IdleStop       time.Duration `yaml:"idle_stop"`
	PermissionMode string        `yaml:"permission_mode"`
	Model          string        `yaml:"model"`
}

type Config struct {
	Slack          SlackConfig              `yaml:"slack"`
	Channels       map[string]ChannelConfig `yaml:"channels"`
	DeploymentMode DeploymentMode           `yaml:"deployment_mode"`
}

var (
	ErrInlineSecret    = errors.New("config: inline secrets are not allowed; use env or file references")
	ErrMissingRepo     = errors.New("config: channel has no repo configured")
	ErrNonAbsoluteRepo = errors.New("config: channel repo must be an absolute path to a local git checkout")
	ErrNoProject       = errors.New("controller: channel has no project")
	ErrUnknownMode     = errors.New("config: unknown deployment_mode; valid values: laptop, shared")
	ErrInvalidPermMode = errors.New("config: invalid permission_mode; valid values: auto, default, bypassPermissions, acceptEdits")
)

func (t TokenRef) Validate(name string) error {
	if t.Inline != "" {
		return fmt.Errorf("%w: field %q", ErrInlineSecret, name)
	}
	return nil
}

// Resolve returns the token value by reading the env var or file.
// It returns an error when the env var is unset/empty or the file cannot be read.
// Inline values are rejected by Validate() and are never returned here.
func (t TokenRef) Resolve() (string, error) {
	if t.EnvVar != "" {
		v := os.Getenv(t.EnvVar)
		if v == "" {
			return "", fmt.Errorf("config: env var %q is not set or empty", t.EnvVar)
		}
		return v, nil
	}
	if t.FilePath != "" {
		data, err := os.ReadFile(t.FilePath)
		if err != nil {
			return "", fmt.Errorf("config: token file %q: %w", t.FilePath, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", fmt.Errorf("config: token ref has no source configured (set env or file)")
}

func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	return Parse(data)
}

func (c *Config) Validate() error {
	if c.DeploymentMode == "" {
		c.DeploymentMode = ModeLocal
	}
	switch c.DeploymentMode {
	case ModeLocal, ModeShared:
	default:
		return fmt.Errorf("%w: %q", ErrUnknownMode, c.DeploymentMode)
	}
	if err := c.Slack.AppToken.Validate("slack.app_token"); err != nil {
		return err
	}
	if err := c.Slack.BotToken.Validate("slack.bot_token"); err != nil {
		return err
	}
	for id, ch := range c.Channels {
		if strings.TrimSpace(ch.Repo) == "" {
			return fmt.Errorf("%w: channel %q", ErrMissingRepo, id)
		}
		if !filepath.IsAbs(ch.Repo) {
			return fmt.Errorf("%w: channel %q repo %q", ErrNonAbsoluteRepo, id, ch.Repo)
		}
		switch ch.PermissionMode {
		case "", "auto", "default", "bypassPermissions", "acceptEdits":
		default:
			return fmt.Errorf("%w: channel %q got %q", ErrInvalidPermMode, id, ch.PermissionMode)
		}
	}
	return nil
}

// Resolver implements controller.ProjectResolver for a static config.
type Resolver struct{ cfg *Config }

func NewResolver(cfg *Config) *Resolver { return &Resolver{cfg: cfg} }

func (r *Resolver) Resolve(_ context.Context, channel string) (string, error) {
	ch, ok := r.cfg.Channels[channel]
	if !ok {
		return "", fmt.Errorf("%w: channel %q", ErrNoProject, channel)
	}
	return ch.Repo, nil
}

// PermMode returns the configured permission_mode for the channel, or "" when
// the channel is not found or no mode is set (callers treat "" as "use default").
func (r *Resolver) PermMode(channel string) string {
	ch, ok := r.cfg.Channels[channel]
	if !ok {
		return ""
	}
	return ch.PermissionMode
}
