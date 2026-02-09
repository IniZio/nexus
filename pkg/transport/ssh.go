package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHTransport struct {
	config      *Config
	sshConfig   *ssh.ClientConfig
	client      *ssh.Client
	mu          sync.RWMutex
	info        *Info
	connectTime time.Time
}

func NewSSHTransport(cfg *Config) (*SSHTransport, error) {
	if err := validateSSHConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid SSH config: %w", err)
	}

	sshConfig, err := buildSSHConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build SSH config: %w", err)
	}

	return &SSHTransport{
		config:    cfg,
		sshConfig: sshConfig,
		info: &Info{
			Protocol:   "ssh",
			Target:     cfg.Target,
			Connected:  false,
			CreatedAt:  time.Now(),
			Properties: make(map[string]string),
		},
	}, nil
}

func (s *SSHTransport) Connect(ctx context.Context, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil && s.IsConnected() {
		return nil
	}

	if target != "" {
		s.config.Target = target
		s.info.Target = target
	}

	host, port, err := parseSSHTarget(s.config.Target)
	if err != nil {
		return fmt.Errorf("failed to parse SSH target: %w", err)
	}

	if s.config.Timeout == 0 {
		s.config.Timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		client, err := ssh.Dial("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), s.sshConfig)
		if err != nil {
			done <- fmt.Errorf("SSH dial failed: %w", err)
			return
		}
		s.client = client
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			return s.wrapError(err, "connection_failed")
		}
	case <-ctx.Done():
		return ErrTimeout
	}

	s.info.Connected = true
	s.connectTime = time.Now()
	s.info.LastUsed = s.connectTime
	s.info.Properties["host"] = host
	s.info.Properties["port"] = fmt.Sprintf("%d", port)
	s.info.Properties["username"] = s.config.Auth.Username

	return nil
}

func (s *SSHTransport) Disconnect(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client == nil {
		return nil
	}

	err := s.client.Close()
	s.client = nil
	s.info.Connected = false

	return err
}

func (s *SSHTransport) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.client == nil {
		return false
	}

	_, _, err := s.client.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

func (s *SSHTransport) Execute(ctx context.Context, cmd *Command) (*Result, error) {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()

	if client == nil {
		return nil, ErrNotConnected
	}

	session, err := client.NewSession()
	if err != nil {
		return nil, s.wrapError(err, "command_failed")
	}
	defer session.Close()

	if cmd.WorkingDir != "" {
		fullCmd := fmt.Sprintf("cd %s && %s", cmd.WorkingDir, strings.Join(cmd.Cmd, " "))
		cmd.Cmd = []string{"sh", "-c", fullCmd}
	}

	for k, v := range cmd.Env {
		if err := session.Setenv(k, v); err != nil {
			return nil, fmt.Errorf("failed to set environment variable %s: %w", k, err)
		}
	}

	if cmd.Stdin != nil {
		session.Stdin = cmd.Stdin
	}

	var stdout, stderr strings.Builder
	if cmd.Stdout != nil {
		session.Stdout = cmd.Stdout
	} else if cmd.CaptureOutput {
		session.Stdout = &stdout
	}

	if cmd.Stderr != nil {
		session.Stderr = cmd.Stderr
	} else if cmd.CaptureOutput {
		session.Stderr = &stderr
	}

	timeout := cmd.Timeout
	if timeout == 0 {
		timeout = s.config.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
	}

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- session.Run(strings.Join(cmd.Cmd, " "))
	}()

	select {
	case err := <-done:
		duration := time.Since(start)
		s.info.LastUsed = time.Now()

		result := &Result{
			Start:    start,
			End:      start.Add(duration),
			Duration: duration,
		}

		if cmd.CaptureOutput {
			result.Output = stdout.String()
			if stderr.String() != "" {
				if result.Output != "" {
					result.Output += "\n"
				}
				result.Output += stderr.String()
			}
		}

		if err != nil {
			if exitError, ok := err.(*ssh.ExitError); ok {
				result.ExitCode = exitError.ExitStatus()
				result.Error = err.Error()
			} else {
				return nil, s.wrapError(err, "command_failed")
			}
		}

		return result, nil

	case <-ctx.Done():
		return nil, ErrTimeout
	}
}

func (s *SSHTransport) Upload(ctx context.Context, localPath, remotePath string) error {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()

	if client == nil {
		return ErrNotConnected
	}

	localFile, err := os.Open(localPath)
	if err != nil {
		return s.wrapError(err, "file_not_found")
	}
	defer localFile.Close()

	session, err := client.NewSession()
	if err != nil {
		return s.wrapError(err, "connection_failed")
	}
	defer session.Close()

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return s.wrapError(err, "connection_failed")
	}

	go func() {
		defer stdinPipe.Close()
		io.Copy(stdinPipe, localFile)
	}()

	cmd := fmt.Sprintf("cat > %s", remotePath)
	err = session.Run(cmd)
	if err != nil {
		return s.wrapError(err, "file_operation_failed")
	}

	s.info.LastUsed = time.Now()
	return nil
}

func (s *SSHTransport) Download(ctx context.Context, remotePath, localPath string) error {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()

	if client == nil {
		return ErrNotConnected
	}

	session, err := client.NewSession()
	if err != nil {
		return s.wrapError(err, "connection_failed")
	}
	defer session.Close()

	output, err := session.Output(fmt.Sprintf("cat %s", remotePath))
	if err != nil {
		return s.wrapError(err, "file_not_found")
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return s.wrapError(err, "permission_denied")
	}

	err = os.WriteFile(localPath, output, 0644)
	if err != nil {
		return s.wrapError(err, "permission_denied")
	}

	s.info.LastUsed = time.Now()
	return nil
}

func (s *SSHTransport) GetInfo() *Info {
	s.mu.RLock()
	defer s.mu.RUnlock()

	info := *s.info
	if s.client != nil {
		info.Connected = s.IsConnected()
	}
	return &info
}

func validateSSHConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}

	if cfg.Auth.Type == "" {
		cfg.Auth.Type = "ssh_key"
	}

	if cfg.Auth.Username == "" {
		return fmt.Errorf("username is required for SSH transport")
	}

	switch cfg.Auth.Type {
	case "ssh_key":
		if cfg.Auth.KeyPath == "" && len(cfg.Auth.KeyData) == 0 {
			return fmt.Errorf("key_path or key_data is required for SSH key authentication")
		}
	case "password":
		if cfg.Auth.Password == "" {
			return fmt.Errorf("password is required for password authentication")
		}
	default:
		return fmt.Errorf("unsupported authentication type: %s", cfg.Auth.Type)
	}

	return nil
}

func buildSSHConfig(cfg *Config) (*ssh.ClientConfig, error) {
	sshConfig := &ssh.ClientConfig{
		User:            cfg.Auth.Username,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         cfg.Timeout,
	}

	if cfg.Security.StrictHostKeyChecking {
		sshConfig.HostKeyCallback = ssh.FixedHostKey(nil)
	}

	switch cfg.Auth.Type {
	case "ssh_key":
		var keySigner ssh.Signer
		var err error

		if cfg.Auth.KeyData != nil {
			keySigner, err = ssh.ParsePrivateKey(cfg.Auth.KeyData)
		} else {
			keyData, err := os.ReadFile(cfg.Auth.KeyPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read SSH key: %w", err)
			}
			keySigner, err = ssh.ParsePrivateKey(keyData)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to parse SSH key: %w", err)
		}

		sshConfig.Auth = []ssh.AuthMethod{
			ssh.PublicKeys(keySigner),
		}

	case "password":
		sshConfig.Auth = []ssh.AuthMethod{
			ssh.Password(cfg.Auth.Password),
		}
	}

	return sshConfig, nil
}

func parseSSHTarget(target string) (host string, port int, err error) {
	if target == "" {
		return "", 0, fmt.Errorf("target cannot be empty")
	}

	if !strings.Contains(target, ":") {
		return target, 22, nil
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return "", 0, fmt.Errorf("invalid target format: %w", err)
	}

	if portStr == "" {
		return "", 0, fmt.Errorf("invalid target format: port is required")
	}

	port, err = net.LookupPort("tcp", portStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port: %w", err)
	}

	return host, port, nil
}

func (s *SSHTransport) wrapError(err error, errorType string) error {
	if err == nil {
		return nil
	}

	retryable := false
	switch errorType {
	case "connection_failed", "timeout":
		retryable = true
	}

	if sshErr, ok := err.(*ssh.ExitError); ok {
		errorType = "command_failed"
		retryable = false
		return &TransportError{
			Type:      errorType,
			Message:   sshErr.Error(),
			Code:      sshErr.ExitStatus(),
			Retryable: retryable,
		}
	}

	return &TransportError{
		Type:      errorType,
		Message:   err.Error(),
		Retryable: retryable,
	}
}
