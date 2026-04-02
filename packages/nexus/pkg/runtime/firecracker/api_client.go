package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

type apiClient struct {
	sockPath string
	http     *http.Client
}

func newAPIClient(sockPath string) *apiClient {
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sockPath)
		},
	}
	return &apiClient{
		sockPath: sockPath,
		http:     &http.Client{Transport: transport},
	}
}

func (c *apiClient) put(ctx context.Context, path string, body any) error {
	return c.request(ctx, http.MethodPut, path, body)
}

func (c *apiClient) patch(ctx context.Context, path string, body any) error {
	return c.request(ctx, http.MethodPatch, path, body)
}

func (c *apiClient) get(ctx context.Context, path string, result any) error {
	return c.requestWithResult(ctx, http.MethodGet, path, nil, result)
}

func (c *apiClient) request(ctx context.Context, method, path string, body any) error {
	return c.requestWithResult(ctx, method, path, body, nil)
}

func (c *apiClient) requestWithResult(ctx context.Context, method, path string, body any, result any) error {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	url := "http://localhost" + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("firecracker API %s %s failed: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("firecracker API %s %s returned %d: %s", method, path, resp.StatusCode, string(bodyBytes))
	}

	if result != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}
