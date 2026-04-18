package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func readState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func writeState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func shouldCheck(state State, interval time.Duration, force bool) bool {
	if force {
		return true
	}
	if interval <= 0 {
		return true
	}
	last := strings.TrimSpace(state.LastCheckedAt)
	if last == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	return time.Since(t) >= interval
}

func markBadVersion(state *State, version string) {
	version = strings.TrimSpace(version)
	if version == "" {
		return
	}
	for _, v := range state.BadVersions {
		if v == version {
			return
		}
	}
	state.BadVersions = append(state.BadVersions, version)
	sort.Strings(state.BadVersions)
}

func isBadVersion(state State, version string) bool {
	for _, v := range state.BadVersions {
		if strings.TrimSpace(v) == strings.TrimSpace(version) {
			return true
		}
	}
	return false
}

func shouldSuppressVersion(state State, version string, cooldown time.Duration) bool {
	if !isBadVersion(state, version) {
		return false
	}
	if strings.TrimSpace(state.AttemptedVersion) != strings.TrimSpace(version) {
		return false
	}
	if cooldown <= 0 {
		return true
	}
	lastFailure := strings.TrimSpace(state.LastFailureAt)
	if lastFailure == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, lastFailure)
	if err != nil {
		return true
	}
	return time.Since(t) < cooldown
}
