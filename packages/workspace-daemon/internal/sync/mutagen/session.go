package mutagen

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type SessionManager struct {
	daemon    *EmbeddedDaemon
	mutagenBin string
	mu        sync.RWMutex
	sessions  map[string]*SyncSession
}

type SyncSession struct {
	ID        string
	AlphaPath string
	BetaPath  string
	Status    string
}

func NewSessionManager(daemon *EmbeddedDaemon) (*SessionManager, error) {
	mutagenBin := daemon.mutagenBin
	if mutagenBin == "" {
		mutagenBin = "mutagen"
	}

	mgr := &SessionManager{
		daemon:     daemon,
		mutagenBin: mutagenBin,
		sessions:   make(map[string]*SyncSession),
	}

	return mgr, nil
}

func (m *SessionManager) MutagenBin() string {
	return m.mutagenBin
}

func (m *SessionManager) CreateSession(ctx context.Context, workspaceID, alphaPath, betaPath string, config *SessionConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, s := range m.sessions {
		if s.AlphaPath == alphaPath && s.BetaPath == betaPath {
			return s.ID, nil
		}
	}

	mode := "two-way-safe"
	watchInterval := "1"

	if config != nil {
		if config.Mode != "" {
			mode = string(config.Mode)
		}
		if config.WatchIntervalSeconds > 0 {
			watchInterval = fmt.Sprintf("%d", config.WatchIntervalSeconds)
		}
	}

	sessionName := fmt.Sprintf("nexus-%s", workspaceID)

	args := []string{
		"sync", "create",
		"--name", sessionName,
		"--sync-mode", mode,
		"--watch-polling-interval", watchInterval,
	}

	if config != nil && len(config.Exclude) > 0 {
		for _, exclude := range config.Exclude {
			args = append(args, "--exclude", exclude)
		}
	}

	args = append(args, alphaPath, betaPath)

	cmd := exec.CommandContext(ctx, m.mutagenBin, args...)
	cmd.Env = m.daemonEnv()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	log.Printf("[mutagen] Creating session: %s %s -> %s", m.mutagenBin, alphaPath, betaPath)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create mutagen session: %w: %s", err, stderr.String())
	}

	sessionID := m.findSessionIDByName(sessionName)
	if sessionID == "" {
		sessionID = fmt.Sprintf("nexus-%d", time.Now().UnixNano())
	}

	m.sessions[sessionID] = &SyncSession{
		ID:        sessionID,
		AlphaPath: alphaPath,
		BetaPath:  betaPath,
		Status:    "created",
	}

	log.Printf("[mutagen] Created sync session %s for workspace %s", sessionID, workspaceID)

	return sessionID, nil
}

func (m *SessionManager) daemonEnv() []string {
	if m.daemon != nil && m.daemon.DataDir() != "" {
		return []string{"MUTAGEN_DATA_DIRECTORY=" + m.daemon.DataDir()}
	}
	return nil
}

func (m *SessionManager) findSessionIDByName(name string) string {
	cmd := exec.Command(m.mutagenBin, "sync", "list")
	cmd.Env = m.daemonEnv()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return ""
	}

	lines := strings.Split(stdout.String(), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Name:") && strings.Contains(line, name) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "Identifier:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Identifier:"))
		}
	}
	return ""
}

func (m *SessionManager) PauseSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	_, exists := m.sessions[sessionID]
	m.mu.Unlock()

	if !exists {
		sessions, err := m.ListSessions()
		if err != nil {
			return err
		}
		found := false
		for _, s := range sessions {
			if s.ID == sessionID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("session not found: %s", sessionID)
		}
	}

	cmd := exec.CommandContext(ctx, m.mutagenBin, "sync", "pause", sessionID)
	cmd.Env = m.daemonEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to pause mutagen session: %w: %s", err, stderr.String())
	}

	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		s.Status = "paused"
	}
	m.mu.Unlock()

	log.Printf("[mutagen] Paused session %s", sessionID)
	return nil
}

func (m *SessionManager) ResumeSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	_, exists := m.sessions[sessionID]
	m.mu.Unlock()

	if !exists {
		sessions, err := m.ListSessions()
		if err != nil {
			return err
		}
		found := false
		for _, s := range sessions {
			if s.ID == sessionID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("session not found: %s", sessionID)
		}
	}

	cmd := exec.CommandContext(ctx, m.mutagenBin, "sync", "resume", sessionID)
	cmd.Env = m.daemonEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to resume mutagen session: %w: %s", err, stderr.String())
	}

	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		s.Status = "running"
	}
	m.mu.Unlock()

	log.Printf("[mutagen] Resumed session %s", sessionID)
	return nil
}

func (m *SessionManager) TerminateSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd := exec.CommandContext(ctx, m.mutagenBin, "sync", "terminate", sessionID)
	cmd.Env = m.daemonEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to terminate mutagen session: %w: %s", err, stderr.String())
	}

	delete(m.sessions, sessionID)

	log.Printf("[mutagen] Terminated session %s", sessionID)
	return nil
}

func (m *SessionManager) FlushSession(ctx context.Context, sessionID string) error {
	cmd := exec.CommandContext(ctx, m.mutagenBin, "sync", "flush", sessionID)
	cmd.Env = m.daemonEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to flush mutagen session: %w: %s", err, stderr.String())
	}

	log.Printf("[mutagen] Flushed session %s", sessionID)
	return nil
}

func (m *SessionManager) GetSessionStatus(ctx context.Context, sessionID string) (*SessionStatus, error) {
	cmd := exec.CommandContext(ctx, m.mutagenBin, "sync", "list", sessionID)
	cmd.Env = m.daemonEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "no sessions found") || strings.Contains(stderr.String(), "unknown") {
			return nil, fmt.Errorf("session not found: %s", sessionID)
		}
		return nil, fmt.Errorf("failed to get mutagen status: %w: %s", err, stderr.String())
	}

	output := stdout.String()
	status := &SessionStatus{
		SessionID: sessionID,
		Status:    "unknown",
		Connected: false,
	}

	if strings.Contains(output, "Connected: Yes") {
		status.Connected = true
	}
	if strings.Contains(output, "Staging files") {
		status.Status = "staging"
	} else if strings.Contains(output, "Watching for changes") {
		status.Status = "watching"
	} else if strings.Contains(output, "Transition problems") {
		status.Status = "error"
	} else if status.Connected {
		status.Status = "connected"
	}

	m.mu.RLock()
	if s, ok := m.sessions[sessionID]; ok {
		status.AlphaPath = s.AlphaPath
		status.BetaPath = s.BetaPath
	}
	m.mu.RUnlock()

	return status, nil
}

func (m *SessionManager) ListSessions() ([]*SyncSession, error) {
	cmd := exec.Command(m.mutagenBin, "sync", "list")
	cmd.Env = m.daemonEnv()
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

	var result []*SyncSession
	lines := strings.Split(output, "\n")
	var currentSession *SyncSession

	for _, line := range lines {
		if strings.HasPrefix(line, "Name:") {
			currentSession = &SyncSession{}
			result = append(result, currentSession)
		} else if strings.HasPrefix(line, "Identifier:") && currentSession != nil {
			id := strings.TrimSpace(strings.TrimPrefix(line, "Identifier:"))
			currentSession.ID = id
		} else if strings.HasPrefix(line, "\tURL:") && currentSession != nil && currentSession.AlphaPath == "" {
			url := strings.TrimSpace(strings.TrimPrefix(line, "\tURL:"))
			currentSession.AlphaPath = extractPathFromURL(url)
		} else if strings.HasPrefix(line, "\tURL:") && currentSession != nil && currentSession.AlphaPath != "" && currentSession.BetaPath == "" {
			url := strings.TrimSpace(strings.TrimPrefix(line, "\tURL:"))
			currentSession.BetaPath = extractPathFromURL(url)
		}
	}

	return result, nil
}

func extractPathFromURL(url string) string {
	parts := strings.Split(url, "://")
	if len(parts) < 2 {
		return url
	}
	pathPart := parts[1]
	if idx := strings.Index(pathPart, "?"); idx != -1 {
		pathPart = pathPart[:idx]
	}
	if idx := strings.Index(pathPart, "#"); idx != -1 {
		pathPart = pathPart[:idx]
	}
	return filepath.Clean(pathPart)
}

type SessionConfig struct {
	Mode                string
	WatchIntervalSeconds int
	Exclude             []string
}

type SessionStatus struct {
	SessionID  string
	AlphaPath  string
	BetaPath   string
	Status     string
	Connected  bool
}
