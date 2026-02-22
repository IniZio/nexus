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

func GetSSHAgentMounts() ([]string, []string) {
	sshAuthSock, err := DetectSSHAuthSock()
	if err != nil {
		return nil, nil
	}

	binds := []string{fmt.Sprintf("%s:/ssh-agent:ro", sshAuthSock)}
	env := []string{"SSH_AUTH_SOCK=/ssh-agent"}

	usr, err := user.Current()
	if err == nil {
		sshDir := filepath.Join(usr.HomeDir, ".ssh")
		if _, err := os.Stat(sshDir); err == nil {
			binds = append(binds, fmt.Sprintf("%s:/root/.ssh:ro", sshDir))
		}
	}

	return binds, env
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
