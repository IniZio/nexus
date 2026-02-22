package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type BoulderState struct {
	Iteration      int    `json:"iteration"`
	LastActivity  int64  `json:"lastActivity"`
	LastEnforcement int64 `json:"lastEnforcement"`
	Status        string `json:"status"`
}

func boulderStatePath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".nexus", "boulder", "state.json")
}

func loadBoulderState() (*BoulderState, error) {
	path := boulderStatePath()
	
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			state := &BoulderState{
				Iteration: 0,
				Status:    "ENFORCING",
			}
			if err := saveBoulderState(state); err != nil {
				return nil, fmt.Errorf("failed to create default state: %w", err)
			}
			return state, nil
		}
		return nil, fmt.Errorf("reading state: %w", err)
	}

	var state BoulderState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing state: %w", err)
	}

	return &state, nil
}

func saveBoulderState(state *BoulderState) error {
	dir := filepath.Dir(boulderStatePath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating boulder directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	if err := os.WriteFile(boulderStatePath(), data, 0644); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}

	return nil
}
