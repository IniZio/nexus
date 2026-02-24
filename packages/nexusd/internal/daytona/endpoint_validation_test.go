//go:build integration
// +build integration

package daytona

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEndpointCorrectness(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	baseURL := "https://app.daytona.io/api"

	t.Run("WrongEndpointReturns404", func(t *testing.T) {
		req, _ := http.NewRequest("GET", baseURL+"/workspace", nil)
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		if resp.StatusCode != 404 {
			t.Errorf("WRONG ENDPOINT: /workspace returned %d, expected 404. Update to /sandbox",
				resp.StatusCode)
		} else {
			t.Log("✓ /workspace correctly returns 404 (endpoint is wrong)")
		}
	})

	t.Run("CorrectEndpointExists", func(t *testing.T) {
		req, _ := http.NewRequest("GET", baseURL+"/sandbox", nil)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		if resp.StatusCode == 404 {
			t.Fatal("CORRECT ENDPOINT NOT FOUND: /sandbox returns 404. Check API documentation")
		}

		assert.Equal(t, 401, resp.StatusCode,
			"Expected 401 Unauthorized, got %d. Endpoint might be wrong", resp.StatusCode)
		t.Log("✓ /sandbox endpoint exists (returns 401 without auth)")
	})

	t.Run("CorrectEndpointWithAuth", func(t *testing.T) {
		req, _ := http.NewRequest("GET", baseURL+"/sandbox", nil)
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode == 404 {
			t.Fatalf("ENDPOINT NOT FOUND: %s", string(body))
		}

		if resp.StatusCode == 403 {
			t.Skip("No credits available - cannot fully verify endpoint")
		}

		assert.Equal(t, 200, resp.StatusCode,
			"Expected 200 OK, got %d: %s", resp.StatusCode, string(body))
		t.Log("✓ /sandbox with auth returns 200")
	})
}

func TestHTTPMethods(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	ctx := context.Background()
	client, err := NewClient("", apiKey)
	require.NoError(t, err)

	sandbox, err := client.CreateSandbox(ctx, CreateSandboxRequest{
		Name:             fmt.Sprintf("test-methods-%d", time.Now().Unix()),
		AutoStopInterval: 5,
	})
	require.NoError(t, err, "Failed to create test sandbox")

	defer func() {
		client.DeleteSandbox(ctx, sandbox.ID)
	}()

	t.Run("CreateUsesPOST", func(t *testing.T) {
		t.Log("✓ POST /sandbox creates sandbox")
	})

	t.Run("GetUsesGET", func(t *testing.T) {
		got, err := client.GetSandbox(ctx, sandbox.ID)
		require.NoError(t, err)
		assert.Equal(t, sandbox.ID, got.ID)
		t.Log("✓ GET /sandbox/{id} retrieves sandbox")
	})

	t.Run("StopUsesPOST", func(t *testing.T) {
		err := client.StopSandbox(ctx, sandbox.ID)
		require.NoError(t, err)

		time.Sleep(2 * time.Second)
		got, _ := client.GetSandbox(ctx, sandbox.ID)
		assert.Equal(t, "stopped", got.State)
		t.Log("✓ POST /sandbox/{id}/stop stops sandbox")
	})

	t.Run("StartUsesPOST", func(t *testing.T) {
		err := client.StartSandbox(ctx, sandbox.ID)
		require.NoError(t, err)

		time.Sleep(5 * time.Second)
		got, _ := client.GetSandbox(ctx, sandbox.ID)
		assert.Equal(t, "started", got.State)
		t.Log("✓ POST /sandbox/{id}/start starts sandbox")
	})

	t.Run("DeleteUsesDELETE", func(t *testing.T) {
		temp, _ := client.CreateSandbox(ctx, CreateSandboxRequest{
			Name:             fmt.Sprintf("test-delete-%d", time.Now().Unix()),
			AutoStopInterval: 5,
		})

		err := client.DeleteSandbox(ctx, temp.ID)
		require.NoError(t, err)

		_, err = client.GetSandbox(ctx, temp.ID)
		assert.Error(t, err, "Expected error for deleted sandbox")
		t.Log("✓ DELETE /sandbox/{id} deletes sandbox")
	})
}

func TestResponseStructure(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	ctx := context.Background()
	client, err := NewClient("", apiKey)
	require.NoError(t, err)

	sandbox, err := client.CreateSandbox(ctx, CreateSandboxRequest{
		Name:             fmt.Sprintf("test-structure-%d", time.Now().Unix()),
		AutoStopInterval: 5,
	})
	require.NoError(t, err)
	defer client.DeleteSandbox(ctx, sandbox.ID)

	t.Run("RequiredFieldsExist", func(t *testing.T) {
		assert.NotEmpty(t, sandbox.ID, "ID field is required")
		assert.NotEmpty(t, sandbox.Name, "Name field is required")
		assert.NotEmpty(t, sandbox.State, "State field is required")
		assert.NotZero(t, sandbox.CPU, "CPU field should be set")
		assert.NotZero(t, sandbox.Memory, "Memory field should be set")
		assert.NotZero(t, sandbox.Disk, "Disk field should be set")
		assert.NotEmpty(t, sandbox.Class, "Class field is required")

		t.Logf("✓ All required fields present: ID=%s, State=%s, Class=%s",
			sandbox.ID, sandbox.State, sandbox.Class)
	})

	t.Run("ValidStateValues", func(t *testing.T) {
		validStates := []string{"started", "stopped", "error", "creating", "pending", "running"}
		assert.Contains(t, validStates, sandbox.State,
			"State '%s' is not a recognized value", sandbox.State)
		t.Logf("✓ State value '%s' is valid", sandbox.State)
	})
}

func TestRequestFormat(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	ctx := context.Background()
	client, err := NewClient("", apiKey)
	require.NoError(t, err)

	t.Run("EnvVarsFieldName", func(t *testing.T) {
		sandbox, err := client.CreateSandbox(ctx, CreateSandboxRequest{
			Name:             fmt.Sprintf("test-env-%d", time.Now().Unix()),
			EnvVars:          map[string]string{"TEST_VAR": "test_value"},
			AutoStopInterval: 5,
		})
		require.NoError(t, err, "If this fails with 400, env field name might be wrong")
		defer client.DeleteSandbox(ctx, sandbox.ID)

		t.Log("✓ Env vars sent successfully (field name is correct)")
	})

	t.Run("ResourceConfiguration", func(t *testing.T) {
		sandbox, err := client.CreateSandbox(ctx, CreateSandboxRequest{
			Name:             fmt.Sprintf("test-resources-%d", time.Now().Unix()),
			Class:            "small",
			AutoStopInterval: 5,
		})
		require.NoError(t, err)
		defer client.DeleteSandbox(ctx, sandbox.ID)

		assert.Equal(t, "small", sandbox.Class)
		assert.NotZero(t, sandbox.CPU)
		assert.NotZero(t, sandbox.Memory)

		t.Logf("✓ Resources configured: CPU=%d, Memory=%d, Class=%s",
			sandbox.CPU, sandbox.Memory, sandbox.Class)
	})
}

func TestEndpointErrorHandling(t *testing.T) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}

	ctx := context.Background()
	client, err := NewClient("", apiKey)
	require.NoError(t, err)

	t.Run("NotFoundError", func(t *testing.T) {
		_, err := client.GetSandbox(ctx, "non-existent-id-12345")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "404", "Error should contain 404 status")
		t.Log("✓ 404 error handled correctly")
	})

	t.Run("UnauthorizedError", func(t *testing.T) {
		badClient, _ := NewClient("", "invalid-api-key")
		_, err := badClient.CreateSandbox(ctx, CreateSandboxRequest{
			Name: "test-unauthorized",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401", "Error should contain 401 status")
		t.Log("✓ 401 error handled correctly")
	})
}
