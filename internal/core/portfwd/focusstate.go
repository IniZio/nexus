package portfwd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const focusStateFileName = "focus.state"

// FocusState records which herdr workspace and sandbox currently hold focus.
type FocusState struct {
	WorkspaceID string    `json:"workspace_id"`
	SandboxID   string    `json:"sandbox_id"`
	Session     string    `json:"session"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func FocusStatePath() string {
	return filepath.Join(StateDir(), focusStateFileName)
}

// WriteFocusState atomically writes s to path (temp-then-rename), creating parents 0700.
func WriteFocusState(path string, s FocusState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("portfwd focus state mkdir: %w", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("portfwd focus state marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("portfwd focus state write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("portfwd focus state rename: %w", err)
	}
	return nil
}

// ReadFocusState reads focus.state at path; missing file → (zero, false, nil).
func ReadFocusState(path string) (FocusState, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return FocusState{}, false, nil
	}
	if err != nil {
		return FocusState{}, false, fmt.Errorf("portfwd focus state read: %w", err)
	}
	s, err := ParseFocusState(data)
	if err != nil {
		return FocusState{}, false, err
	}
	return s, true, nil
}

func ParseFocusState(b []byte) (FocusState, error) {
	var s FocusState
	if err := json.Unmarshal(b, &s); err != nil {
		return FocusState{}, fmt.Errorf("portfwd focus state parse: %w", err)
	}
	return s, nil
}
