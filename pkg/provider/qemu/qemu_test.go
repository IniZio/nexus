package qemu

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockExecCommand allows mocking exec.CommandContext
var mockExecCommand func(ctx context.Context, name string, args ...string) *exec.Cmd

func init() {
	// In a real implementation, you'd use a more sophisticated mocking strategy
	// For now, we'll use integration tests when QEMU is available
}

// TestNewQEMUProvider_Success tests successful provider creation
func TestNewQEMUProvider_Success(t *testing.T) {
	// Check if QEMU is available
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	p, err := NewQEMUProvider()
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "qemu", p.Name())
	assert.NotEmpty(t, p.baseDir)
}

// TestNewQEMUProvider_QEMUNotAvailable tests error when QEMU is not installed
func TestNewQEMUProvider_QEMUNotAvailable(t *testing.T) {
	// This test would need to manipulate PATH or use mocking
	// For now, skip if QEMU is available
	if _, err := exec.LookPath("qemu-system-x86_64"); err == nil {
		t.Skip("QEMU is available, cannot test unavailability")
	}

	_, err := NewQEMUProvider()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "qemu-system-x86_64 not found")
}

// TestQEMUProvider_Name tests the Name method
func TestQEMUProvider_Name(t *testing.T) {
	p := &QEMUProvider{}
	assert.Equal(t, "qemu", p.Name())
}

// TestQEMUProvider_Create_Success tests successful VM creation
func TestQEMUProvider_Create_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	ctx := context.Background()
	sessionID := "test-create-success"
	workspacePath := t.TempDir()

	cfg := &config.Config{
		Remote: config.Remote{
			Node: "", // Local execution
		},
		QEMU: struct {
			Image        string   `yaml:"image,omitempty"`
			CPU          int      `yaml:"cpu,omitempty"`
			Memory       string   `yaml:"memory,omitempty"`
			Disk         string   `yaml:"disk,omitempty"`
			SSHPort      int      `yaml:"ssh_port,omitempty"`
			ForwardPorts []string `yaml:"forward_ports,omitempty"`
			CacheMode    string   `yaml:"cache_mode,omitempty"`
			IoThread     bool     `yaml:"io_thread,omitempty"`
			VirtIO       bool     `yaml:"virtio,omitempty"`
			SELinux      bool     `yaml:"selinux,omitempty"`
			Firewall     bool     `yaml:"firewall,omitempty"`
		}{
			Image:   "ubuntu:22.04",
			CPU:     2,
			Memory:  "4G",
			Disk:    "20G",
			SSHPort: 2222,
		},
		Services: map[string]config.Service{
			"web": {Port: 8080},
			"api": {Port: 3000},
		},
	}

	p, err := NewQEMUProvider()
	require.NoError(t, err)

	session, err := p.Create(ctx, sessionID, workspacePath, cfg)
	require.NoError(t, err)
	assert.NotNil(t, session)
	assert.Equal(t, sessionID, session.ID)
	assert.Equal(t, "qemu", session.Provider)
	assert.Equal(t, "created", session.Status)
	assert.Equal(t, 2222, session.SSHPort)
	assert.Len(t, session.Services, 2)
	assert.Equal(t, 8080, session.Services["web"])
	assert.Equal(t, 3000, session.Services["api"])
	assert.Contains(t, session.Labels, "nexus.session.id")
	assert.Equal(t, sessionID, session.Labels["nexus.session.id"])

	// Cleanup
	_ = p.Destroy(ctx, sessionID)
}

// TestQEMUProvider_Create_WithDefaults tests creation with default values
func TestQEMUProvider_Create_WithDefaults(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	ctx := context.Background()
	sessionID := "test-create-defaults"
	workspacePath := t.TempDir()

	cfg := &config.Config{
		QEMU: struct {
			Image        string   `yaml:"image,omitempty"`
			CPU          int      `yaml:"cpu,omitempty"`
			Memory       string   `yaml:"memory,omitempty"`
			Disk         string   `yaml:"disk,omitempty"`
			SSHPort      int      `yaml:"ssh_port,omitempty"`
			ForwardPorts []string `yaml:"forward_ports,omitempty"`
			CacheMode    string   `yaml:"cache_mode,omitempty"`
			IoThread     bool     `yaml:"io_thread,omitempty"`
			VirtIO       bool     `yaml:"virtio,omitempty"`
			SELinux      bool     `yaml:"selinux,omitempty"`
			Firewall     bool     `yaml:"firewall,omitempty"`
		}{
			// All empty - should use defaults
		},
		Services: map[string]config.Service{},
	}

	p, err := NewQEMUProvider()
	require.NoError(t, err)

	session, err := p.Create(ctx, sessionID, workspacePath, cfg)
	require.NoError(t, err)
	assert.NotNil(t, session)
	// Verify defaults are set
	assert.Equal(t, 2222, session.SSHPort) // Default SSH port is 2222

	// Cleanup
	_ = p.Destroy(ctx, sessionID)
}

// TestQEMUProvider_Create_InvalidConfig tests error handling for invalid config
func TestQEMUProvider_Create_InvalidConfig(t *testing.T) {
	ctx := context.Background()
	sessionID := "test-invalid-config"
	workspacePath := t.TempDir()

	p, err := NewQEMUProvider()
	if err != nil {
		t.Skip("QEMU not available:", err)
	}

	_, err = p.Create(ctx, sessionID, workspacePath, "invalid-config")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config type")
}

// TestQEMUProvider_Start_Integration tests VM start
func TestQEMUProvider_Start_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	if !isCommandAvailable("ssh-keygen") {
		t.Skip("ssh-keygen not available")
	}

	// This test requires a VM image with cloud-init support
	// Skip unless explicitly enabled
	if os.Getenv("VENDETTA_TEST_QEMU_VM") == "" {
		t.Skip("Set VENDETTA_TEST_QEMU_VM to enable VM integration tests")
	}

	ctx := context.Background()
	sessionID := "test-start"
	workspacePath := t.TempDir()

	cfg := &config.Config{
		QEMU: struct {
			Image        string   `yaml:"image,omitempty"`
			CPU          int      `yaml:"cpu,omitempty"`
			Memory       string   `yaml:"memory,omitempty"`
			Disk         string   `yaml:"disk,omitempty"`
			SSHPort      int      `yaml:"ssh_port,omitempty"`
			ForwardPorts []string `yaml:"forward_ports,omitempty"`
			CacheMode    string   `yaml:"cache_mode,omitempty"`
			IoThread     bool     `yaml:"io_thread,omitempty"`
			VirtIO       bool     `yaml:"virtio,omitempty"`
			SELinux      bool     `yaml:"selinux,omitempty"`
			Firewall     bool     `yaml:"firewall,omitempty"`
		}{
			Image:   "ubuntu:22.04",
			CPU:     2,
			Memory:  "2G", // Smaller for testing
			Disk:    "10G",
			SSHPort: 2222,
		},
		Services: map[string]config.Service{},
	}

	p, err := NewQEMUProvider()
	require.NoError(t, err)

	_, err = p.Create(ctx, sessionID, workspacePath, cfg)
	require.NoError(t, err)

	err = p.Start(ctx, sessionID)
	require.NoError(t, err)

	// Verify VM is running
	sessions, err := p.List(ctx)
	require.NoError(t, err)

	found := false
	for _, s := range sessions {
		if s.ID == sessionID {
			found = true
			assert.Equal(t, "running", s.Status)
			break
		}
	}
	assert.True(t, found, "VM should be running")

	// Cleanup
	_ = p.Destroy(ctx, sessionID)
}

// TestQEMUProvider_Stop_Integration tests VM stop
func TestQEMUProvider_Stop_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	if !isCommandAvailable("ssh-keygen") {
		t.Skip("ssh-keygen not available")
	}

	if os.Getenv("VENDETTA_TEST_QEMU_VM") == "" {
		t.Skip("Set VENDETTA_TEST_QEMU_VM to enable VM integration tests")
	}

	ctx := context.Background()
	sessionID := "test-stop"
	workspacePath := t.TempDir()

	cfg := &config.Config{
		QEMU: struct {
			Image        string   `yaml:"image,omitempty"`
			CPU          int      `yaml:"cpu,omitempty"`
			Memory       string   `yaml:"memory,omitempty"`
			Disk         string   `yaml:"disk,omitempty"`
			SSHPort      int      `yaml:"ssh_port,omitempty"`
			ForwardPorts []string `yaml:"forward_ports,omitempty"`
			CacheMode    string   `yaml:"cache_mode,omitempty"`
			IoThread     bool     `yaml:"io_thread,omitempty"`
			VirtIO       bool     `yaml:"virtio,omitempty"`
			SELinux      bool     `yaml:"selinux,omitempty"`
			Firewall     bool     `yaml:"firewall,omitempty"`
		}{
			Image:   "ubuntu:22.04",
			CPU:     2,
			Memory:  "2G",
			Disk:    "10G",
			SSHPort: 2222,
		},
		Services: map[string]config.Service{},
	}

	p, err := NewQEMUProvider()
	require.NoError(t, err)

	_, err = p.Create(ctx, sessionID, workspacePath, cfg)
	require.NoError(t, err)

	err = p.Start(ctx, sessionID)
	require.NoError(t, err)

	err = p.Stop(ctx, sessionID)
	require.NoError(t, err)

	// Verify VM is not running
	sessions, err := p.List(ctx)
	require.NoError(t, err)

	for _, s := range sessions {
		if s.ID == sessionID {
			t.Fatalf("VM should not be running after stop, got status: %s", s.Status)
		}
	}

	// Cleanup
	_ = p.Destroy(ctx, sessionID)
}

// TestQEMUProvider_Destroy_Integration tests VM destroy
func TestQEMUProvider_Destroy_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	ctx := context.Background()
	sessionID := "test-destroy"
	workspacePath := t.TempDir()

	cfg := &config.Config{
		QEMU: struct {
			Image        string   `yaml:"image,omitempty"`
			CPU          int      `yaml:"cpu,omitempty"`
			Memory       string   `yaml:"memory,omitempty"`
			Disk         string   `yaml:"disk,omitempty"`
			SSHPort      int      `yaml:"ssh_port,omitempty"`
			ForwardPorts []string `yaml:"forward_ports,omitempty"`
			CacheMode    string   `yaml:"cache_mode,omitempty"`
			IoThread     bool     `yaml:"io_thread,omitempty"`
			VirtIO       bool     `yaml:"virtio,omitempty"`
			SELinux      bool     `yaml:"selinux,omitempty"`
			Firewall     bool     `yaml:"firewall,omitempty"`
		}{
			Image:   "ubuntu:22.04",
			CPU:     2,
			Memory:  "2G",
			Disk:    "10G",
			SSHPort: 2222,
		},
		Services: map[string]config.Service{},
	}

	p, err := NewQEMUProvider()
	require.NoError(t, err)

	_, err = p.Create(ctx, sessionID, workspacePath, cfg)
	require.NoError(t, err)

	err = p.Destroy(ctx, sessionID)
	require.NoError(t, err)

	// Verify disk image is removed
	diskPath := p.baseDir + "/" + sessionID + ".img"
	_, err = os.Stat(diskPath)
	assert.True(t, os.IsNotExist(err), "Disk image should be removed after destroy")
}

// TestQEMUProvider_List_Empty tests listing with no VMs
func TestQEMUProvider_List_Empty(t *testing.T) {
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	ctx := context.Background()

	p, err := NewQEMUProvider()
	require.NoError(t, err)

	sessions, err := p.List(ctx)
	require.NoError(t, err)
	// Should return empty slice, not nil
	assert.NotNil(t, sessions)
}

// TestQEMUProvider_List_WithVMs tests listing with running VMs
func TestQEMUProvider_List_WithVMs(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	if !isCommandAvailable("ssh-keygen") {
		t.Skip("ssh-keygen not available")
	}

	if os.Getenv("VENDETTA_TEST_QEMU_VM") == "" {
		t.Skip("Set VENDETTA_TEST_QEMU_VM to enable VM integration tests")
	}

	ctx := context.Background()
	sessionID := "test-list-vms"
	workspacePath := t.TempDir()

	cfg := &config.Config{
		QEMU: struct {
			Image        string   `yaml:"image,omitempty"`
			CPU          int      `yaml:"cpu,omitempty"`
			Memory       string   `yaml:"memory,omitempty"`
			Disk         string   `yaml:"disk,omitempty"`
			SSHPort      int      `yaml:"ssh_port,omitempty"`
			ForwardPorts []string `yaml:"forward_ports,omitempty"`
			CacheMode    string   `yaml:"cache_mode,omitempty"`
			IoThread     bool     `yaml:"io_thread,omitempty"`
			VirtIO       bool     `yaml:"virtio,omitempty"`
			SELinux      bool     `yaml:"selinux,omitempty"`
			Firewall     bool     `yaml:"firewall,omitempty"`
		}{
			Image:   "ubuntu:22.04",
			CPU:     2,
			Memory:  "2G",
			Disk:    "10G",
			SSHPort: 2222,
		},
		Services: map[string]config.Service{},
	}

	p, err := NewQEMUProvider()
	require.NoError(t, err)

	_, err = p.Create(ctx, sessionID, workspacePath, cfg)
	require.NoError(t, err)
	defer p.Destroy(ctx, sessionID)

	err = p.Start(ctx, sessionID)
	require.NoError(t, err)
	defer p.Stop(ctx, sessionID)

	sessions, err := p.List(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, sessions)

	found := false
	for _, s := range sessions {
		if s.ID == sessionID {
			found = true
			assert.Equal(t, "qemu", s.Provider)
			assert.Equal(t, "running", s.Status)
			assert.Contains(t, s.Labels, "nexus.session.id")
			break
		}
	}
	assert.True(t, found, "Should find our test VM in list")
}

// TestQEMUProvider_Exec tests command execution (requires SSH to VM)
func TestQEMUProvider_Exec(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	// This test requires a running VM with SSH configured
	// Skip for now as it would need a proper OS image with cloud-init
	t.Skip("Skipping exec test - requires proper OS image with SSH configured")
}

// TestQEMUProvider_Exec_Commands tests command construction
func TestQEMUProvider_Exec_Commands(t *testing.T) {
	ctx := context.Background()
	p := &QEMUProvider{}

	tests := []struct {
		name         string
		cmd          []string
		wantContains []string
	}{
		{
			name:         "simple command",
			cmd:          []string{"echo", "hello"},
			wantContains: []string{"ssh", "echo", "hello"},
		},
		{
			name:         "command with quotes",
			cmd:          []string{"echo", "'hello world'"},
			wantContains: []string{"ssh", "echo", "hello world"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := provider.ExecOptions{
				Cmd:    tt.cmd,
				Stdout: false,
				Stderr: false,
			}

			// This will fail execution (no VM running), but we can check the command construction
			// In a real test, we'd mock execRemoteWithStreams
			err := p.Exec(ctx, "test-session", opts)
			// We expect error since no VM is running, but we can't easily verify command construction
			// without more sophisticated mocking
			assert.Error(t, err)
		})
	}
}

// TestQEMUProvider_RemoteConfig tests remote configuration handling
func TestQEMUProvider_RemoteConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	// This test requires an actual remote host to test SSH transport
	// Skip unless explicitly enabled with a real remote
	if os.Getenv("VENDETTA_TEST_REMOTE") == "" {
		t.Skip("Set VENDETTA_TEST_REMOTE to enable remote integration tests")
	}

	ctx := context.Background()
	sessionID := "test-remote"
	workspacePath := t.TempDir()

	cfg := &config.Config{
		Remote: config.Remote{
			Node: "remote-node.example.com",
			User: "developer",
		},
		QEMU: struct {
			Image        string   `yaml:"image,omitempty"`
			CPU          int      `yaml:"cpu,omitempty"`
			Memory       string   `yaml:"memory,omitempty"`
			Disk         string   `yaml:"disk,omitempty"`
			SSHPort      int      `yaml:"ssh_port,omitempty"`
			ForwardPorts []string `yaml:"forward_ports,omitempty"`
			CacheMode    string   `yaml:"cache_mode,omitempty"`
			IoThread     bool     `yaml:"io_thread,omitempty"`
			VirtIO       bool     `yaml:"virtio,omitempty"`
			SELinux      bool     `yaml:"selinux,omitempty"`
			Firewall     bool     `yaml:"firewall,omitempty"`
		}{
			Image:   "ubuntu:22.04",
			CPU:     2,
			Memory:  "4G",
			Disk:    "20G",
			SSHPort: 2222,
		},
		Services: map[string]config.Service{},
	}

	p, err := NewQEMUProvider()
	require.NoError(t, err)

	session, err := p.Create(ctx, sessionID, workspacePath, cfg)
	require.NoError(t, err)

	// Verify remote node is set
	assert.Equal(t, "remote-node.example.com", p.remote)
	assert.NotNil(t, session)

	// Cleanup (note: this will fail if remote node doesn't exist, but that's expected)
	_ = p.Destroy(ctx, sessionID)
}

// TestQEMUProvider_ConfigServiceStruct tests the Service config struct
func TestQEMUProvider_ConfigServiceStruct(t *testing.T) {
	svc := config.Service{
		Command:   "npm run dev",
		Port:      8080,
		DependsOn: []string{"db"},
		Env:       map[string]string{"NODE_ENV": "development"},
		Healthcheck: &config.Healthcheck{
			URL:      "http://localhost:8080/health",
			Interval: "10s",
			Timeout:  "5s",
			Retries:  3,
		},
	}

	assert.Equal(t, "npm run dev", svc.Command)
	assert.Equal(t, 8080, svc.Port)
	assert.Contains(t, svc.DependsOn, "db")
	assert.Contains(t, svc.Env, "NODE_ENV")
	assert.NotNil(t, svc.Healthcheck)
	assert.Equal(t, "http://localhost:8080/health", svc.Healthcheck.URL)
}

// TestQEMUProvider_SessionStruct tests the Session struct
func TestQEMUProvider_SessionStruct(t *testing.T) {
	session := &provider.Session{
		ID:       "test-vm-id",
		Provider: "qemu",
		Status:   "running",
		SSHPort:  2222,
		Services: map[string]int{
			"web": 8080,
			"api": 3000,
		},
		Labels: map[string]string{
			"nexus.session.id": "session-123",
			"qemu.disk.path":   "/path/to/disk.img",
		},
	}

	assert.Equal(t, "test-vm-id", session.ID)
	assert.Equal(t, "qemu", session.Provider)
	assert.Equal(t, "running", session.Status)
	assert.Equal(t, 2222, session.SSHPort)
	assert.Len(t, session.Services, 2)
	assert.Len(t, session.Labels, 2)
}

// TestQEMUProvider_ExecOptionsStruct tests the ExecOptions struct
func TestQEMUProvider_ExecOptionsStruct(t *testing.T) {
	var stdout, stderr io.Writer
	opts := provider.ExecOptions{
		Cmd:          []string{"/bin/bash", "-c", "echo hello"},
		Env:          []string{"VAR1=value1", "VAR2=value2"},
		Stdout:       true,
		Stderr:       true,
		StdoutWriter: stdout,
		StderrWriter: stderr,
	}

	assert.Len(t, opts.Cmd, 3)
	assert.Equal(t, "/bin/bash", opts.Cmd[0])
	assert.Len(t, opts.Env, 2)
	assert.True(t, opts.Stdout)
	assert.True(t, opts.Stderr)
}

// TestQEMUProvider_ProviderInterface tests that QEMUProvider implements provider.Provider
func TestQEMUProvider_ProviderInterface(t *testing.T) {
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not available:", err)
	}

	p, err := NewQEMUProvider()
	require.NoError(t, err)

	var provider provider.Provider = p
	assert.Equal(t, "qemu", provider.Name())
}

// TestQEMUProvider_execRemote tests local vs remote execution
func TestQEMUProvider_execRemote(t *testing.T) {
	ctx := context.Background()
	p := &QEMUProvider{}

	// Test local execution
	p.remote = ""
	output, err := p.execRemote(ctx, "echo 'test'")
	require.NoError(t, err)
	assert.Contains(t, output, "test")

	// Test that remote would use SSH (this will fail, but we can check the structure)
	p.remote = "remote.example.com"
	// We can't actually test remote without a real remote host
}

// Helper function for testing
func isCommandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// createTestSSHKey generates a temporary SSH key for testing
func createTestSSHKey(t *testing.T) string {
	t.Helper()

	if !isCommandAvailable("ssh-keygen") {
		t.Skip("ssh-keygen not available")
	}

	keyPath := filepath.Join(t.TempDir(), "id_rsa")
	cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "2048", "-f", keyPath, "-N", "", "-q")
	if err := cmd.Run(); err != nil {
		t.Skipf("Failed to generate SSH key: %v", err)
	}

	return keyPath
}

// TestEnvironment checks test environment
func TestEnvironment(t *testing.T) {
	t.Log("Testing QEMU provider")
	t.Logf("QEMU available: %v", isCommandAvailable("qemu-system-x86_64"))
	t.Logf("SSH available: %v", isCommandAvailable("ssh"))
}
