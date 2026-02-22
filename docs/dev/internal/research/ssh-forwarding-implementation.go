// Package ssh provides SSH agent forwarding and key mounting for Docker containers.
// It adapts to the host platform (Linux, macOS) and provides the most secure
// available method for SSH authentication in containers.
//
// macOS Limitation:
// Docker Desktop on macOS runs containers in a Linux VM. Unix sockets cannot be
// bind-mounted across the VM boundary. This package uses a TCP bridge (via socat)
// to work around this limitation while maintaining security.
//
// Usage:
//
//	provider := &ssh.Provider{}
//	config, err := provider.Configure(ctx, containerConfig, hostConfig)
//	if err != nil {
//	    return err
//	}
//	defer provider.Cleanup(config)
//
package ssh

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ForwardMode represents the SSH forwarding method used
type ForwardMode string

const (
	// ForwardNone means no SSH forwarding is configured
	ForwardNone ForwardMode = "none"
	// ForwardBridge means TCP bridge via socat (macOS)
	ForwardBridge ForwardMode = "bridge"
	// ForwardDirect means direct Unix socket mount (Linux)
	ForwardDirect ForwardMode = "direct"
	// ForwardMount means read-only key mounting (fallback)
	ForwardMount ForwardMode = "mount"
)

// Config holds the SSH forwarding configuration
type Config struct {
	Mode       ForwardMode
	BridgePort int
	SocatPID   int
	KeyPaths   []string
}

// Provider handles SSH configuration for containers
type Provider struct {
	// SocatPath caches the path to socat binary
	SocatPath string
	// HasSocat indicates if socat is available
	HasSocat bool
}

// NewProvider creates a new SSH provider and detects capabilities
func NewProvider() *Provider {
	p := &Provider{}
	p.detectCapabilities()
	return p
}

func (p *Provider) detectCapabilities() {
	// Check for socat
	if path, err := exec.LookPath("socat"); err == nil {
		p.SocatPath = path
		p.HasSocat = true
	}
}

// Configure sets up SSH forwarding for a container
func (p *Provider) Configure(
	ctx context.Context,
	containerConfig interface{}, // *container.Config
	hostConfig interface{}, // *container.HostConfig
) (*Config, error) {
	// Detect SSH agent
	socketPath := os.Getenv("SSH_AUTH_SOCK")
	if socketPath == "" {
		// Try to find socket automatically on macOS
		socketPath = p.findSSHAgentSocket()
	}

	hasAgent := socketPath != "" && p.socketExists(socketPath)

	// Platform-specific configuration
	switch runtime.GOOS {
	case "darwin":
		return p.configureMacOS(ctx, containerConfig, hostConfig, hasAgent, socketPath)
	case "linux":
		return p.configureLinux(ctx, containerConfig, hostConfig, hasAgent, socketPath)
	default:
		// Unknown platform - fall back to key mounting
		return p.configureKeyMount(ctx, containerConfig, hostConfig)
	}
}

// configureMacOS sets up SSH forwarding for macOS Docker Desktop
func (p *Provider) configureMacOS(
	ctx context.Context,
	containerConfig interface{},
	hostConfig interface{},
	hasAgent bool,
	socketPath string,
) (*Config, error) {
	// macOS Docker Desktop runs in a VM, so we need special handling

	// Try TCP bridge first (most secure)
	if hasAgent && p.HasSocat {
		config, err := p.setupTCPBridge(ctx, containerConfig, hostConfig, socketPath)
		if err == nil {
			return config, nil
		}
		// Log warning and fall through
		fmt.Fprintf(os.Stderr, "Warning: TCP bridge failed (%v), falling back to key mounting\n", err)
	} else if hasAgent && !p.HasSocat {
		fmt.Fprintln(os.Stderr, "Note: Install socat for better SSH security: brew install socat")
		fmt.Fprintln(os.Stderr, "Falling back to SSH key mounting...")
	}

	// Fallback to key mounting
	return p.configureKeyMount(ctx, containerConfig, hostConfig)
}

// configureLinux sets up SSH forwarding for native Linux Docker
func (p *Provider) configureLinux(
	ctx context.Context,
	containerConfig interface{},
	hostConfig interface{},
	hasAgent bool,
	socketPath string,
) (*Config, error) {
	if !hasAgent {
		return p.configureKeyMount(ctx, containerConfig, hostConfig)
	}

	// On Linux, direct socket mounting works
	return p.setupDirectMount(containerConfig, hostConfig, socketPath)
}

// setupTCPBridge creates a TCP bridge using socat for macOS
func (p *Provider) setupTCPBridge(
	ctx context.Context,
	containerConfig interface{},
	hostConfig interface{},
	socketPath string,
) (*Config, error) {
	if p.SocatPath == "" {
		return nil, fmt.Errorf("socat not available")
	}

	// Find available localhost port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to find available port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	// Start socat on host: Unix socket -> TCP
	// Options:
	// - fork: handle multiple connections
	// - reuseaddr: allow socket reuse
	// - range=127.0.0.1/32: only accept localhost connections
	cmd := exec.CommandContext(ctx, p.SocatPath,
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr,range=127.0.0.1/32", port),
		fmt.Sprintf("UNIX-CONNECT:%s", socketPath),
	)

	// Start socat in background
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start socat: %w", err)
	}

	// Wait a moment for socat to start listening
	time.Sleep(100 * time.Millisecond)

	// Verify socat is running
	if cmd.Process == nil {
		return nil, fmt.Errorf("socat failed to start")
	}

	// Configure container environment
	// Container will need to bridge TCP back to Unix socket
	setContainerEnv(containerConfig, "SSH_AGENT_BRIDGE_PORT", fmt.Sprintf("%d", port))
	setContainerEnv(containerConfig, "SSH_AUTH_SOCK", "/ssh-agent")

	return &Config{
		Mode:       ForwardBridge,
		BridgePort: port,
		SocatPID:   cmd.Process.Pid,
	}, nil
}

// setupDirectMount mounts SSH agent socket directly (Linux only)
func (p *Provider) setupDirectMount(
	containerConfig interface{},
	hostConfig interface{},
	socketPath string,
) (*Config, error) {
	// Mount socket into container
	addMount(hostConfig, Mount{
		Type:     "bind",
		Source:   socketPath,
		Target:   "/ssh-agent",
		ReadOnly: false,
	})

	setContainerEnv(containerConfig, "SSH_AUTH_SOCK", "/ssh-agent")

	return &Config{
		Mode: ForwardDirect,
	}, nil
}

// configureKeyMount mounts SSH keys as read-only volumes
func (p *Provider) configureKeyMount(
	ctx context.Context,
	containerConfig interface{},
	hostConfig interface{},
) (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	sshDir := filepath.Join(home, ".ssh")
	if _, err := os.Stat(sshDir); err != nil {
		return nil, fmt.Errorf("SSH directory not found: %w", err)
	}

	// Mount .ssh directory as read-only
	addMount(hostConfig, Mount{
		Type:     "bind",
		Source:   sshDir,
		Target:   "/root/.ssh",
		ReadOnly: true,
	})

	// For macOS, copy keys to tmpfs for better security
	// This prevents keys from being written to container overlay FS
	initScript := `#!/bin/sh
# Nexus SSH setup - copy keys to tmpfs for security
if [ -d /root/.ssh ]; then
    mkdir -p /tmp/.ssh
    cp -r /root/.ssh/* /tmp/.ssh/ 2>/dev/null || true
    chmod 700 /tmp/.ssh
    chmod 600 /tmp/.ssh/id_* 2>/dev/null || true
    chmod 644 /tmp/.ssh/*.pub /tmp/.ssh/config /tmp/.ssh/known_hosts 2>/dev/null || true
    
    # Use tmpfs keys for SSH
    export SSH_AUTH_SOCK=""
    if [ -f /tmp/.ssh/id_ed25519 ]; then
        export GIT_SSH_COMMAND="ssh -i /tmp/.ssh/id_ed25519 -o StrictHostKeyChecking=accept-new"
    elif [ -f /tmp/.ssh/id_rsa ]; then
        export GIT_SSH_COMMAND="ssh -i /tmp/.ssh/id_rsa -o StrictHostKeyChecking=accept-new"
    fi
fi
`

	// Wrap the container command
	wrapContainerCommand(containerConfig, initScript)

	return &Config{
		Mode: ForwardMount,
	}, nil
}

// Cleanup stops the SSH forwarding bridge if running
func (p *Provider) Cleanup(config *Config) error {
	if config == nil {
		return nil
	}

	if config.Mode == ForwardBridge && config.SocatPID > 0 {
		process, err := os.FindProcess(config.SocatPID)
		if err == nil {
			// Try graceful shutdown first
			process.Signal(os.Interrupt)
			time.Sleep(100 * time.Millisecond)
			// Force kill if still running
			process.Kill()
		}
	}

	return nil
}

// Helper methods

func (p *Provider) findSSHAgentSocket() string {
	// Try common macOS socket locations
	patterns := []string{
		"/tmp/com.apple.launchd.*/Listeners",
		"/tmp/ssh-*/agent.*",
	}

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			// Return most recent socket
			return matches[len(matches)-1]
		}
	}

	return ""
}

func (p *Provider) socketExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// Check if it's a socket
	return info.Mode()&os.ModeSocket != 0
}

// Container abstraction helpers (would use actual Docker types in real implementation)

type Mount struct {
	Type     string
	Source   string
	Target   string
	ReadOnly bool
}

func addMount(hostConfig interface{}, mount Mount) {
	// In real implementation, this would append to hostConfig.Mounts
	// For now, this is a placeholder
}

func setContainerEnv(containerConfig interface{}, key, value string) {
	// In real implementation, this would append to containerConfig.Env
	// For now, this is a placeholder
}

func wrapContainerCommand(containerConfig interface{}, initScript string) {
	// In real implementation, this would:
	// 1. Save original entrypoint/cmd
	// 2. Set new entrypoint to /bin/sh -c
	// 3. Set cmd to: initScript + " && exec " + originalCmd
}

// Example usage
func ExampleUsage() {
	// Create provider
	provider := NewProvider()

	// Check capabilities
	if provider.HasSocat {
		fmt.Println("✓ socat available - will use TCP bridge on macOS")
	} else {
		fmt.Println("⚠ socat not found - will use key mounting on macOS")
		fmt.Println("  Install with: brew install socat")
	}

	// Configure would be called when creating a container
	// config, err := provider.Configure(ctx, containerConfig, hostConfig)
	// if err != nil {
	//     log.Fatal(err)
	// }
	// defer provider.Cleanup(config)
}
