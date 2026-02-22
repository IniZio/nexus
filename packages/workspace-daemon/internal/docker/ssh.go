package docker

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

type SSHConfig struct {
	Mode string
	Keys []string
}

type SSHKeyPair struct {
	PrivateKeyPath string
	PublicKeyPath  string
}

func DetectSSHAuthSock() (string, error) {
	sshAuthSock := os.Getenv("SSH_AUTH_SOCK")
	if sshAuthSock == "" {
		return "", fmt.Errorf("SSH_AUTH_SOCK not set")
	}

	if _, err := os.Stat(sshAuthSock); err != nil {
		return "", fmt.Errorf("SSH_AUTH_SOCK not accessible: %w", err)
	}

	return sshAuthSock, nil
}

func GetSSHAgentMounts() ([]VolumeMount, []string) {
	return nil, nil
}

func GetSSHKeyMounts(keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	binds := []string{}
	usr, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("getting current user: %w", err)
	}

	for _, key := range keys {
		hostKeyPath := filepath.Join(usr.HomeDir, ".ssh", key)
		if _, err := os.Stat(hostKeyPath); err != nil {
			continue
		}
		containerKeyPath := fmt.Sprintf("/root/.ssh/%s", filepath.Base(key))
		binds = append(binds, fmt.Sprintf("%s:%s:ro", hostKeyPath, containerKeyPath))
	}

	return binds, nil
}

func GetHomeDir() (string, error) {
	usr, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("getting current user: %w", err)
	}
	return usr.HomeDir, nil
}

func GetUserSSHKey() (*SSHKeyPair, error) {
	homeDir, err := GetHomeDir()
	if err != nil {
		return nil, err
	}

	keyPath := filepath.Join(homeDir, ".ssh", "id_ed25519_nexus")
	pubKeyPath := keyPath + ".pub"

	if _, err := os.Stat(keyPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("SSH key not found at %s (run: ssh-keygen -t ed25519 -f %s)", keyPath, keyPath)
		}
		return nil, fmt.Errorf("checking SSH key: %w", err)
	}

	return &SSHKeyPair{
		PrivateKeyPath: keyPath,
		PublicKeyPath:  pubKeyPath,
	}, nil
}

func GetUserPublicKeys() ([]string, error) {
	homeDir, err := GetHomeDir()
	if err != nil {
		return nil, err
	}

	sshDir := filepath.Join(homeDir, ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return nil, fmt.Errorf("reading SSH directory: %w", err)
	}

	var keys []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) > 4 && name[len(name)-4:] == ".pub" {
			keyPath := filepath.Join(sshDir, name)
			content, err := os.ReadFile(keyPath)
			if err != nil {
				continue
			}
			keys = append(keys, string(content))
		}
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("no public keys found in %s", sshDir)
	}

	return keys, nil
}

func generateSSHEntrypoint(publicKey string) string {
	keyEnv := ""
	if publicKey != "" {
		keyEnv = fmt.Sprintf(`
useradd -m -s /bin/bash nexus || true
echo 'nexus ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers.d/nexus
mkdir -p /home/nexus/.ssh
chmod 700 /home/nexus/.ssh
echo '%s' > /home/nexus/.ssh/authorized_keys
chmod 600 /home/nexus/.ssh/authorized_keys
chown -R nexus:nexus /home/nexus/.ssh
chown -R nexus:nexus /workspace
`, publicKey)
	}

	return fmt.Sprintf(`#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq openssh-server sudo git curl wget vim nano > /dev/null 2>&1

mkdir -p /var/run/sshd
mkdir -p /workspace

%s

sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config

/usr/sbin/sshd

tail -f /dev/null
`, keyEnv)
}
