package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type BoulderState struct {
	Iteration       int    `json:"iteration"`
	LastActivity    int64  `json:"lastActivity"`
	LastEnforcement int64  `json:"lastEnforcement"`
	Status          string `json:"status"`
}

type BoulderConfig struct {
	IdleThresholdMs   int `json:"idle_threshold_ms"`
	CooldownMs        int `json:"cooldown_ms"`
	CountdownSeconds  int `json:"countdown_seconds"`
	AbortWindowMs     int `json:"abort_window_ms"`
	MaxFailures       int `json:"max_failures"`
	BackoffMultiplier int `json:"backoff_multiplier"`
}

func boulderConfigPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".nexus", "boulder", "config.json")
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

func loadBoulderConfig() (*BoulderConfig, error) {
	path := boulderConfigPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := defaultBoulderConfig()
			if err := saveBoulderConfig(cfg); err != nil {
				return nil, fmt.Errorf("failed to create default config: %w", err)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg BoulderConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

func saveBoulderConfig(cfg *BoulderConfig) error {
	dir := filepath.Dir(boulderConfigPath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating boulder directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(boulderConfigPath(), data, 0644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

func defaultBoulderConfig() *BoulderConfig {
	return &BoulderConfig{
		IdleThresholdMs:   30000,
		CooldownMs:        30000,
		CountdownSeconds:  2,
		AbortWindowMs:     3000,
		MaxFailures:       5,
		BackoffMultiplier: 2,
	}
}

func (c *BoulderConfig) Get(key string) (string, error) {
	switch key {
	case "idle_threshold_ms":
		return strconv.Itoa(c.IdleThresholdMs), nil
	case "cooldown_ms":
		return strconv.Itoa(c.CooldownMs), nil
	case "countdown_seconds":
		return strconv.Itoa(c.CountdownSeconds), nil
	case "abort_window_ms":
		return strconv.Itoa(c.AbortWindowMs), nil
	case "max_failures":
		return strconv.Itoa(c.MaxFailures), nil
	case "backoff_multiplier":
		return strconv.Itoa(c.BackoffMultiplier), nil
	default:
		return "", fmt.Errorf("unknown key: %s", key)
	}
}

func (c *BoulderConfig) Set(key, value string) error {
	intVal, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid value for %s: %w", key, err)
	}

	switch key {
	case "idle_threshold_ms":
		c.IdleThresholdMs = intVal
	case "cooldown_ms":
		c.CooldownMs = intVal
	case "countdown_seconds":
		c.CountdownSeconds = intVal
	case "abort_window_ms":
		c.AbortWindowMs = intVal
	case "max_failures":
		c.MaxFailures = intVal
	case "backoff_multiplier":
		c.BackoffMultiplier = intVal
	default:
		return fmt.Errorf("unknown key: %s", key)
	}

	return nil
}
