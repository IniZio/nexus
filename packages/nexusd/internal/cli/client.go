package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nexus/nexus/packages/nexusd/internal/docker"
)

var (
	encodeBufferPool = sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}
)

func getEncodeBuffer() *bytes.Buffer {
	return encodeBufferPool.Get().(*bytes.Buffer)
}

func putEncodeBuffer(buf *bytes.Buffer) {
	buf.Reset()
	encodeBufferPool.Put(buf)
}

func jsonMarshalToBuffer(v interface{}) ([]byte, error) {
	buf := getEncodeBuffer()
	defer putEncodeBuffer(buf)
	enc := json.NewEncoder(buf)
	err := enc.Encode(v)
	if err != nil {
		return nil, err
	}
	data := make([]byte, buf.Len())
	copy(data, buf.Bytes())
	return data, nil
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type Workspace struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name"`
	Status       string            `json:"status"`
	Backend      string            `json:"backend"`
	Repository   *Repository       `json:"repository,omitempty"`
	Branch       string            `json:"branch,omitempty"`
	Ports        []PortMapping     `json:"ports,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	WorktreePath string            `json:"worktree_path,omitempty"`
	Health       *HealthStatus     `json:"health,omitempty"`
	IdleTime     time.Duration     `json:"idle_time,omitempty"`
	AutoPause    bool              `json:"auto_pause,omitempty"`
}

type Repository struct {
	URL       string `json:"url"`
	Provider  string `json:"provider,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
}

type PortMapping struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	Visibility    string `json:"visibility"`
	URL           string `json:"url,omitempty"`
}

type CreateWorkspaceRequest struct {
	Name          string            `json:"name"`
	DisplayName   string            `json:"display_name,omitempty"`
	RepositoryURL string            `json:"repository_url,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	Backend       string            `json:"backend,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	ForwardSSH    bool              `json:"forward_ssh,omitempty"`
	ID            string            `json:"id,omitempty"`
	WorktreePath  string            `json:"worktree_path,omitempty"`
	DinD          bool              `json:"dind,omitempty"`
}

type ListWorkspacesResponse struct {
	Workspaces []Workspace `json:"workspaces"`
	Total      int         `json:"total"`
}

type Config struct {
	IdleTimeout time.Duration `json:"idle_timeout"`
	AutoPause   bool          `json:"auto_pause"`
	AutoResume  bool          `json:"auto_resume"`
}

// NewClient creates a new API client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) Health() error {
	resp, err := c.http.Get(c.baseURL + "/health")
	if err != nil {
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) CreateWorkspace(req CreateWorkspaceRequest) (*Workspace, error) {
	body, err := jsonMarshalToBuffer(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return c.parseWorkspaceResponse(resp)
}

func (c *Client) ListWorkspaces() (*ListWorkspacesResponse, error) {
	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/workspaces", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, err := jsonMarshalToBuffer(apiResp.Data)
	if err != nil {
		return nil, err
	}

	var result ListWorkspacesResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *Client) GetWorkspace(id string) (*Workspace, error) {
	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/workspaces/"+id, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return c.parseWorkspaceResponse(resp)
}

func (c *Client) StartWorkspace(id string) (*Workspace, error) {
	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+id+"/start", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return c.parseWorkspaceResponse(resp)
}

func (c *Client) StopWorkspace(id string, timeoutSeconds int) (*Workspace, error) {
	req := struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}{TimeoutSeconds: timeoutSeconds}

	body, err := jsonMarshalToBuffer(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+id+"/stop", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return c.parseWorkspaceResponse(resp)
}

func (c *Client) DeleteWorkspace(id string) error {
	httpReq, err := http.NewRequest("DELETE", c.baseURL+"/api/v1/workspaces/"+id, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to delete workspace: status %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) Exec(id string, command []string) (string, error) {
	ws, err := c.GetWorkspace(id)
	if err != nil {
		return "", fmt.Errorf("getting workspace: %w", err)
	}
	id = ws.ID

	req := struct {
		Command []string `json:"command"`
	}{Command: command}

	body, err := jsonMarshalToBuffer(req)
	if err != nil {
		return "", err
	}

	url := c.baseURL + "/api/v1/workspaces/" + id + "/exec"

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("decode error: %v, body: %s", err, string(respBody))
	}

	if !apiResp.Success {
		return "", fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, err := jsonMarshalToBuffer(apiResp.Data)
	if err != nil {
		return "", err
	}

	var result struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}

	return result.Output, nil
}

func (c *Client) InjectSSHKey(id string) (string, error) {
	ws, err := c.GetWorkspace(id)
	if err != nil {
		return "", fmt.Errorf("getting workspace: %w", err)
	}
	id = ws.ID

	url := c.baseURL + "/api/v1/workspaces/" + id + "/inject-key"

	httpReq, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("decode error: %v, body: %s", err, string(respBody))
	}

	if !apiResp.Success {
		return "", fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, err := jsonMarshalToBuffer(apiResp.Data)
	if err != nil {
		return "", err
	}

	var result struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}

	return result.Output, nil
}

func (c *Client) Shell(id string) error {
	ws, err := c.GetWorkspace(id)
	if err != nil {
		return fmt.Errorf("getting workspace: %w", err)
	}

	var sshPort int
	for _, port := range ws.Ports {
		if port.Name == "ssh" {
			sshPort = port.HostPort
			break
		}
	}

	if sshPort == 0 {
		return fmt.Errorf("SSH port not found for workspace %s", id)
	}

	homeDir, _ := os.UserHomeDir()
	keyPath := filepath.Join(homeDir, ".ssh", "id_ed25519_nexus")

	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		keyPair, err := docker.GetUserSSHKey()
		if err != nil {
			return fmt.Errorf("failed to get SSH key: %w", err)
		}
		keyPath = keyPair.PrivateKeyPath
	}

	sshCmd := exec.Command("ssh",
		"-p", fmt.Sprintf("%d", sshPort),
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "RequestTTY=force",
		"root@localhost",
	)
	sshCmd.Stdin = os.Stdin
	sshCmd.Stdout = os.Stdout
	sshCmd.Stderr = os.Stderr

	return sshCmd.Run()
}

func (c *Client) GetLogs(id string, tail int) (string, error) {
	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/workspaces/"+id+"/logs?tail="+fmt.Sprint(tail), nil)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", err
	}

	if !apiResp.Success {
		return "", fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, err := jsonMarshalToBuffer(apiResp.Data)
	if err != nil {
		return "", err
	}

	var result struct {
		Logs string `json:"logs"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}

	return result.Logs, nil
}

func (c *Client) ForwardSSHAgent(workspaceID string) error {
	sshAuthSock := os.Getenv("SSH_AUTH_SOCK")
	if sshAuthSock == "" {
		return fmt.Errorf("SSH_AUTH_SOCK not set")
	}

	agentConn, err := net.Dial("unix", sshAuthSock)
	if err != nil {
		return fmt.Errorf("connecting to SSH agent: %w", err)
	}
	defer func() { _ = agentConn.Close() }()

	wsURL := strings.Replace(c.baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL += "/ws/ssh-agent?workspace=" + workspaceID

	headers := http.Header{}
	if c.token != "" {
		headers.Add("Authorization", "Bearer "+c.token)
	}

	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		return fmt.Errorf("connecting to WebSocket: %w", err)
	}
	defer func() { _ = wsConn.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := agentConn.Read(buf)
			if err != nil {
				break
			}
			_ = wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
		}
		_ = wsConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""))
	}()

	go func() {
		defer wg.Done()
		for {
			msgType, reader, err := wsConn.NextReader()
			if err != nil {
				break
			}
			if msgType == websocket.CloseMessage {
				break
			}
			if msgType != websocket.BinaryMessage {
				continue
			}
			buf := make([]byte, 4096)
			n, err := reader.Read(buf)
			if err != nil {
				break
			}
			_, _ = agentConn.Write(buf[:n])
		}
		agentConn.Close()
	}()

	fmt.Printf("SSH agent forwarded to workspace %s\n", workspaceID)
	fmt.Printf("Press Ctrl+C to stop forwarding\n")

	wg.Wait()
	return nil
}

func (c *Client) parseWorkspaceResponse(resp *http.Response) (*Workspace, error) {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, err := jsonMarshalToBuffer(apiResp.Data)
	if err != nil {
		return nil, err
	}

	var ws Workspace
	if err := json.Unmarshal(data, &ws); err != nil {
		return nil, err
	}

	return &ws, nil
}

type SyncStatus struct {
	State     string     `json:"state"`
	SessionID string     `json:"session_id,omitempty"`
	LastSync  time.Time  `json:"last_sync"`
	Conflicts []Conflict `json:"conflicts"`
}

type Conflict struct {
	Path         string `json:"path"`
	AlphaContent string `json:"alpha_content"`
	BetaContent  string `json:"beta_content"`
}

func (c *Client) GetSyncStatus(workspaceID string) (*SyncStatus, error) {
	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/sync/status", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, err := jsonMarshalToBuffer(apiResp.Data)
	if err != nil {
		return nil, err
	}

	var status SyncStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

func (c *Client) PauseSync(workspaceID string) error {
	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/sync/pause", nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) ResumeSync(workspaceID string) error {
	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/sync/resume", nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) FlushSync(workspaceID string) error {
	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/sync/flush", nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) PauseWorkspace(workspaceID string) error {
	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/pause", nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) ResumeWorkspace(workspaceID string) (*Workspace, error) {
	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/resume", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return c.parseWorkspaceResponse(resp)
}

type Checkpoint struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func (c *Client) CreateCheckpoint(workspaceID, name string) (*Checkpoint, error) {
	ws, err := c.GetWorkspace(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("getting workspace: %w", err)
	}
	workspaceID = ws.ID

	body, err := jsonMarshalToBuffer(map[string]string{"name": name})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/checkpoints", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, _ := jsonMarshalToBuffer(apiResp.Data)
	var cp Checkpoint
	json.Unmarshal(data, &cp)

	return &cp, nil
}

func (c *Client) ListCheckpoints(workspaceID string) ([]Checkpoint, error) {
	ws, err := c.GetWorkspace(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("getting workspace: %w", err)
	}
	workspaceID = ws.ID

	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/checkpoints", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, _ := jsonMarshalToBuffer(apiResp.Data)
	var checkpoints []Checkpoint
	json.Unmarshal(data, &checkpoints)

	return checkpoints, nil
}

func (c *Client) RestoreCheckpoint(workspaceID, checkpointID string) (*Workspace, error) {
	ws, err := c.GetWorkspace(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("getting workspace: %w", err)
	}
	workspaceID = ws.ID

	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/checkpoints/"+checkpointID+"/restore", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return c.parseWorkspaceResponse(resp)
}

func (c *Client) DeleteCheckpoint(workspaceID, checkpointID string) error {
	ws, err := c.GetWorkspace(workspaceID)
	if err != nil {
		return fmt.Errorf("getting workspace: %w", err)
	}
	workspaceID = ws.ID

	httpReq, err := http.NewRequest("DELETE", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/checkpoints/"+checkpointID, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to delete checkpoint: %s", resp.Status)
	}

	return nil
}

type Session struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

func (c *Client) ListSessions() ([]Session, error) {
	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/sessions", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, _ := jsonMarshalToBuffer(apiResp.Data)
	var sessions []Session
	json.Unmarshal(data, &sessions)

	return sessions, nil
}

func (c *Client) AttachSession(sessionID string) error {
	return fmt.Errorf("not implemented: session attach")
}

func (c *Client) KillSession(sessionID string) error {
	httpReq, err := http.NewRequest("DELETE", c.baseURL+"/api/v1/sessions/"+sessionID, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

type Service struct {
	Name          string `json:"name"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	Status        string `json:"status"`
	URL           string `json:"url,omitempty"`
}

func (c *Client) ListServices(workspaceID string) ([]Service, error) {
	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/services", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, _ := jsonMarshalToBuffer(apiResp.Data)
	var services []Service
	json.Unmarshal(data, &services)

	return services, nil
}

func (c *Client) GetServiceLogs(workspaceID, serviceName string, tail int) (string, error) {
	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/services/"+serviceName+"/logs?tail="+fmt.Sprint(tail), nil)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", err
	}

	if !apiResp.Success {
		return "", fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, _ := jsonMarshalToBuffer(apiResp.Data)
	var result struct {
		Logs string `json:"logs"`
	}
	json.Unmarshal(data, &result)

	return result.Logs, nil
}

func (c *Client) ForwardPort(workspaceID, port string) error {
	return fmt.Errorf("not implemented: port forwarding")
}

func (c *Client) AddPortForward(workspaceID string, containerPort int) (int, error) {
	ws, err := c.GetWorkspace(workspaceID)
	if err != nil {
		return 0, fmt.Errorf("getting workspace: %w", err)
	}
	workspaceID = ws.ID

	body, err := jsonMarshalToBuffer(map[string]int{"container_port": containerPort})
	if err != nil {
		return 0, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/ports", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return 0, err
	}

	if !apiResp.Success {
		return 0, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, _ := jsonMarshalToBuffer(apiResp.Data)
	var result struct {
		HostPort int `json:"host_port"`
	}
	json.Unmarshal(data, &result)

	return result.HostPort, nil
}

func (c *Client) ListPortForwards(workspaceID string) ([]PortMapping, error) {
	ws, err := c.GetWorkspace(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("getting workspace: %w", err)
	}
	workspaceID = ws.ID

	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/ports", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, _ := jsonMarshalToBuffer(apiResp.Data)
	var ports []PortMapping
	json.Unmarshal(data, &ports)

	return ports, nil
}

func (c *Client) RemovePortForward(workspaceID string, hostPort int) error {
	ws, err := c.GetWorkspace(workspaceID)
	if err != nil {
		return fmt.Errorf("getting workspace: %w", err)
	}
	workspaceID = ws.ID

	httpReq, err := http.NewRequest("DELETE", c.baseURL+"/api/v1/workspaces/"+workspaceID+"/ports/"+fmt.Sprint(hostPort), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

type HealthStatus struct {
	Healthy   bool          `json:"healthy"`
	Checks    []CheckResult `json:"checks"`
	LastCheck time.Time     `json:"last_check"`
}

type CheckResult struct {
	Name    string        `json:"name"`
	Healthy bool          `json:"healthy"`
	Error   string        `json:"error,omitempty"`
	Latency time.Duration `json:"latency"`
}

func (c *Client) GetHealth(workspaceID, serviceName string) (*HealthStatus, error) {
	url := c.baseURL + "/api/v1/workspaces/" + workspaceID + "/health"
	if serviceName != "" {
		url += "?service=" + serviceName
	}

	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, _ := jsonMarshalToBuffer(apiResp.Data)
	var health HealthStatus
	json.Unmarshal(data, &health)

	return &health, nil
}

func (c *Client) GetConfig() (*Config, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/config", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	data, _ := jsonMarshalToBuffer(apiResp.Data)
	var cfg Config
	json.Unmarshal(data, &cfg)

	return &cfg, nil
}

func (c *Client) SetConfig(key, value string) error {
	configMap := map[string]string{key: value}
	body, _ := jsonMarshalToBuffer(configMap)

	req, err := http.NewRequest("POST", c.baseURL+"/api/v1/config", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return err
	}

	if !apiResp.Success {
		return fmt.Errorf("API error: %s", apiResp.Error)
	}

	return nil
}
