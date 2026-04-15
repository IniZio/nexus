package store

import "time"

type SandboxResourceSettingsRow struct {
	DefaultMemoryMiB int
	DefaultVCPUs     int
	MaxMemoryMiB     int
	MaxVCPUs         int
	UpdatedAt        time.Time
}

type SandboxResourceSettingsRepository interface {
	GetSandboxResourceSettings() (SandboxResourceSettingsRow, bool, error)
	UpsertSandboxResourceSettings(row SandboxResourceSettingsRow) error
}
