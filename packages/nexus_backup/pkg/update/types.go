package update

import "time"

type ReleaseManifest struct {
	SchemaVersion         int                    `json:"schemaVersion"`
	Version               string                 `json:"version"`
	PublishedAt           string                 `json:"publishedAt"`
	MinimumUpdaterVersion string                 `json:"minimumUpdaterVersion"`
	Artifacts             map[string]TargetBuild `json:"artifacts"`
}

type TargetBuild struct {
	URLBase string         `json:"urlBase"`
	CLI     BinaryArtifact `json:"cli"`
	Daemon  BinaryArtifact `json:"daemon"`
}

type BinaryArtifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type State struct {
	LastCheckedAt    string   `json:"lastCheckedAt"`
	LastSuccessAt    string   `json:"lastSuccessAt"`
	LastSuccess      string   `json:"lastSuccess"`
	LastFailureAt    string   `json:"lastFailureAt"`
	LastFailure      string   `json:"lastFailure"`
	CurrentVersion   string   `json:"currentVersion"`
	PendingVersion   string   `json:"pendingVersion"`
	AttemptedVersion string   `json:"attemptedVersion"`
	BadVersions      []string `json:"badVersions"`
}

type Status struct {
	CurrentVersion string     `json:"currentVersion"`
	LatestVersion  string     `json:"latestVersion"`
	LastCheckedAt  string     `json:"lastCheckedAt"`
	LastSuccessAt  string     `json:"lastSuccessAt"`
	LastFailureAt  string     `json:"lastFailureAt"`
	LastFailure    string     `json:"lastFailure"`
	UpdateReady    bool       `json:"updateReady"`
	CheckedAt      *time.Time `json:"checkedAt,omitempty"`
}

type Options struct {
	ReleaseBaseURL     string
	PublicKeyBase64    string
	CheckInterval      time.Duration
	BadVersionCooldown time.Duration
	CurrentVersion     string
	CurrentUpdater     string
	AutoApply          bool
	Force              bool
}

type Result struct {
	Updated       bool
	FromVersion   string
	ToVersion     string
	DaemonHealthy bool
}
