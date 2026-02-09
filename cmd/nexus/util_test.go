package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureSSHKeyExists(t *testing.T) {
	oldHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", oldHome)

	pubKey, keyPath, err := ensureSSHKey()
	require.NoError(t, err)

	assert.NotEmpty(t, pubKey)
	assert.NotEmpty(t, keyPath)
	assert.Contains(t, pubKey, "ssh-ed25519")

	fileInfo, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.False(t, fileInfo.IsDir())
}

func TestEnsureSSHKeyAlreadyExists(t *testing.T) {
	oldHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", oldHome)

	pubKey1, keyPath, err := ensureSSHKey()
	require.NoError(t, err)

	pubKey2, keyPath2, err := ensureSSHKey()
	require.NoError(t, err)

	assert.Equal(t, keyPath, keyPath2)
	assert.Equal(t, pubKey1, pubKey2)
}

func TestCreateSSHKeyFile(t *testing.T) {
	tmpHome := t.TempDir()
	sshDir := filepath.Join(tmpHome, ".ssh")

	err := os.MkdirAll(sshDir, 0700)
	require.NoError(t, err)

	assert.DirExists(t, sshDir)
}
