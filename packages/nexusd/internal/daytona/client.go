package daytona

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	DefaultAPIURL = "https://app.daytona.io/api"
)

type Client struct {
	apiURL string
	apiKey string
	http   *http.Client
}

func NewClient(apiURL, apiKey string) (*Client, error) {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}

	if err := ValidateAPIKey(apiKey); err != nil {
		return nil, err
	}

	return &Client{
		apiURL: apiURL,
		apiKey: apiKey,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (c *Client) CreateSandbox(ctx context.Context, req CreateSandboxRequest) (*Sandbox, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := fmt.Sprintf("%s/sandbox", c.apiURL)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("creating sandbox: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create sandbox failed: %d - %s", resp.StatusCode, string(body))
	}

	var sandbox Sandbox
	if err := json.NewDecoder(resp.Body).Decode(&sandbox); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &sandbox, nil
}

func (c *Client) GetSandbox(ctx context.Context, id string) (*Sandbox, error) {
	url := fmt.Sprintf("%s/sandbox/%s", c.apiURL, id)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("getting sandbox: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get sandbox failed: %d - %s", resp.StatusCode, string(body))
	}

	var sandbox Sandbox
	if err := json.NewDecoder(resp.Body).Decode(&sandbox); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &sandbox, nil
}

func (c *Client) StartSandbox(ctx context.Context, id string) error {
	url := fmt.Sprintf("%s/sandbox/%s/start", c.apiURL, id)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return err
	}

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("starting sandbox: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("start sandbox failed: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) StopSandbox(ctx context.Context, id string) error {
	url := fmt.Sprintf("%s/sandbox/%s/stop", c.apiURL, id)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return err
	}

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("stopping sandbox: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stop sandbox failed: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) DeleteSandbox(ctx context.Context, id string) error {
	url := fmt.Sprintf("%s/sandbox/%s", c.apiURL, id)

	httpReq, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("deleting sandbox: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete sandbox failed: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) ListWorkspaces(ctx context.Context) ([]Sandbox, error) {
	url := fmt.Sprintf("%s/sandbox", c.apiURL)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list workspaces failed: %d - %s", resp.StatusCode, string(body))
	}

	var workspaces []Sandbox
	if err := json.NewDecoder(resp.Body).Decode(&workspaces); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return workspaces, nil
}

func (c *Client) GetSandboxByName(ctx context.Context, name string) (*Sandbox, error) {
	workspaces, err := c.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}

	for _, ws := range workspaces {
		if ws.Name == name {
			return &ws, nil
		}
	}

	return nil, fmt.Errorf("sandbox not found: %s", name)
}

func (c *Client) CreateSSHAccess(ctx context.Context, sandboxID string, expiresInMinutes int) (*SSHTokenResponse, error) {
	type createSSHAccessRequest struct {
		ExpiresInMinutes int `json:"expiresInMinutes,omitempty"`
	}

	req := createSSHAccessRequest{ExpiresInMinutes: expiresInMinutes}
	if req.ExpiresInMinutes == 0 {
		req.ExpiresInMinutes = 60
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := fmt.Sprintf("%s/sandbox/%s/ssh-access", c.apiURL, sandboxID)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("creating SSH access: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create SSH access failed: %d - %s", resp.StatusCode, string(respBody))
	}

	var tokenResp SSHTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &tokenResp, nil
}
