package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func DetectExistingKeys(sshDir string) (hasEd25519, hasRSA bool, err error) {
	ed25519Path := filepath.Join(sshDir, "id_ed25519")
	rsaPath := filepath.Join(sshDir, "id_rsa")

	_, err = os.Stat(ed25519Path)
	if err == nil {
		hasEd25519 = true
	} else if !os.IsNotExist(err) {
		return false, false, fmt.Errorf("failed to stat ed25519 key: %w", err)
	}

	_, err = os.Stat(rsaPath)
	if err == nil {
		hasRSA = true
	} else if !os.IsNotExist(err) {
		return false, false, fmt.Errorf("failed to stat rsa key: %w", err)
	}

	return hasEd25519, hasRSA, nil
}

func GenerateSSHKey(keyType, keyPath string) error {
	cmd := exec.Command("ssh-keygen", "-t", keyType, "-f", keyPath, "-N", "", "-C", "nexus@localhost")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to generate SSH key: %w (output: %s)", err, output)
	}

	if err := os.Chmod(keyPath, 0600); err != nil {
		return fmt.Errorf("failed to set key permissions: %w", err)
	}

	pubPath := keyPath + ".pub"
	if err := os.Chmod(pubPath, 0644); err != nil {
		return fmt.Errorf("failed to set public key permissions: %w", err)
	}

	return nil
}

func ValidateKeyPermissions(keyPath string) error {
	info, err := os.Stat(keyPath)
	if err != nil {
		return fmt.Errorf("failed to stat key: %w", err)
	}

	mode := info.Mode()
	if mode != 0600 {
		return fmt.Errorf("invalid key permissions: expected 0600 but got %#o", mode)
	}

	return nil
}

func ReadPublicKey(pubKeyPath string) (string, error) {
	data, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read public key: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

func EnsureSSHKey(sshDir string, keyType string) error {
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	keyPath := filepath.Join(sshDir, "id_"+keyType)

	if _, err := os.Stat(keyPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat key: %w", err)
	}

	return GenerateSSHKey(keyType, keyPath)
}
