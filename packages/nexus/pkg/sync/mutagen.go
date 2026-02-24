package sync

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type MutagenSession struct {
	ID        string
	AlphaPath string
	BetaPath  string
	Config    MutagenConfig
}

type MutagenConfig struct {
	Mode          string        `yaml:"mode"`
	Exclude       []string      `yaml:"exclude"`
	WatchInterval time.Duration `yaml:"watchInterval"`
}

type MutagenClient struct {
	binaryPath string
}

func NewMutagenClient() *MutagenClient {
	return &MutagenClient{
		binaryPath: "mutagen",
	}
}

func (c *MutagenClient) CreateSession(alpha, beta string, config MutagenConfig) (*MutagenSession, error) {
	if config.Mode == "" {
		config.Mode = "two-way-safe"
	}

	if config.WatchInterval == 0 {
		config.WatchInterval = 1 * time.Second
	}

	sessionName := fmt.Sprintf("nexus-%d", time.Now().UnixNano())

	args := []string{
		"sync", "create",
		"--name", sessionName,
		"--sync-mode", config.Mode,
		"--watch-polling-interval", fmt.Sprintf("%.0f", config.WatchInterval.Seconds()),
	}

	args = append(args, alpha, beta)

	cmd := exec.Command(c.binaryPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to create mutagen session: %w: %s", err, stderr.String())
	}

	session := &MutagenSession{
		ID:        sessionName,
		AlphaPath: alpha,
		BetaPath:  beta,
		Config:    config,
	}

	return session, nil
}

func (c *MutagenClient) PauseSession(id string) error {
	cmd := exec.Command(c.binaryPath, "sync", "pause", id)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to pause mutagen session: %w: %s", err, stderr.String())
	}

	return nil
}

func (c *MutagenClient) ResumeSession(id string) error {
	cmd := exec.Command(c.binaryPath, "sync", "resume", id)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to resume mutagen session: %w: %s", err, stderr.String())
	}

	return nil
}

func (c *MutagenClient) TerminateSession(id string) error {
	cmd := exec.Command(c.binaryPath, "sync", "terminate", id)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to terminate mutagen session: %w: %s", err, stderr.String())
	}

	return nil
}

func (c *MutagenClient) GetStatus(id string) (*SyncStatus, error) {
	cmd := exec.Command(c.binaryPath, "sync", "list", id)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "no sessions found") || strings.Contains(stderr.String(), "unknown") {
			return nil, fmt.Errorf("session not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get mutagen status: %w: %s", err, stderr.String())
	}

	output := stdout.String()
	status := &SyncStatus{
		State:     "unknown",
		Conflicts: []Conflict{},
	}

	if strings.Contains(output, "Connected: Yes") {
		status.State = "connected"
	}
	if strings.Contains(output, "Staging files") {
		status.State = "staging"
	} else if strings.Contains(output, "Watching for changes") {
		status.State = "watching"
	} else if strings.Contains(output, "Transition problems") {
		status.State = "error"
	}

	return status, nil
}

func (c *MutagenClient) Flush(id string) error {
	cmd := exec.Command(c.binaryPath, "sync", "flush", id)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to flush mutagen session: %w: %s", err, stderr.String())
	}

	return nil
}

func (c *MutagenClient) ListSessions() ([]MutagenSession, error) {
	cmd := exec.Command(c.binaryPath, "sync", "list")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "no sessions found") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list mutagen sessions: %w: %s", err, stderr.String())
	}

	output := stdout.String()
	if output == "No synchronization sessions found" {
		return nil, nil
	}

	var result []MutagenSession
	lines := strings.Split(output, "\n")
	var currentSession *MutagenSession

	for _, line := range lines {
		if strings.HasPrefix(line, "Name:") {
			currentSession = &MutagenSession{}
			result = append(result, *currentSession)
		} else if strings.HasPrefix(line, "Identifier:") && currentSession != nil {
			id := strings.TrimSpace(strings.TrimPrefix(line, "Identifier:"))
			currentSession.ID = id
		} else if strings.HasPrefix(line, "Alpha:") && currentSession != nil {
			currentSession.AlphaPath = ""
		} else if strings.HasPrefix(line, "\tURL:") && currentSession != nil && currentSession.AlphaPath == "" {
			currentSession.AlphaPath = strings.TrimSpace(strings.TrimPrefix(line, "\tURL:"))
		} else if strings.HasPrefix(line, "Beta:") && currentSession != nil {
			currentSession.BetaPath = ""
		} else if strings.HasPrefix(line, "\tURL:") && currentSession != nil && currentSession.BetaPath == "" {
			currentSession.BetaPath = strings.TrimSpace(strings.TrimPrefix(line, "\tURL:"))
		}
	}

	return result, nil
}

type mutagenEndpoint struct {
	Path string `json:"path"`
}

type mutagenConflict struct {
	Path         string `json:"path"`
	AlphaContent string `json:"alphaContent"`
	BetaContent  string `json:"betaContent"`
}

type SyncStatus struct {
	State     string
	Conflicts []Conflict
	LastSync  time.Time
}

type Conflict struct {
	Path         string
	AlphaContent string
	BetaContent  string
}

type Manager struct {
	client     *MutagenClient
	config     *Config
	stateStore StateStore
}

type Config struct {
	Provider string   `yaml:"provider"`
	Mode     string   `yaml:"mode"`
	Exclude  []string `yaml:"exclude"`
}

type StateStore interface {
	SaveSessionID(ctx context.Context, workspaceName, sessionID string) error
	GetSessionID(ctx context.Context, workspaceName string) (string, error)
	DeleteSessionID(ctx context.Context, workspaceName string) error
}

func NewManager(config *Config, store StateStore) *Manager {
	return &Manager{
		client:     NewMutagenClient(),
		config:     config,
		stateStore: store,
	}
}

func (m *Manager) StartSync(ctx context.Context, workspaceName, worktreePath, containerPath string) (string, error) {
	sessionID, err := m.stateStore.GetSessionID(ctx, workspaceName)
	if err != nil {
		return "", err
	}

	if sessionID != "" {
		status, err := m.client.GetStatus(sessionID)
		if err == nil && status.State == "watching" {
			return sessionID, nil
		}
	}

	alphaPath := worktreePath
	betaPath := containerPath

	mutagenConfig := MutagenConfig{
		Mode:          m.config.Mode,
		Exclude:       m.config.Exclude,
		WatchInterval: 1 * time.Second,
	}

	session, err := m.client.CreateSession(alphaPath, betaPath, mutagenConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create sync session: %w", err)
	}

	if err := m.stateStore.SaveSessionID(ctx, workspaceName, session.ID); err != nil {
		m.client.TerminateSession(session.ID)
		return "", fmt.Errorf("failed to save session ID: %w", err)
	}

	return session.ID, nil
}

func (m *Manager) PauseSync(ctx context.Context, workspaceName string) error {
	sessionID, err := m.stateStore.GetSessionID(ctx, workspaceName)
	if err != nil {
		return err
	}

	if sessionID == "" {
		return fmt.Errorf("no sync session found for workspace: %s", workspaceName)
	}

	return m.client.PauseSession(sessionID)
}

func (m *Manager) ResumeSync(ctx context.Context, workspaceName string) error {
	sessionID, err := m.stateStore.GetSessionID(ctx, workspaceName)
	if err != nil {
		return err
	}

	if sessionID == "" {
		return fmt.Errorf("no sync session found for workspace: %s", workspaceName)
	}

	return m.client.ResumeSession(sessionID)
}

func (m *Manager) StopSync(ctx context.Context, workspaceName string) error {
	sessionID, err := m.stateStore.GetSessionID(ctx, workspaceName)
	if err != nil {
		return err
	}

	if sessionID == "" {
		return nil
	}

	if err := m.client.TerminateSession(sessionID); err != nil {
		return fmt.Errorf("failed to terminate sync session: %w", err)
	}

	return m.stateStore.DeleteSessionID(ctx, workspaceName)
}

func (m *Manager) GetSyncStatus(ctx context.Context, workspaceName string) (*SyncStatus, error) {
	sessionID, err := m.stateStore.GetSessionID(ctx, workspaceName)
	if err != nil {
		return nil, err
	}

	if sessionID == "" {
		return nil, fmt.Errorf("no sync session found for workspace: %s", workspaceName)
	}

	return m.client.GetStatus(sessionID)
}

func (m *Manager) FlushSync(ctx context.Context, workspaceName string) error {
	sessionID, err := m.stateStore.GetSessionID(ctx, workspaceName)
	if err != nil {
		return err
	}

	if sessionID == "" {
		return fmt.Errorf("no sync session found for workspace: %s", workspaceName)
	}

	return m.client.Flush(sessionID)
}

type FileStateStore struct {
	stateDir string
}

func NewFileStateStore(stateDir string) *FileStateStore {
	return &FileStateStore{stateDir: stateDir}
}

func (s *FileStateStore) SaveSessionID(ctx context.Context, workspaceName, sessionID string) error {
	sessionFile := filepath.Join(s.stateDir, workspaceName+".sync")
	return WriteFileAtomic(sessionFile, []byte(sessionID))
}

func (s *FileStateStore) GetSessionID(ctx context.Context, workspaceName string) (string, error) {
	sessionFile := filepath.Join(s.stateDir, workspaceName+".sync")
	data, err := ReadFileAtomic(sessionFile)
	if err != nil {
		if IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *FileStateStore) DeleteSessionID(ctx context.Context, workspaceName string) error {
	sessionFile := filepath.Join(s.stateDir, workspaceName+".sync")
	return DeleteFileAtomic(sessionFile)
}
