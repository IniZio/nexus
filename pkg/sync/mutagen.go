package sync

import (
	"bytes"
	"context"
	"encoding/json"
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
		"create",
		"--name", sessionName,
		"--sync-mode", config.Mode,
		"--watch-interval", fmt.Sprintf("%.0f", config.WatchInterval.Seconds()),
	}

	for _, exclude := range config.Exclude {
		args = append(args, "--exclude", exclude)
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
	cmd := exec.Command(c.binaryPath, "pause", id)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to pause mutagen session: %w: %s", err, stderr.String())
	}

	return nil
}

func (c *MutagenClient) ResumeSession(id string) error {
	cmd := exec.Command(c.binaryPath, "resume", id)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to resume mutagen session: %w: %s", err, stderr.String())
	}

	return nil
}

func (c *MutagenClient) TerminateSession(id string) error {
	cmd := exec.Command(c.binaryPath, "terminate", id)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to terminate mutagen session: %w: %s", err, stderr.String())
	}

	return nil
}

func (c *MutagenClient) GetStatus(id string) (*SyncStatus, error) {
	cmd := exec.Command(c.binaryPath, "list", "--json", id)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "no sessions found") {
			return nil, fmt.Errorf("session not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get mutagen status: %w: %s", err, stderr.String())
	}

	var sessions []mutagenSessionJSON
	if err := json.Unmarshal(stdout.Bytes(), &sessions); err != nil {
		return nil, fmt.Errorf("failed to parse mutagen output: %w", err)
	}

	if len(sessions) == 0 {
		return nil, fmt.Errorf("session not found: %s", id)
	}

	session := sessions[0]

	status := &SyncStatus{
		State:     session.SyncState,
		Conflicts: []Conflict{},
	}

	if session.LastSync != "" {
		if t, err := time.Parse(time.RFC3339, session.LastSync); err == nil {
			status.LastSync = t
		}
	}

	for _, conflict := range session.Conflicts {
		status.Conflicts = append(status.Conflicts, Conflict{
			Path:         conflict.Path,
			AlphaContent: conflict.AlphaContent,
			BetaContent:  conflict.BetaContent,
		})
	}

	return status, nil
}

func (c *MutagenClient) Flush(id string) error {
	cmd := exec.Command(c.binaryPath, "flush", id)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to flush mutagen session: %w: %s", err, stderr.String())
	}

	return nil
}

func (c *MutagenClient) ListSessions() ([]MutagenSession, error) {
	cmd := exec.Command(c.binaryPath, "list", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "no sessions found") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list mutagen sessions: %w: %s", err, stderr.String())
	}

	var sessions []mutagenSessionJSON
	if err := json.Unmarshal(stdout.Bytes(), &sessions); err != nil {
		return nil, fmt.Errorf("failed to parse mutagen output: %w", err)
	}

	var result []MutagenSession
	for _, s := range sessions {
		result = append(result, MutagenSession{
			ID:        s.Identifier,
			AlphaPath: s.Alpha.Path,
			BetaPath:  s.Beta.Path,
		})
	}

	return result, nil
}

type mutagenSessionJSON struct {
	Identifier string         `json:"identifier"`
	Name       string         `json:"name"`
	SyncState  string         `json:"syncState"`
	Alpha      mutagenEndpoint `json:"alpha"`
	Beta       mutagenEndpoint `json:"beta"`
	LastSync   string         `json:"lastSync"`
	Conflicts  []mutagenConflict `json:"conflicts"`
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
