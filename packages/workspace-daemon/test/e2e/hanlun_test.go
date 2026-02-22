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

	createCmd := exec.Command("nexus", "create", "hanlun-e2e", "--dind", "--token", "test")
	createCmd.Dir = repoPath
	require.NoError(t, createCmd.Run())
	defer exec.Command("nexus", "destroy", "hanlun-e2e", "--token", "test").Run()

	time.Sleep(2 * time.Second)
	urlCmd := exec.Command("nexus", "url", "hanlun-e2e", "--token", "test")
	urlCmd.Dir = repoPath
	urlOutput, err := urlCmd.CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(urlOutput), "localhost")

	composeCmd := exec.Command("nexus", "exec", "hanlun-e2e", "--",
		"docker-compose", "up", "-d")
	composeCmd.Dir = repoPath
	require.NoError(t, composeCmd.Run())

	time.Sleep(10 * time.Second)

	healthCmd := exec.Command("nexus", "health", "hanlun-e2e", "--token", "test")
	healthCmd.Dir = repoPath
	healthOutput, err := healthCmd.CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(healthOutput), "healthy")
}

func TestWorkspaceCreateAndDestroy(t *testing.T) {
	if os.Getenv("SKIP_E2E") != "" {
		t.Skip("Skipping E2E test")
	}

	createCmd := exec.Command("nexus", "create", "e2e-test", "--token", "test")
	output, err := createCmd.CombinedOutput()
	if err != nil {
		t.Skipf("Could not create workspace: %v\n%s", err, output)
	}
	defer exec.Command("nexus", "destroy", "e2e-test", "--token", "test").Run()

	time.Sleep(2 * time.Second)

	urlCmd := exec.Command("nexus", "url", "e2e-test", "--token", "test")
	urlOutput, err := urlCmd.CombinedOutput()
	require.NoError(t, err, "url command should succeed")
	assert.Contains(t, string(urlOutput), "localhost")
}

func TestWorkspaceExec(t *testing.T) {
	if os.Getenv("SKIP_E2E") != "" {
		t.Skip("Skipping E2E test")
	}

	createCmd := exec.Command("nexus", "create", "e2e-exec-test", "--token", "test")
	output, err := createCmd.CombinedOutput()
	if err != nil {
		t.Skipf("Could not create workspace: %v\n%s", err, output)
	}
	defer exec.Command("nexus", "destroy", "e2e-exec-test", "--token", "test").Run()

	time.Sleep(3 * time.Second)

	execCmd := exec.Command("nexus", "exec", "e2e-exec-test", "--", "echo", "hello world")
	execOutput, err := execCmd.CombinedOutput()
	require.NoError(t, err, "exec command should succeed")
	assert.Contains(t, string(execOutput), "hello world")
}
