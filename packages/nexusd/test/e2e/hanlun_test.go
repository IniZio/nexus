package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHanlunLMS(t *testing.T) {
	if _, err := exec.LookPath("nexus"); err != nil {
		t.Skip("nexus binary not in PATH, skipping E2E test")
	}

	if os.Getenv("SKIP_E2E") != "" {
		t.Skip("Skipping E2E test")
	}

	tmpDir := t.TempDir()

	repoPath := filepath.Join(tmpDir, "hanlun-lms")
	cmd := exec.Command("git", "clone", "--depth", "1",
		"git@github.com:oursky/hanlun-lms.git", repoPath)
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("Could not clone hanlun-lms: %v\n%s", err, output)
	}

	createCmd := exec.Command("nexus", "workspace", "create", "hanlun-e2e")
	createCmd.Dir = repoPath
	require.NoError(t, createCmd.Run())
	defer exec.Command("nexus", "workspace", "delete", "--force", "hanlun-e2e").Run()

	time.Sleep(2 * time.Second)
	statusCmd := exec.Command("nexus", "workspace", "status", "hanlun-e2e")
	statusCmd.Dir = repoPath
	statusOutput, err := statusCmd.CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(statusOutput), "ssh: ")

	execCmd := exec.Command("nexus", "workspace", "exec", "hanlun-e2e", "--",
		"docker-compose", "up", "-d")
	execCmd.Dir = repoPath
	require.NoError(t, execCmd.Run())

	time.Sleep(10 * time.Second)

	healthCmd := exec.Command("nexus", "workspace", "status", "hanlun-e2e")
	healthCmd.Dir = repoPath
	healthOutput, err := healthCmd.CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(healthOutput), "running")
}

func TestWorkspaceCreateAndDestroy(t *testing.T) {
	if _, err := exec.LookPath("nexus"); err != nil {
		t.Skip("nexus binary not in PATH, skipping E2E test")
	}

	if os.Getenv("SKIP_E2E") != "" {
		t.Skip("Skipping E2E test")
	}

	createCmd := exec.Command("nexus", "workspace", "create", "e2e-test")
	output, err := createCmd.CombinedOutput()
	if err != nil {
		t.Skipf("Could not create workspace: %v\n%s", err, output)
	}
	defer exec.Command("nexus", "workspace", "delete", "--force", "e2e-test").Run()

	time.Sleep(2 * time.Second)

	statusCmd := exec.Command("nexus", "workspace", "status", "e2e-test")
	statusOutput, err := statusCmd.CombinedOutput()
	require.NoError(t, err, "status command should succeed")
	assert.Contains(t, string(statusOutput), "ssh: ")
}

func TestWorkspaceExec(t *testing.T) {
	if _, err := exec.LookPath("nexus"); err != nil {
		t.Skip("nexus binary not in PATH, skipping E2E test")
	}

	if os.Getenv("SKIP_E2E") != "" {
		t.Skip("Skipping E2E test")
	}

	createCmd := exec.Command("nexus", "workspace", "create", "e2e-exec-test")
	output, err := createCmd.CombinedOutput()
	if err != nil {
		t.Skipf("Could not create workspace: %v\n%s", err, output)
	}
	defer exec.Command("nexus", "workspace", "delete", "--force", "e2e-exec-test").Run()

	time.Sleep(3 * time.Second)

	execCmd := exec.Command("nexus", "workspace", "exec", "e2e-exec-test", "--", "echo", "hello world")
	execOutput, err := execCmd.CombinedOutput()
	require.NoError(t, err, "exec command should succeed")
	assert.Contains(t, string(execOutput), "hello world")
}
