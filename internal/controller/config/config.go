package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/IniZio/nexus/internal/controller"
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
	Repo      string        `yaml:"repo"`
	IdlePause time.Duration `yaml:"idle_pause"`
	IdleStop  time.Duration `yaml:"idle_stop"`
}

type Config struct {
	Slack          SlackConfig               `yaml:"slack"`
	Channels       map[string]ChannelConfig  `yaml:"channels"`
	DeploymentMode DeploymentMode            `yaml:"deployment_mode"`
}

var (
	ErrInlineSecret    = errors.New("config: inline secrets are not allowed; use env or file references")
	ErrMissingRepo     = errors.New("config: channel has no repo configured")
)

func (t TokenRef) Validate(name string) error {
	if t.Inline != "" {
		return fmt.Errorf("%w: field %q", ErrInlineSecret, name)
	}
	return nil
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
	}
	return nil
}

// Resolver implements controller.ProjectResolver for a static config.
type Resolver struct{ cfg *Config }

func NewResolver(cfg *Config) *Resolver { return &Resolver{cfg: cfg} }

func (r *Resolver) Resolve(_ context.Context, channel string) (string, error) {
	ch, ok := r.cfg.Channels[channel]
	if !ok {
		return "", fmt.Errorf("%w: channel %q", controller.ErrNoProject, channel)
	}
	return ch.Repo, nil
}

var _ controller.ProjectResolver = (*Resolver)(nil)
