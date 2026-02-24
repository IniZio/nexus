//go:build integration
// +build integration

package daytona

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestFullDaytonaLifecycle(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	ctx := context.Background()
	client, err := NewClient("", apiKey)
	require.NoError(t, err)

	sandboxName := fmt.Sprintf("test-lifecycle-%d", time.Now().Unix())
	var sandboxID string

	t.Run("CreateSandbox", func(t *testing.T) {
		req := CreateSandboxRequest{
			Name:             sandboxName,
			AutoStopInterval: 5,
		}

		sandbox, err := client.CreateSandbox(ctx, req)
		require.NoError(t, err, "Failed to create sandbox")
		assert.NotEmpty(t, sandbox.ID)
		assert.Equal(t, sandboxName, sandbox.Name)
		assert.Equal(t, "started", sandbox.State)
		sandboxID = sandbox.ID

		t.Logf("✓ Created sandbox: %s (state: %s)", sandbox.ID, sandbox.State)
		t.Logf("  Resources: CPU=%d, Memory=%dGB, Disk=%dGB, Class=%s",
			sandbox.CPU, sandbox.Memory, sandbox.Disk, sandbox.Class)
	})

	t.Cleanup(func() {
		if sandboxID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			client.DeleteSandbox(ctx, sandboxID)
			t.Logf("✓ Cleaned up sandbox: %s", sandboxID)
		}
	})

	t.Run("GetSandbox", func(t *testing.T) {
		require.NotEmpty(t, sandboxID)
		got, err := client.GetSandbox(ctx, sandboxID)
		require.NoError(t, err)
		assert.Equal(t, sandboxID, got.ID)
		assert.Equal(t, sandboxName, got.Name)
		assert.Equal(t, "started", got.State)
		t.Logf("✓ Retrieved sandbox: %s (state: %s)", got.ID, got.State)
	})

	t.Run("ListWorkspaces", func(t *testing.T) {
		workspaces, err := client.ListWorkspaces(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(workspaces), 1)
		t.Logf("✓ Found %d workspace(s)", len(workspaces))
	})

	t.Run("StopSandbox", func(t *testing.T) {
		require.NotEmpty(t, sandboxID)
		err := client.StopSandbox(ctx, sandboxID)
		require.NoError(t, err, "Failed to stop sandbox")
		t.Log("✓ Stopped sandbox")

		time.Sleep(3 * time.Second)

		got, err := client.GetSandbox(ctx, sandboxID)
		require.NoError(t, err)
		assert.Equal(t, "stopped", got.State)
		t.Logf("✓ Verified sandbox is stopped")
	})

	t.Run("StartSandbox", func(t *testing.T) {
		require.NotEmpty(t, sandboxID)
		err := client.StartSandbox(ctx, sandboxID)
		require.NoError(t, err, "Failed to start sandbox")
		t.Log("✓ Started sandbox")

		time.Sleep(5 * time.Second)

		got, err := client.GetSandbox(ctx, sandboxID)
		require.NoError(t, err)
		assert.Equal(t, "started", got.State)
		t.Logf("✓ Verified sandbox is started")
	})

	t.Run("DeleteSandbox", func(t *testing.T) {
		require.NotEmpty(t, sandboxID)
		err := client.DeleteSandbox(ctx, sandboxID)
		require.NoError(t, err, "Failed to delete sandbox")
		sandboxID = ""
		t.Log("✓ Deleted sandbox")
	})
}

func TestSSHConnection(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	ctx := context.Background()
	client, err := NewClient("", apiKey)
	require.NoError(t, err)

	sandboxName := fmt.Sprintf("test-ssh-%d", time.Now().Unix())

	t.Run("CreateSandbox", func(t *testing.T) {
		req := CreateSandboxRequest{
			Name:             sandboxName,
			AutoStopInterval: 5,
		}

		sandbox, err := client.CreateSandbox(ctx, req)
		require.NoError(t, err)
		assert.NotEmpty(t, sandbox.ID)

		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			client.DeleteSandbox(ctx, sandbox.ID)
		})

		t.Run("WaitForReady", func(t *testing.T) {
			for i := 0; i < 30; i++ {
				got, err := client.GetSandbox(ctx, sandbox.ID)
				require.NoError(t, err)
				if got.State == "started" {
					t.Logf("✓ Sandbox ready after %d seconds", i*2)
					return
				}
				time.Sleep(2 * time.Second)
			}
		})

		t.Run("CreateSSHAccess", func(t *testing.T) {
			sshAccess, err := client.CreateSSHAccess(ctx, sandbox.ID, 60)
			require.NoError(t, err, "Failed to create SSH access")
			assert.NotEmpty(t, sshAccess.Token)
			assert.True(t, !sshAccess.ExpiresAt.IsZero(), "ExpiresAt should not be zero")
			tokenLen := len(sshAccess.Token)
			if tokenLen > 50 {
				tokenLen = 50
			}
			t.Logf("✓ Got SSH token: %s...", sshAccess.Token[:tokenLen])
			t.Logf("  Token expires at: %s", sshAccess.ExpiresAt)
			t.Logf("  SSH Command: %s", sshAccess.SshCommand)

			t.Run("SSHConnectionWithToken", func(t *testing.T) {
				conn, err := ssh.Dial("tcp", "ssh.app.daytona.io:22", &ssh.ClientConfig{
					User:            sshAccess.Token,
					Auth:            []ssh.AuthMethod{},
					HostKeyCallback: ssh.InsecureIgnoreHostKey(),
					Timeout:         30 * time.Second,
				})
				require.NoError(t, err, "Failed to connect via SSH")
				defer conn.Close()

				session, err := conn.NewSession()
				require.NoError(t, err)
				defer session.Close()

				output, err := session.Output("echo 'Hello from Daytona'")
				require.NoError(t, err)

				assert.Contains(t, string(output), "Hello from Daytona")
				t.Logf("✓ SSH connection works: %s", string(output))
			})

			t.Run("BackendGetSSHConnection", func(t *testing.T) {
				for i := 0; i < 10; i++ {
					s, err := client.GetSandbox(ctx, sandbox.ID)
					require.NoError(t, err)
					if s.State == "started" || s.State == "running" {
						break
					}
					time.Sleep(500 * time.Millisecond)
				}

				backend := &DaytonaBackend{
					client:    client,
					idMapping: make(map[string]string),
				}
				backend.setDaytonaID(sandboxName, sandbox.ID)

				conn, err := backend.GetSSHConnection(ctx, sandboxName)
				require.NoError(t, err, "Failed to get SSH connection")
				assert.Equal(t, "ssh.app.daytona.io", conn.Host)
				assert.Equal(t, int32(22), conn.Port)
				assert.NotEmpty(t, conn.Username)
				assert.Empty(t, conn.PrivateKey)

				t.Logf("✓ Backend SSH connection: %s@%s:%d", conn.Username, conn.Host, conn.Port)
			})
		})
	})
}

func TestResourceConfiguration(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	ctx := context.Background()
	client, err := NewClient("", apiKey)
	require.NoError(t, err)

	sandboxName := fmt.Sprintf("test-resources-%d", time.Now().Unix())
	var sandboxID string

	t.Run("CreateWithClass", func(t *testing.T) {
		req := CreateSandboxRequest{
			Name:             sandboxName,
			AutoStopInterval: 5,
			Class:            "small",
		}

		sandbox, err := client.CreateSandbox(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, "small", sandbox.Class)

		sandboxID = sandbox.ID
		t.Logf("✓ Created sandbox with class: %s", sandbox.Class)

		t.Cleanup(func() {
			if sandboxID != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				client.DeleteSandbox(ctx, sandboxID)
			}
		})
	})

	t.Run("VerifyResources", func(t *testing.T) {
		require.NotEmpty(t, sandboxID)
		got, err := client.GetSandbox(ctx, sandboxID)
		require.NoError(t, err)
		assert.Equal(t, "small", got.Class)
		t.Logf("✓ Verified class: %s", got.Class)
	})
}

func TestErrorHandling(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	ctx := context.Background()
	client, err := NewClient("", apiKey)
	require.NoError(t, err)

	t.Run("GetNonExistentSandbox", func(t *testing.T) {
		_, err := client.GetSandbox(ctx, "non-existent-id")
		require.Error(t, err)
		t.Logf("✓ Got expected error for non-existent sandbox: %v", err)
	})

	t.Run("StopNonExistentSandbox", func(t *testing.T) {
		err := client.StopSandbox(ctx, "non-existent-id")
		require.Error(t, err)
		t.Logf("✓ Got expected error stopping non-existent sandbox: %v", err)
	})

	t.Run("StartNonExistentSandbox", func(t *testing.T) {
		err := client.StartSandbox(ctx, "non-existent-id")
		require.Error(t, err)
		t.Logf("✓ Got expected error starting non-existent sandbox: %v", err)
	})

	t.Run("DeleteNonExistentSandbox", func(t *testing.T) {
		err := client.DeleteSandbox(ctx, "non-existent-id")
		require.Error(t, err)
		t.Logf("✓ Got expected error deleting non-existent sandbox: %v", err)
	})

	t.Run("CreateDuplicateName", func(t *testing.T) {
		sandboxName := fmt.Sprintf("test-dup-%d", time.Now().Unix())

		req := CreateSandboxRequest{
			Name:             sandboxName,
			AutoStopInterval: 5,
		}

		_, err := client.CreateSandbox(ctx, req)
		require.NoError(t, err)

		_, err = client.CreateSandbox(ctx, req)
		require.Error(t, err)
		t.Logf("✓ Got expected error for duplicate name: %v", err)

		client.DeleteSandbox(ctx, sandboxName)
	})
}

func TestDaytonaBackend(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	ctx := context.Background()
	client, err := NewClient("", apiKey)
	require.NoError(t, err)

	backend := &DaytonaBackend{
		client:    client,
		idMapping: make(map[string]string),
	}

	sandboxName := fmt.Sprintf("test-backend-%d", time.Now().Unix())

	sandbox, err := client.CreateSandbox(ctx, CreateSandboxRequest{
		Name:             sandboxName,
		AutoStopInterval: 5,
	})
	require.NoError(t, err)

	backend.setDaytonaID(sandboxName, sandbox.ID)
	t.Logf("✓ Created sandbox: %s", sandbox.ID)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client.DeleteSandbox(ctx, sandbox.ID)
	})

	t.Run("GetStatus", func(t *testing.T) {
		got, err := backend.GetStatus(ctx, sandboxName)
		require.NoError(t, err)
		t.Logf("✓ Workspace status: %s", got)
	})

	t.Run("GetWorkspaceStatus", func(t *testing.T) {
		status, err := backend.GetWorkspaceStatus(ctx, sandboxName)
		require.NoError(t, err)
		t.Logf("✓ Workspace status: %s", status)
	})
}
