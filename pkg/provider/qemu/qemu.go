package qemu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/provider"
	"github.com/nexus/nexus/pkg/transport"
)

type QEMUProvider struct {
	transport.Manager
	remote  string
	baseDir string
}

func NewQEMUProvider() (*QEMUProvider, error) {
	// Check if qemu-system-x86_64 is available
	if _, err := os.Stat("/usr/bin/qemu-system-x86_64"); err != nil {
		if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
			return nil, fmt.Errorf("qemu-system-x86_64 not found: %w", err)
		}
	}
	return &QEMUProvider{
		Manager: *transport.NewManager(),
		baseDir: filepath.Join(os.Getenv("HOME"), ".nexus", "qemu"),
	}, nil
}

func (p *QEMUProvider) Name() string {
	return "qemu"
}

// Create creates a QEMU disk image and prepares VM configuration
func (p *QEMUProvider) Create(ctx context.Context, sessionID string, workspacePath string, rawConfig interface{}) (*provider.Session, error) {
	cfg, ok := rawConfig.(*config.Config)
	if !ok {
		return nil, fmt.Errorf("invalid config type")
	}

	// Set remote node if configured
	if cfg.Remote.Node != "" {
		p.remote = fmt.Sprintf("%s@%s", cfg.Remote.User, cfg.Remote.Node)
		if cfg.Remote.Port > 0 && cfg.Remote.Port != 22 {
			p.remote = fmt.Sprintf("%s -p %d", p.remote, cfg.Remote.Port)
		}

		// Register SSH transport config for remote execution
		target := cfg.Remote.Node
		if cfg.Remote.Port > 0 && cfg.Remote.Port != 22 {
			target = fmt.Sprintf("%s:%d", cfg.Remote.Node, cfg.Remote.Port)
		} else {
			target = fmt.Sprintf("%s:22", cfg.Remote.Node)
		}

		// Default SSH key path
		sshKeyPath := filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")

		sshConfig := transport.CreateDefaultSSHConfig(
			target,
			cfg.Remote.User,
			sshKeyPath,
		)
		if err := p.RegisterConfig("remote-qemu", sshConfig); err != nil {
			return nil, fmt.Errorf("failed to register SSH transport config: %w", err)
		}
	}

	// Prepare storage directory
	vmDir := filepath.Join(p.baseDir, sessionID)
	if _, err := p.execRemote(ctx, fmt.Sprintf("mkdir -p %s", vmDir)); err != nil {
		return nil, fmt.Errorf("failed to create VM directory: %w", err)
	}

	// Create disk image
	diskSize := cfg.QEMU.Disk
	if diskSize == "" {
		diskSize = "20G"
	}
	diskPath := filepath.Join(vmDir, fmt.Sprintf("%s.qcow2", sessionID))

	cmd := fmt.Sprintf("qemu-img create -f qcow2 %s %s", diskPath, diskSize)
	if _, err := p.execRemote(ctx, cmd); err != nil {
		return nil, fmt.Errorf("failed to create disk image: %w", err)
	}

	// Generate SSH key for VM access
	sshKeyPath := filepath.Join(vmDir, "id_rsa")
	if err := p.generateSSHKey(ctx, sshKeyPath); err != nil {
		return nil, fmt.Errorf("failed to generate SSH key: %w", err)
	}

	// Prepare VM launch command template
	vmCmd := p.buildQEMUCommand(sessionID, workspacePath, cfg)
	vmScriptPath := filepath.Join(vmDir, "start.sh")
	vmScript := fmt.Sprintf("#!/bin/bash\n%s", vmCmd)
	if err := p.writeFileRemote(ctx, vmScriptPath, vmScript); err != nil {
		return nil, fmt.Errorf("failed to write VM start script: %w", err)
	}
	if _, err := p.execRemote(ctx, fmt.Sprintf("chmod +x %s", vmScriptPath)); err != nil {
		return nil, fmt.Errorf("failed to make script executable: %w", err)
	}

	// Allocate SSH port
	sshPort := cfg.QEMU.SSHPort
	if sshPort == 0 {
		sshPort = 2222 // Default
	}

	// Prepare port forwarding for services
	portForwards := []string{}
	for _, svc := range cfg.Services {
		if svc.Port > 0 {
			portForwards = append(portForwards, fmt.Sprintf("tcp::%d-:%d", svc.Port, svc.Port))
		}
	}

	return &provider.Session{
		ID:       sessionID,
		Provider: p.Name(),
		Status:   "created",
		SSHPort:  sshPort,
		Services: p.extractServicePorts(cfg),
		Labels: map[string]string{
			"nexus.session.id": sessionID,
			"nexus.vm.dir":     vmDir,
			"nexus.workspace":  workspacePath,
			"nexus.remote":     p.remote,
		},
	}, nil
}

// Start launches the QEMU VM
func (p *QEMUProvider) Start(ctx context.Context, sessionID string) error {
	vmDir := filepath.Join(p.baseDir, sessionID)

	// Start VM in background with nohup
	cmd := fmt.Sprintf("cd %s && nohup bash start.sh > vm.log 2>&1 &", vmDir)
	if _, err := p.execRemote(ctx, cmd); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	// Wait for SSH to be available
	return p.waitForSSH(ctx, sessionID, 30*time.Second)
}

// Stop gracefully stops the QEMU VM
func (p *QEMUProvider) Stop(ctx context.Context, sessionID string) error {
	// Send ACPI shutdown signal
	if _, err := p.execRemote(ctx, fmt.Sprintf("echo 'system_powerdown' | nc -U %s/qemu-monitor.sock",
		filepath.Join(p.baseDir, sessionID))); err == nil {
		// Wait for graceful shutdown
		time.Sleep(10 * time.Second)
	}

	// Force kill if still running
	return p.killVM(ctx, sessionID)
}

// Destroy removes VM resources
func (p *QEMUProvider) Destroy(ctx context.Context, sessionID string) error {
	if err := p.Stop(ctx, sessionID); err != nil {
		// Continue cleanup even if stop fails
	}

	vmDir := filepath.Join(p.baseDir, sessionID)
	cmd := fmt.Sprintf("rm -rf %s", vmDir)
	if _, err := p.execRemote(ctx, cmd); err != nil {
		return fmt.Errorf("failed to remove VM: %w", err)
	}
	return nil
}

// Exec executes commands inside the VM via SSH
func (p *QEMUProvider) Exec(ctx context.Context, sessionID string, opts provider.ExecOptions) error {
	vmDir := filepath.Join(p.baseDir, sessionID)
	sshKeyPath := filepath.Join(vmDir, "id_rsa")

	cmd := strings.Join(opts.Cmd, " ")
	sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null root@localhost -p %d '%s'",
		sshKeyPath, p.getSessionSSHPort(sessionID), strings.ReplaceAll(cmd, "'", "\\'"))

	if opts.Stdout {
		if opts.StdoutWriter != nil {
			cmdExec := exec.CommandContext(ctx, "sh", "-c", sshCmd)
			cmdExec.Stdout = opts.StdoutWriter
			if opts.Stderr {
				cmdExec.Stderr = opts.StdoutWriter
			}
			return cmdExec.Run()
		}
		cmdExec := exec.CommandContext(ctx, "sh", "-c", sshCmd)
		cmdExec.Stdout = os.Stdout
		if opts.Stderr {
			cmdExec.Stderr = os.Stderr
		}
		return cmdExec.Run()
	}

	cmdExec := exec.CommandContext(ctx, "sh", "-c", sshCmd)
	output, err := cmdExec.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command failed: %w, output: %s", err, string(output))
	}
	return nil
}

// List returns all QEMU VMs managed by nexus
func (p *QEMUProvider) List(ctx context.Context) ([]provider.Session, error) {
	output, err := p.execRemote(ctx, "ps aux | grep '[q]emu-system' | grep -oE '%s-[a-z0-9_-]+' | sort -u")
	if err != nil {
		return []provider.Session{}, nil // No VMs running
	}

	sessions := make([]provider.Session, 0, 4)
	sessionIDs := strings.Split(strings.TrimSpace(output), "\n")
	for _, id := range sessionIDs {
		if id != "" {
			sessions = append(sessions, provider.Session{
				ID:       id,
				Provider: p.Name(),
				Status:   "running",
				Labels:   map[string]string{"nexus.session.id": id},
			})
		}
	}
	return sessions, nil
}

// Helper methods

func (p *QEMUProvider) execRemote(ctx context.Context, cmd string) (string, error) {
	if p.remote == "" {
		// Local execution
		execCmd := exec.CommandContext(ctx, "sh", "-c", cmd)
		output, err := execCmd.CombinedOutput()
		return string(output), err
	}

	// Remote execution via transport layer
	t, err := p.CreateTransport("remote-qemu")
	if err != nil {
		return "", fmt.Errorf("failed to create SSH transport: %w", err)
	}

	err = t.Connect(ctx, p.remote)
	if err != nil {
		return "", fmt.Errorf("failed to connect via transport: %w", err)
	}
	defer t.Disconnect(ctx)

	result, err := t.Execute(ctx, &transport.Command{
		Cmd:           []string{"sh", "-c", cmd},
		CaptureOutput: true,
	})
	if err != nil {
		return "", err
	}

	return result.Output, nil
}

func (p *QEMUProvider) buildQEMUCommand(sessionID, workspacePath string, cfg *config.Config) string {
	vmDir := filepath.Join(p.baseDir, sessionID)
	diskPath := filepath.Join(vmDir, fmt.Sprintf("%s.qcow2", sessionID))

	cpu := cfg.QEMU.CPU
	if cpu == 0 {
		cpu = 2
	}
	mem := cfg.QEMU.Memory
	if mem == "" {
		mem = "4G"
	}

	// Base QEMU command
	cmd := []string{
		"qemu-system-x86_64",
		"-hda", diskPath,
		"-m", mem,
		"-smp", fmt.Sprintf("%d", cpu),
		"-enable-kvm",
		"-cpu", "host",
		"-display", "none",
		"-daemonize",
		"-monitor", fmt.Sprintf("unix:%s/qemu-monitor.sock,server,nowait", vmDir),
	}

	// Network configuration with port forwarding
	netCfg := fmt.Sprintf("user,hostfwd=tcp::%d-:22", cfg.QEMU.SSHPort)
	for _, svc := range cfg.Services {
		if svc.Port > 0 {
			netCfg += fmt.Sprintf(",hostfwd=tcp::%d-:%d", svc.Port, svc.Port)
		}
	}
	cmd = append(cmd, "-net", "nic,model=virtio")
	cmd = append(cmd, "-net", netCfg)

	// Workspace mount (9p virtio)
	cmd = append(cmd, "-virtfs", "local,path="+workspacePath+",mount_tag=host0,security_model=mapped-xattr")
	cmd = append(cmd, "-device", "virtio-9p-pci,id=virtio-0,fsdev=fsdev-host0,mount_tag=/workspace")

	// Cloud-init config for SSH
	cmd = append(cmd, "-drive", fmt.Sprintf("file=%s/cloud-init.iso,format=raw,if=virtio,readonly=on", vmDir))

	return strings.Join(cmd, " ")
}

func (p *QEMUProvider) generateSSHKey(ctx context.Context, path string) error {
	cmd := fmt.Sprintf("ssh-keygen -t rsa -b 4096 -f %s -N '' -q", path)
	_, err := p.execRemote(ctx, cmd)
	return err
}

func (p *QEMUProvider) writeFileRemote(ctx context.Context, path, content string) error {
	if p.remote == "" {
		cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("cat > %s", path))
		cmd.Stdin = strings.NewReader(content)
		return cmd.Run()
	}

	t, err := p.CreateTransport("remote-qemu")
	if err != nil {
		return fmt.Errorf("failed to create SSH transport: %w", err)
	}

	err = t.Connect(ctx, p.remote)
	if err != nil {
		return fmt.Errorf("failed to connect via transport: %w", err)
	}
	defer t.Disconnect(ctx)

	return t.Upload(ctx, path, path)
}

func (p *QEMUProvider) waitForSSH(ctx context.Context, sessionID string, timeout time.Duration) error {
	sshPort := p.getSessionSSHPort(sessionID)
	cmd := fmt.Sprintf("ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 -o BatchMode=yes root@localhost -p %d echo ready", sshPort)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			execCmd := exec.CommandContext(ctx, "sh", "-c", cmd)
			if err := execCmd.Run(); err == nil {
				return nil
			}
			time.Sleep(2 * time.Second)
		}
	}
	return fmt.Errorf("SSH not available after %v", timeout)
}

func (p *QEMUProvider) killVM(ctx context.Context, sessionID string) error {
	output, err := p.execRemote(ctx, "ps aux | grep '[q]emu-system' | grep '%s-"+sessionID+"' | awk '{print $2}'")
	if err != nil {
		return err
	}
	pid := strings.TrimSpace(output)
	if pid != "" {
		killCmd := fmt.Sprintf("kill %s", pid)
		if _, err := p.execRemote(ctx, killCmd); err != nil {
			return fmt.Errorf("failed to kill VM: %w", err)
		}
	}
	return nil
}

func (p *QEMUProvider) getSessionSSHPort(sessionID string) int {
	// Read from session metadata or use default
	// For now, return default
	return 2222
}

func (p *QEMUProvider) extractServicePorts(cfg *config.Config) map[string]int {
	services := make(map[string]int)
	for name, svc := range cfg.Services {
		if svc.Port > 0 {
			services[name] = svc.Port
		}
	}
	return services
}
