package metrics

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Logger struct {
	filePath string
	store    *LogStore
}

func NewLogger(basePath string) *Logger {
	return &Logger{
		filePath: filepath.Join(basePath, ".nexus", "logs", "usage.json"),
		store:    &LogStore{},
	}
}

func (l *Logger) Load() error {
	data, err := os.ReadFile(l.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			l.store = &LogStore{
				Entries: []UsageLog{},
				Metadata: StoreMeta{
					Version:     "1.0",
					LastUpdated: time.Now().Format(time.RFC3339),
				},
			}
			return nil
		}
		return fmt.Errorf("failed to read log file: %w", err)
	}

	if err := json.Unmarshal(data, l.store); err != nil {
		return fmt.Errorf("failed to unmarshal log store: %w", err)
	}

	return nil
}

func (l *Logger) Log(entry UsageLog) error {
	if err := l.Load(); err != nil {
		return err
	}

	if entry.ID == "" {
		entry.ID = generateID()
	}

	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().Format(time.RFC3339)
	}

	l.store.Entries = append(l.store.Entries, entry)

	return l.Save()
}

func (l *Logger) Query(filters Filter) ([]UsageLog, error) {
	if err := l.Load(); err != nil {
		return nil, err
	}

	var results []UsageLog
	for _, entry := range l.store.Entries {
		if l.matches(entry, filters) {
			results = append(results, entry)
		}
	}

	return results, nil
}

func (l *Logger) Save() error {
	if err := os.MkdirAll(filepath.Dir(l.filePath), 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	l.store.Metadata.LastUpdated = time.Now().Format(time.RFC3339)

	data, err := json.MarshalIndent(l.store, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal log store: %w", err)
	}

	if err := os.WriteFile(l.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write log file: %w", err)
	}

	return nil
}

func (l *Logger) matches(entry UsageLog, filters Filter) bool {
	if filters.Agent != "" && entry.Agent != filters.Agent {
		return false
	}

	if !filters.StartTime.IsZero() {
		entryTime, err := time.Parse(time.RFC3339, entry.Timestamp)
		if err != nil {
			return false
		}
		if entryTime.Before(filters.StartTime) {
			return false
		}
	}

	if !filters.EndTime.IsZero() {
		entryTime, err := time.Parse(time.RFC3339, entry.Timestamp)
		if err != nil {
			return false
		}
		if entryTime.After(filters.EndTime) {
			return false
		}
	}

	if filters.Category != "" && entry.Invocation.Category != filters.Category {
		return false
	}

	if filters.Type != "" && entry.Invocation.Type != filters.Type {
		return false
	}

	return true
}

func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

type Filter struct {
	Agent     string
	StartTime time.Time
	EndTime   time.Time
	Category  string
	Type      string
}
