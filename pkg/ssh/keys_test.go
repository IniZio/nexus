package ssh

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectExistingKeys(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name            string
		setupKeys       func(string)
		expectedEd25519 bool
		expectedRSA     bool
	}{
		{
			name: "no keys exist",
			setupKeys: func(sshDir string) {
				os.MkdirAll(sshDir, 0700)
			},
			expectedEd25519: false,
			expectedRSA:     false,
		},
		{
			name: "ed25519 key exists",
			setupKeys: func(sshDir string) {
				os.MkdirAll(sshDir, 0700)
				os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("fake"), 0600)
			},
			expectedEd25519: true,
			expectedRSA:     false,
		},
		{
			name: "rsa key exists",
			setupKeys: func(sshDir string) {
				os.MkdirAll(sshDir, 0700)
				os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("fake"), 0600)
			},
			expectedEd25519: false,
			expectedRSA:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sshDir := filepath.Join(tempDir, tt.name)
			tt.setupKeys(sshDir)

			hasEd25519, hasRSA, err := DetectExistingKeys(sshDir)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedEd25519, hasEd25519)
			assert.Equal(t, tt.expectedRSA, hasRSA)
		})
	}
}

func TestGenerateSSHKey(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name     string
		keyType  string
		fileName string
		wantErr  bool
		validate func(string, string)
	}{
		{
			name:     "generate ed25519 key",
			keyType:  "ed25519",
			fileName: "id_ed25519_test",
			wantErr:  false,
			validate: func(keyPath, pubPath string) {
				assert.FileExists(t, keyPath)
				assert.FileExists(t, pubPath)

				keyInfo, _ := os.Stat(keyPath)
				assert.Equal(t, os.FileMode(0600), keyInfo.Mode())

				pubInfo, _ := os.Stat(pubPath)
				assert.Equal(t, os.FileMode(0644), pubInfo.Mode())
			},
		},
		{
			name:     "generate rsa key",
			keyType:  "rsa",
			fileName: "id_rsa_test",
			wantErr:  false,
			validate: func(keyPath, pubPath string) {
				assert.FileExists(t, keyPath)
				assert.FileExists(t, pubPath)
			},
		},
		{
			name:     "generate ecdsa key",
			keyType:  "ecdsa",
			fileName: "id_ecdsa_test",
			wantErr:  false,
			validate: func(keyPath, pubPath string) {
				assert.FileExists(t, keyPath)
				assert.FileExists(t, pubPath)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyPath := filepath.Join(tempDir, tt.fileName)
			err := GenerateSSHKey(tt.keyType, keyPath)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				pubPath := keyPath + ".pub"
				if tt.validate != nil {
					tt.validate(keyPath, pubPath)
				}
			}
		})
	}
}

func TestValidateKeyPermissions(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name    string
		setup   func(string)
		wantErr bool
		errMsg  string
	}{
		{
			name: "correct permissions",
			setup: func(keyPath string) {
				os.WriteFile(keyPath, []byte("test"), 0600)
			},
			wantErr: false,
		},
		{
			name: "wrong permissions",
			setup: func(keyPath string) {
				os.WriteFile(keyPath, []byte("test"), 0644)
			},
			wantErr: true,
			errMsg:  "permissions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyPath := filepath.Join(tempDir, tt.name)
			tt.setup(keyPath)

			err := ValidateKeyPermissions(keyPath)
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

func TestReadPublicKey(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name     string
		setup    func(string)
		wantErr  bool
		validate func(string)
	}{
		{
			name: "read valid public key",
			setup: func(pubPath string) {
				os.WriteFile(pubPath, []byte("ssh-ed25519 AAAA..."), 0644)
			},
			wantErr: false,
			validate: func(content string) {
				assert.Equal(t, "ssh-ed25519 AAAA...", content)
			},
		},
		{
			name: "missing public key",
			setup: func(pubPath string) {
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pubPath := filepath.Join(tempDir, tt.name+".pub")
			tt.setup(pubPath)

			content, err := ReadPublicKey(pubPath)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(content)
				}
			}
		})
	}
}

func TestEnsureSSHKey(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name           string
		sshDir         string
		keyType        string
		wantErr        bool
		validateExists bool
	}{
		{
			name:           "ensure ed25519 key generated",
			sshDir:         filepath.Join(tempDir, "new_ssh"),
			keyType:        "ed25519",
			wantErr:        false,
			validateExists: true,
		},
		{
			name:           "reuse existing key",
			sshDir:         filepath.Join(tempDir, "existing_ssh"),
			keyType:        "ed25519",
			wantErr:        false,
			validateExists: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EnsureSSHKey(tt.sshDir, tt.keyType)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateExists {
					keyPath := filepath.Join(tt.sshDir, "id_"+tt.keyType)
					assert.FileExists(t, keyPath)
					assert.FileExists(t, keyPath+".pub")
				}
			}

			if !tt.wantErr && tt.validateExists {
				err2 := EnsureSSHKey(tt.sshDir, tt.keyType)
				assert.NoError(t, err2)
			}
		})
	}
}
