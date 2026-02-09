package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadUserConfig(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(string)
		wantErr  bool
		validate func(*UserConfig)
	}{
		{
			name: "load valid config",
			setup: func(configPath string) {
				content := `github:
  username: testuser
  user_id: 12345
ssh:
  key_path: ~/.ssh/id_ed25519
`
				os.WriteFile(configPath, []byte(content), 0644)
			},
			wantErr: false,
			validate: func(uc *UserConfig) {
				assert.Equal(t, "testuser", uc.GitHub.Username)
				assert.Equal(t, int64(12345), uc.GitHub.UserID)
			},
		},
		{
			name: "missing config file",
			setup: func(configPath string) {
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")
			tt.setup(configPath)

			cfg, err := LoadUserConfig(configPath)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(cfg)
				}
			}
		})
	}
}

func TestSaveUserConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	cfg := &UserConfig{}
	cfg.GitHub.Username = "testuser"
	cfg.GitHub.UserID = 12345
	cfg.SSH.KeyPath = "~/.ssh/id_ed25519"

	err := SaveUserConfig(configPath, cfg)
	assert.NoError(t, err)
	assert.FileExists(t, configPath)

	loaded, err := LoadUserConfig(configPath)
	assert.NoError(t, err)
	assert.Equal(t, cfg.GitHub.Username, loaded.GitHub.Username)
	assert.Equal(t, cfg.GitHub.UserID, loaded.GitHub.UserID)
}

func TestUserConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *UserConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid minimal config",
			cfg:     &UserConfig{},
			wantErr: false,
		},
		{
			name: "valid full config",
			cfg: func() *UserConfig {
				cfg := &UserConfig{}
				cfg.GitHub.Username = "user"
				cfg.GitHub.UserID = 123
				cfg.SSH.KeyPath = "~/.ssh/id_ed25519"
				return cfg
			}(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUserConfig(tt.cfg)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestEnsureConfigDirectory(t *testing.T) {
	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, ".nexus")

	assert.NoFileExists(t, configDir)

	err := EnsureConfigDirectory(configDir)
	assert.NoError(t, err)
	assert.DirExists(t, configDir)

	info, _ := os.Stat(configDir)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

func TestGetUserConfigPath(t *testing.T) {
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)

	tempHome := t.TempDir()
	os.Setenv("HOME", tempHome)

	path := GetUserConfigPath()
	assert.Equal(t, filepath.Join(tempHome, ".nexus", "config.yaml"), path)
}

func TestAddWorkspaceToConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	cfg := &UserConfig{}
	cfg.GitHub.Username = "testuser"

	err := SaveUserConfig(configPath, cfg)
	assert.NoError(t, err)

	err = AddWorkspaceToConfig(configPath, "feature-x", "ws-123", "ready")
	assert.NoError(t, err)

	loaded, _ := LoadUserConfig(configPath)
	assert.NotEmpty(t, loaded.Workspaces)
	assert.Equal(t, "feature-x", loaded.Workspaces[0].Name)
}

func TestAddMultipleWorkspacesToConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	cfg := &UserConfig{}
	cfg.GitHub.Username = "testuser"

	err := SaveUserConfig(configPath, cfg)
	assert.NoError(t, err)

	err = AddWorkspaceToConfig(configPath, "ws1", "id1", "ready")
	assert.NoError(t, err)

	err = AddWorkspaceToConfig(configPath, "ws2", "id2", "pending")
	assert.NoError(t, err)

	loaded, _ := LoadUserConfig(configPath)
	assert.Equal(t, 2, len(loaded.Workspaces))
	assert.Equal(t, "ws1", loaded.Workspaces[0].Name)
	assert.Equal(t, "ws2", loaded.Workspaces[1].Name)
}

func TestSaveAndLoadComplexConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	cfg := &UserConfig{}
	cfg.GitHub.Username = "alice"
	cfg.GitHub.UserID = 12345
	cfg.GitHub.AvatarURL = "https://avatars.githubusercontent.com/u/12345"
	cfg.SSH.KeyPath = "~/.ssh/id_ed25519"
	cfg.SSH.PublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5..."
	cfg.Editor = "cursor"

	err := SaveUserConfig(configPath, cfg)
	assert.NoError(t, err)

	loaded, err := LoadUserConfig(configPath)
	assert.NoError(t, err)
	assert.Equal(t, "alice", loaded.GitHub.Username)
	assert.Equal(t, int64(12345), loaded.GitHub.UserID)
	assert.Equal(t, "cursor", loaded.Editor)
	assert.Equal(t, "~/.ssh/id_ed25519", loaded.SSH.KeyPath)
}
