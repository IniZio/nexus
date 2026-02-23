package ssh

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

func Execute(ctx context.Context, conn *types.SSHConnection, cmd []string) (string, error) {
	cfg := GetDaytonaSSHConfig(conn)

	keyFile, err := writeTempKey(conn.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("writing temp key: %w", err)
	}
	defer os.Remove(keyFile)

	sshCmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", fmt.Sprintf("ConnectTimeout=%d", cfg.ConnectTimeout),
		"-o", fmt.Sprintf("ServerAliveInterval=%d", cfg.ServerAliveInterval),
		"-i", keyFile,
		"-p", fmt.Sprintf("%d", cfg.Port),
		fmt.Sprintf("%s@%s", cfg.Username, cfg.Host),
	)

	if len(cmd) > 0 {
		sshCmd.Args = append(sshCmd.Args, cmd...)
	}

	output, err := sshCmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return string(output), fmt.Errorf("command failed: %s", string(exitErr.Stderr))
		}
		return "", fmt.Errorf("ssh execution: %w", err)
	}

	return string(output), nil
}

func Shell(ctx context.Context, conn *types.SSHConnection) error {
	cfg := GetDaytonaSSHConfig(conn)

	keyFile, err := writeTempKey(conn.PrivateKey)
	if err != nil {
		return fmt.Errorf("writing temp key: %w", err)
	}
	defer os.Remove(keyFile)

	sshCmd := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", fmt.Sprintf("ConnectTimeout=%d", cfg.ConnectTimeout),
		"-o", fmt.Sprintf("ServerAliveInterval=%d", cfg.ServerAliveInterval),
		"-i", keyFile,
		"-p", fmt.Sprintf("%d", cfg.Port),
		fmt.Sprintf("%s@%s", cfg.Username, cfg.Host),
	)

	sshCmd.Stdin = os.Stdin
	sshCmd.Stdout = os.Stdout
	sshCmd.Stderr = os.Stderr

	return sshCmd.Run()
}

func writeTempKey(key string) (string, error) {
	tmpFile, err := os.CreateTemp("", "nexus-ssh-key-*")
	if err != nil {
		return "", err
	}

	if _, err := tmpFile.WriteString(key); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", err
	}

	if err := tmpFile.Chmod(0600); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", err
	}

	tmpFile.Close()
	return tmpFile.Name(), nil
}

func DialTimeout(network, addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout(network, addr, timeout)
}
