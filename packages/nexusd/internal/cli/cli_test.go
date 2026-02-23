package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nexus/nexus/packages/nexusd/internal/config"
	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

func TestCheckVersion(t *testing.T) {
	originalVersion := version
	t.Cleanup(func() { version = originalVersion })

	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{
			name:    "version set",
			version: "1.0.0",
			wantErr: false,
		},
		{
			name:    "empty version",
			version: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version = tt.version
			err := checkVersion()
			if (err != nil) != tt.wantErr {
				t.Errorf("checkVersion() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckConfigDir(t *testing.T) {
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	defer os.Unsetenv("HOME")

	tests := []struct {
		name    string
		setup   func()
		wantErr bool
	}{
		{
			name:    "config dir exists",
			setup:   func() {},
			wantErr: false,
		},
		{
			name: "config dir does not exist",
			setup: func() {
				nexusDir := filepath.Join(homeDir, ".nexus")
				os.RemoveAll(nexusDir)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nexusDir := filepath.Join(homeDir, ".nexus")
			os.MkdirAll(nexusDir, 0755)

			tt.setup()

			err := checkConfigDir()
			if (err != nil) != tt.wantErr {
				t.Errorf("checkConfigDir() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestToJSON(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  string
	}{
		{
			name:  "string",
			input: "hello",
			want:  "hello",
		},
		{
			name:  "map",
			input: map[string]string{"a": "b"},
			want:  "map[a:b]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toJSON(tt.input)
			if got != tt.want {
				t.Errorf("toJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigGet(t *testing.T) {
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	defer os.Unsetenv("HOME")

	cfg := config.DefaultConfig()
	cfg.Daemon.Port = 9999
	cfg.Boulder.EnforcementLevel = "strict"
	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	tests := []struct {
		name         string
		key          string
		wantContains []string
		wantErr      bool
	}{
		{
			name:         "get all config",
			key:          "",
			wantContains: []string{"version:", "workspace.default:"},
		},
		{
			name:         "get specific key daemon.port",
			key:          "daemon.port",
			wantContains: []string{"9999"},
		},
		{
			name:         "get specific key boulder.enforcement_level",
			key:          "boulder.enforcement_level",
			wantContains: []string{"strict"},
		},
		{
			name:    "get unknown key",
			key:     "unknown.key",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("failed to load config: %v", err)
			}

			if tt.key == "" {
				return
			}

			value, err := cfg.Get(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Get() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(value, want) && !strings.Contains(cfg.Version, want) {
					t.Errorf("expected value to contain %q, got %q", want, value)
				}
			}
		})
	}
}

func TestConfigSet(t *testing.T) {
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	defer os.Unsetenv("HOME")

	cfg := config.DefaultConfig()
	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	tests := []struct {
		name       string
		key        string
		value      string
		verifyKey  string
		verifyWant string
		wantErr    bool
	}{
		{
			name:       "set daemon port",
			key:        "daemon.port",
			value:      "9998",
			verifyKey:  "daemon.port",
			verifyWant: "9998",
		},
		{
			name:       "set boulder enforcement level",
			key:        "boulder.enforcement_level",
			value:      "strict",
			verifyKey:  "boulder.enforcement_level",
			verifyWant: "strict",
		},
		{
			name:    "set unknown key",
			key:     "unknown.key",
			value:   "value",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("failed to load config: %v", err)
			}

			err = cfg.Set(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Set() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			val, _ := cfg.Get(tt.verifyKey)
			if !strings.Contains(val, tt.verifyWant) {
				t.Errorf("expected %s=%s, got %s", tt.verifyKey, tt.verifyWant, val)
			}
		})
	}
}

func TestConfigSetDefaultBackend(t *testing.T) {
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	defer os.Unsetenv("HOME")

	cfg := config.DefaultConfig()
	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	tests := []struct {
		name    string
		backend string
		wantErr bool
	}{
		{
			name:    "set docker backend",
			backend: "docker",
			wantErr: false,
		},
		{
			name:    "set daytona backend",
			backend: "daytona",
			wantErr: false,
		},
		{
			name:    "set unknown backend",
			backend: "unknown",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backendType := types.BackendTypeFromString(tt.backend)
			if backendType == types.BackendUnknown && !tt.wantErr {
				t.Errorf("expected valid backend type for %s", tt.backend)
				return
			}

			if tt.wantErr && backendType != types.BackendUnknown {
				t.Errorf("expected unknown backend type for %s", tt.backend)
			}
		})
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	tests := []struct {
		name    string
		setup   func() (string, func())
		wantErr bool
	}{
		{
			name: "file exists",
			setup: func() (string, func()) {
				tmpDir := t.TempDir()
				file := filepath.Join(tmpDir, "test.yaml")
				os.WriteFile(file, []byte("version: 1"), 0644)
				return file, func() {}
			},
			wantErr: false,
		},
		{
			name: "file does not exist",
			setup: func() (string, func()) {
				tmpDir := t.TempDir()
				return filepath.Join(tmpDir, "nonexistent.yaml"), func() {}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, cleanup := tt.setup()
			defer cleanup()

			cfg := config.DefaultConfig()
			err := loadConfigFromFile(cfg, path)
			if (err != nil) != tt.wantErr {
				t.Errorf("loadConfigFromFile() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
