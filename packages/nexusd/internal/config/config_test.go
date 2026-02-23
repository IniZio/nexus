package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nexus/nexus/packages/nexusd/internal/types"
	"gopkg.in/yaml.v3"
)

func TestLoad_ExistingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	cfg := &Config{
		Version: "1",
		Workspace: WorkspaceConfig{
			DefaultBackend: types.BackendDocker,
			Default:        "test-workspace",
			AutoStart:      false,
			StoragePath:    "/custom/path",
		},
		Boulder: BoulderConfig{
			EnforcementLevel: "strict",
			IdleThreshold:    60,
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".nexus", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Version != "1" {
		t.Errorf("Version = %q, want %q", loaded.Version, "1")
	}
	if loaded.Workspace.Default != "test-workspace" {
		t.Errorf("Workspace.Default = %q, want %q", loaded.Workspace.Default, "test-workspace")
	}
	if loaded.Workspace.AutoStart != false {
		t.Errorf("Workspace.AutoStart = %v, want %v", loaded.Workspace.AutoStart, false)
	}
	if loaded.Boulder.EnforcementLevel != "strict" {
		t.Errorf("Boulder.EnforcementLevel = %q, want %q", loaded.Boulder.EnforcementLevel, "strict")
	}
	if loaded.Boulder.IdleThreshold != 60 {
		t.Errorf("Boulder.IdleThreshold = %d, want %d", loaded.Boulder.IdleThreshold, 60)
	}
}

func TestLoad_MissingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	configPath := filepath.Join(tmpDir, ".nexus", "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("config file was not created")
	}

	if loaded.Version != "1" {
		t.Errorf("Version = %q, want %q", loaded.Version, "1")
	}
	if loaded.Workspace.AutoStart != true {
		t.Errorf("Workspace.AutoStart = %v, want %v", loaded.Workspace.AutoStart, true)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	configPath := filepath.Join(tmpDir, ".nexus", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("invalid: yaml: content:"), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Error("Load() expected error for malformed YAML, got nil")
	}
}

func TestSave(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	cfg := &Config{
		Version: "2",
		Workspace: WorkspaceConfig{
			DefaultBackend: types.BackendSprite,
			Default:        "saved-workspace",
			AutoStart:      false,
			StoragePath:    "/saved/path",
		},
		Boulder: BoulderConfig{
			EnforcementLevel: "disabled",
			IdleThreshold:    120,
		},
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	configPath := filepath.Join(tmpDir, ".nexus", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}

	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to unmarshal saved config: %v", err)
	}

	if loaded.Version != "2" {
		t.Errorf("Version = %q, want %q", loaded.Version, "2")
	}
	if loaded.Workspace.Default != "saved-workspace" {
		t.Errorf("Workspace.Default = %q, want %q", loaded.Workspace.Default, "saved-workspace")
	}
}

func TestGet(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Workspace: WorkspaceConfig{
			DefaultBackend: types.BackendDocker,
			Default:        "my-workspace",
			AutoStart:      true,
			StoragePath:    "/home/user/.nexus/workspaces",
		},
		Boulder: BoulderConfig{
			EnforcementLevel: "normal",
			IdleThreshold:    30,
		},
		Telemetry: TelemetryConfig{
			Enabled:       true,
			Sampling:      100,
			RetentionDays: 30,
		},
		Daemon: DaemonConfig{
			Host: "localhost",
			Port: 9847,
		},
		CLI: CLIConfig{
			Update: UpdateConfig{
				AutoInstall: true,
				Channel:     "stable",
			},
		},
	}

	tests := []struct {
		key     string
		want    string
		wantErr bool
	}{
		{"version", "1", false},
		{"workspace.default", "my-workspace", false},
		{"workspace.default_backend", "docker", false},
		{"workspace.auto_start", "true", false},
		{"workspace.storage_path", "/home/user/.nexus/workspaces", false},
		{"boulder.enforcement_level", "normal", false},
		{"boulder.idle_threshold", "30", false},
		{"telemetry.enabled", "true", false},
		{"telemetry.sampling", "100", false},
		{"telemetry.retention_days", "30", false},
		{"daemon.host", "localhost", false},
		{"daemon.port", "9847", false},
		{"cli.update.auto_install", "true", false},
		{"cli.update.channel", "stable", false},
		{"unknown.key", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := cfg.Get(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("Get(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Get(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestSet(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	cfg := &Config{
		Version:   "1",
		Workspace: WorkspaceConfig{DefaultBackend: types.BackendDocker},
		Boulder:   BoulderConfig{EnforcementLevel: "normal", IdleThreshold: 30},
		Telemetry: TelemetryConfig{Enabled: true, Sampling: 100, RetentionDays: 30},
		Daemon:    DaemonConfig{Host: "localhost", Port: 9847},
		CLI:       CLIConfig{Update: UpdateConfig{AutoInstall: true, Channel: "stable"}},
	}

	tests := []struct {
		key     string
		value   string
		wantErr bool
	}{
		{"version", "2", false},
		{"workspace.default", "new-workspace", false},
		{"workspace.default_backend", "sprite", false},
		{"workspace.auto_start", "false", false},
		{"workspace.storage_path", "/new/path", false},
		{"boulder.enforcement_level", "strict", false},
		{"boulder.idle_threshold", "45", false},
		{"telemetry.enabled", "false", false},
		{"telemetry.sampling", "50", false},
		{"telemetry.retention_days", "60", false},
		{"daemon.host", "0.0.0.0", false},
		{"daemon.port", "9000", false},
		{"cli.update.auto_install", "false", false},
		{"cli.update.channel", "beta", false},
		{"unknown.key", "value", true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			err := cfg.Set(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("Set(%q, %q) error = %v, wantErr %v", tt.key, tt.value, err, tt.wantErr)
				return
			}
		})
	}

	if cfg.Version != "2" {
		t.Errorf("Version = %q, want %q", cfg.Version, "2")
	}
	if cfg.Workspace.Default != "new-workspace" {
		t.Errorf("Workspace.Default = %q, want %q", cfg.Workspace.Default, "new-workspace")
	}
	if cfg.Workspace.DefaultBackend != types.BackendSprite {
		t.Errorf("Workspace.DefaultBackend = %v, want %v", cfg.Workspace.DefaultBackend, types.BackendSprite)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Version != "1" {
		t.Errorf("Version = %q, want %q", cfg.Version, "1")
	}
	if cfg.Workspace.DefaultBackend != types.BackendDocker {
		t.Errorf("Workspace.DefaultBackend = %v, want %v", cfg.Workspace.DefaultBackend, types.BackendDocker)
	}
	if cfg.Workspace.AutoStart != true {
		t.Errorf("Workspace.AutoStart = %v, want %v", cfg.Workspace.AutoStart, true)
	}
	if cfg.Boulder.EnforcementLevel != "normal" {
		t.Errorf("Boulder.EnforcementLevel = %q, want %q", cfg.Boulder.EnforcementLevel, "normal")
	}
	if cfg.Boulder.IdleThreshold != 30 {
		t.Errorf("Boulder.IdleThreshold = %d, want %d", cfg.Boulder.IdleThreshold, 30)
	}
	if cfg.Telemetry.Enabled != true {
		t.Errorf("Telemetry.Enabled = %v, want %v", cfg.Telemetry.Enabled, true)
	}
	if cfg.Daemon.Port != 9847 {
		t.Errorf("Daemon.Port = %d, want %d", cfg.Daemon.Port, 9847)
	}
}

func TestYAMLRoundTrip(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Workspace: WorkspaceConfig{
			DefaultBackend: types.BackendDocker,
			Default:        "test",
			AutoStart:      true,
			StoragePath:    "/path",
		},
		Boulder: BoulderConfig{
			EnforcementLevel: "normal",
			IdleThreshold:    30,
		},
		Telemetry: TelemetryConfig{
			Enabled:       true,
			Sampling:      100,
			RetentionDays: 30,
		},
		Daemon: DaemonConfig{
			Host: "localhost",
			Port: 9847,
		},
		CLI: CLIConfig{
			Update: UpdateConfig{
				AutoInstall: true,
				Channel:     "stable",
			},
		},
		Backends: BackendConfigs{
			Docker: DockerConfig{Enabled: true},
			Daytona: types.DaytonaConfig{
				Enabled: false,
				APIURL:  "https://app.daytona.io/api",
			},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}

	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}

	if loaded.Version != cfg.Version {
		t.Errorf("Version = %q, want %q", loaded.Version, cfg.Version)
	}
	if loaded.Workspace.DefaultBackend != cfg.Workspace.DefaultBackend {
		t.Errorf("DefaultBackend = %v, want %v", loaded.Workspace.DefaultBackend, cfg.Workspace.DefaultBackend)
	}
	if loaded.Backends.Daytona.Enabled != false {
		t.Errorf("Daytona.Enabled = %v, want %v", loaded.Backends.Daytona.Enabled, false)
	}
	if loaded.Backends.Daytona.APIURL != "https://app.daytona.io/api" {
		t.Errorf("Daytona.APIURL = %q, want %q", loaded.Backends.Daytona.APIURL, "https://app.daytona.io/api")
	}
}

func TestGet_AllKeys(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Workspace: WorkspaceConfig{
			DefaultBackend: types.BackendDocker,
			Default:        "test",
			AutoStart:      true,
			StoragePath:    "/path",
		},
		Boulder: BoulderConfig{
			EnforcementLevel: "normal",
			IdleThreshold:    30,
		},
		Telemetry: TelemetryConfig{
			Enabled:       true,
			Sampling:      100,
			RetentionDays: 30,
		},
		Daemon: DaemonConfig{
			Host: "localhost",
			Port: 9847,
		},
		CLI: CLIConfig{
			Update: UpdateConfig{
				AutoInstall: true,
				Channel:     "stable",
			},
		},
	}

	keys := []string{
		"version",
		"workspace.default",
		"workspace.default_backend",
		"workspace.auto_start",
		"workspace.storage_path",
		"boulder.enforcement_level",
		"boulder.idle_threshold",
		"telemetry.enabled",
		"telemetry.sampling",
		"telemetry.retention_days",
		"daemon.host",
		"daemon.port",
		"cli.update.auto_install",
		"cli.update.channel",
	}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			val, err := cfg.Get(key)
			if err != nil {
				t.Errorf("Get(%q) error = %v", key, err)
			}
			if val == "" {
				t.Errorf("Get(%q) returned empty string", key)
			}
		})
	}
}
