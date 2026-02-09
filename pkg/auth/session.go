package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Session struct {
	UserID      string    `json:"user_id"`
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

func GetSessionPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	nexusDir := filepath.Join(home, ".nexus")
	if err := os.MkdirAll(nexusDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create .nexus directory: %w", err)
	}

	return filepath.Join(nexusDir, "session.json"), nil
}

func SaveSession(session *Session) error {
	sessionPath, err := GetSessionPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	if err := os.WriteFile(sessionPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	return nil
}

func LoadSession() (*Session, error) {
	sessionPath, err := GetSessionPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no active session found. Please run 'nexus login' first")
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	if time.Now().After(session.ExpiresAt) {
		return nil, fmt.Errorf("session expired. Please run 'nexus login' again")
	}

	return &session, nil
}

func ClearSession() error {
	sessionPath, err := GetSessionPath()
	if err != nil {
		return err
	}

	if err := os.Remove(sessionPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove session file: %w", err)
	}

	return nil
}

func IsLoggedIn() bool {
	session, err := LoadSession()
	return err == nil && session != nil
}
