package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type HTTPTransport struct {
	config      *Config
	client      *http.Client
	mu          sync.RWMutex
	info        *Info
	connectTime time.Time
}

func NewHTTPTransport(cfg *Config) (*HTTPTransport, error) {
	if err := validateHTTPConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid HTTP config: %w", err)
	}

	client, err := buildHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP client: %w", err)
	}

	return &HTTPTransport{
		config: cfg,
		client: client,
		info: &Info{
			Protocol:   "http",
			Target:     cfg.Target,
			Connected:  false,
			CreatedAt:  time.Now(),
			Properties: make(map[string]string),
		},
	}, nil
}

func (h *HTTPTransport) Connect(ctx context.Context, target string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if target != "" {
		h.config.Target = target
		h.info.Target = target
	}

	if err := h.testConnection(ctx); err != nil {
		return h.wrapError(err, "connection_failed")
	}

	h.info.Connected = true
	h.connectTime = time.Now()
	h.info.LastUsed = h.connectTime

	parsedURL, err := url.Parse(h.config.Target)
	if err == nil {
		h.info.Properties["scheme"] = parsedURL.Scheme
		h.info.Properties["host"] = parsedURL.Host
	}

	return nil
}

func (h *HTTPTransport) Disconnect(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.info.Connected = false
	return nil
}

func (h *HTTPTransport) IsConnected() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if !h.info.Connected {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := h.testConnection(ctx)
	return err == nil
}

func (h *HTTPTransport) Execute(ctx context.Context, cmd *Command) (*Result, error) {
	h.mu.RLock()
	client := h.client
	target := h.config.Target
	h.mu.RUnlock()

	if !h.IsConnected() {
		return nil, ErrNotConnected
	}

	execURL := fmt.Sprintf("%s/api/v1/execute", strings.TrimSuffix(target, "/"))

	payload := map[string]interface{}{
		"cmd":            cmd.Cmd,
		"env":            cmd.Env,
		"working_dir":    cmd.WorkingDir,
		"timeout":        cmd.Timeout.String(),
		"user":           cmd.User,
		"capture_output": cmd.CaptureOutput,
	}

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return nil, h.wrapError(err, "command_failed")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", execURL, &body)
	if err != nil {
		return nil, h.wrapError(err, "command_failed")
	}

	h.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	duration := time.Since(start)

	h.info.LastUsed = time.Now()

	if err != nil {
		return nil, h.wrapError(err, "connection_failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, h.wrapError(
			fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)),
			"command_failed",
		)
	}

	var result Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, h.wrapError(err, "command_failed")
	}

	result.Start = start
	result.End = start.Add(duration)
	result.Duration = duration

	return &result, nil
}

func (h *HTTPTransport) Upload(ctx context.Context, localPath, remotePath string) error {
	h.mu.RLock()
	client := h.client
	target := h.config.Target
	h.mu.RUnlock()

	if !h.IsConnected() {
		return ErrNotConnected
	}

	file, err := os.Open(localPath)
	if err != nil {
		return h.wrapError(err, "file_not_found")
	}
	defer file.Close()

	uploadURL := fmt.Sprintf("%s/api/v1/upload", strings.TrimSuffix(target, "/"))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(remotePath))
	if err != nil {
		return h.wrapError(err, "file_operation_failed")
	}

	if _, err := io.Copy(part, file); err != nil {
		return h.wrapError(err, "file_operation_failed")
	}

	if err := writer.WriteField("path", remotePath); err != nil {
		return h.wrapError(err, "file_operation_failed")
	}

	if err := writer.Close(); err != nil {
		return h.wrapError(err, "file_operation_failed")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", uploadURL, &body)
	if err != nil {
		return h.wrapError(err, "connection_failed")
	}

	h.setAuthHeaders(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return h.wrapError(err, "connection_failed")
	}
	defer resp.Body.Close()

	h.info.LastUsed = time.Now()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return h.wrapError(
			fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)),
			"file_operation_failed",
		)
	}

	return nil
}

func (h *HTTPTransport) Download(ctx context.Context, remotePath, localPath string) error {
	h.mu.RLock()
	client := h.client
	target := h.config.Target
	h.mu.RUnlock()

	if !h.IsConnected() {
		return ErrNotConnected
	}

	downloadURL := fmt.Sprintf("%s/api/v1/download?path=%s",
		strings.TrimSuffix(target, "/"),
		url.QueryEscape(remotePath))

	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return h.wrapError(err, "connection_failed")
	}

	h.setAuthHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return h.wrapError(err, "connection_failed")
	}
	defer resp.Body.Close()

	h.info.LastUsed = time.Now()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return h.wrapError(
			fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)),
			"file_not_found",
		)
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return h.wrapError(err, "permission_denied")
	}

	file, err := os.Create(localPath)
	if err != nil {
		return h.wrapError(err, "permission_denied")
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return h.wrapError(err, "file_operation_failed")
	}

	return nil
}

func (h *HTTPTransport) GetInfo() *Info {
	h.mu.RLock()
	defer h.mu.RUnlock()

	info := *h.info
	info.Connected = h.IsConnected()
	return &info
}

func validateHTTPConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}

	if cfg.Target == "" {
		return fmt.Errorf("target is required for HTTP transport")
	}

	if !strings.HasPrefix(cfg.Target, "http://") && !strings.HasPrefix(cfg.Target, "https://") {
		return fmt.Errorf("target must start with http:// or https://")
	}

	if cfg.Auth.Type == "" {
		cfg.Auth.Type = "token"
	}

	return nil
}

func buildHTTPClient(cfg *Config) (*http.Client, error) {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.Security.SkipTLSVerify,
		},
	}

	if cfg.Security.CACertPath != "" {
		caCert, err := os.ReadFile(cfg.Security.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}

		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)
		transport.TLSClientConfig.RootCAs = caCertPool
	}

	if cfg.Security.CACertData != nil {
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(cfg.Security.CACertData)
		transport.TLSClientConfig.RootCAs = caCertPool
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}

	return client, nil
}

func (h *HTTPTransport) testConnection(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", h.config.Target+"/health", nil)
	if err != nil {
		return err
	}

	h.setAuthHeaders(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed with status %d", resp.StatusCode)
	}

	return nil
}

func (h *HTTPTransport) setAuthHeaders(req *http.Request) {
	switch h.config.Auth.Type {
	case "token":
		if h.config.Auth.Token != "" {
			req.Header.Set("Authorization", "Bearer "+h.config.Auth.Token)
		}
	case "header":
		for k, v := range h.config.Auth.Headers {
			req.Header.Set(k, v)
		}
	case "certificate":
		if h.config.Auth.CertData != nil {
		}
	}
}

func (h *HTTPTransport) wrapError(err error, errorType string) error {
	if err == nil {
		return nil
	}

	retryable := false
	switch errorType {
	case "connection_failed", "timeout":
		retryable = true
	}

	return &TransportError{
		Type:      errorType,
		Message:   err.Error(),
		Retryable: retryable,
	}
}
