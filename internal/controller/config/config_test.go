package config_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/config"
)

func parseYAML(t *testing.T, src string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

const validYAML = `
slack:
  app_token:
    env: SLACK_APP_TOKEN
  bot_token:
    env: SLACK_BOT_TOKEN
channels:
  C_GENERAL:
    repo: /home/example/app
    idle_pause: 10m
    idle_stop: 1h
deployment_mode: laptop
`

func TestResolveChannel(t *testing.T) {
	cfg := parseYAML(t, validYAML)
	r := config.NewResolver(cfg)

	proj, err := r.Resolve(context.Background(), "C_GENERAL")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if proj != "/home/example/app" {
		t.Fatalf("project = %q, want /home/example/app", proj)
	}
}

func TestUnknownChannelRefused(t *testing.T) {
	cfg := parseYAML(t, validYAML)
	r := config.NewResolver(cfg)

	_, err := r.Resolve(context.Background(), "C_UNKNOWN")
	if !errors.Is(err, controller.ErrNoProject) {
		t.Fatalf("expected ErrNoProject, got %v", err)
	}
}

func TestConfigRejectsInlineSecrets(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "inline app token",
			yaml: `
slack:
  app_token:
    value: xapp-real-secret
  bot_token:
    env: SLACK_BOT_TOKEN
channels:
  C1:
    repo: example/repo
`,
		},
		{
			name: "inline bot token",
			yaml: `
slack:
  app_token:
    env: SLACK_APP_TOKEN
  bot_token:
    value: xoxb-real-secret
channels:
  C1:
    repo: example/repo
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Parse([]byte(tc.yaml))
			if !errors.Is(err, config.ErrInlineSecret) {
				t.Fatalf("expected ErrInlineSecret, got %v", err)
			}
		})
	}
}

func TestValidateRejectsMissingRepo(t *testing.T) {
	yaml := `
slack:
  app_token:
    env: SLACK_APP_TOKEN
  bot_token:
    env: SLACK_BOT_TOKEN
channels:
  C_NOREPO:
    idle_pause: 5m
`
	_, err := config.Parse([]byte(yaml))
	if !errors.Is(err, config.ErrMissingRepo) {
		t.Fatalf("expected ErrMissingRepo, got %v", err)
	}
}

func TestEmptyDeploymentModeNormalisesToLaptop(t *testing.T) {
	const src = `
slack:
  app_token:
    env: SLACK_APP_TOKEN
  bot_token:
    env: SLACK_BOT_TOKEN
`
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.DeploymentMode != config.ModeLocal {
		t.Fatalf("DeploymentMode = %q, want %q", cfg.DeploymentMode, config.ModeLocal)
	}
}

func TestUnknownDeploymentModeRejected(t *testing.T) {
	const src = `
slack:
  app_token:
    env: SLACK_APP_TOKEN
  bot_token:
    env: SLACK_BOT_TOKEN
deployment_mode: enterprise
`
	_, err := config.Parse([]byte(src))
	if !errors.Is(err, config.ErrUnknownMode) {
		t.Fatalf("expected ErrUnknownMode, got %v", err)
	}
}

func TestValidateRejectsNonAbsoluteRepo(t *testing.T) {
	yaml := `
slack:
  app_token:
    env: SLACK_APP_TOKEN
  bot_token:
    env: SLACK_BOT_TOKEN
channels:
  C_REL:
    repo: github.com/example/app
`
	_, err := config.Parse([]byte(yaml))
	if !errors.Is(err, config.ErrNonAbsoluteRepo) {
		t.Fatalf("expected ErrNonAbsoluteRepo, got %v", err)
	}
}

func TestErrNoProjectIsControllerAlias(t *testing.T) {
	if !errors.Is(controller.ErrNoProject, config.ErrNoProject) {
		t.Fatal("controller.ErrNoProject must be the same error as config.ErrNoProject")
	}
}
