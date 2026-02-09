package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nexus/nexus/pkg/plugins"
)

// LockEntry represents a locked plugin version
type LockEntry struct {
	Name         string            `yaml:"name"`
	Version      string            `yaml:"version"`
	SHA          string            `yaml:"sha"`
	Repository   string            `yaml:"repository,omitempty"`
	Path         string            `yaml:"path,omitempty"`
	Dependencies []string          `yaml:"dependencies,omitempty"`
	Metadata     map[string]string `yaml:"metadata,omitempty"`
}

// Lockfile represents the nexus.lock structure
type Lockfile struct {
	Version  string                `yaml:"_version"`
	Plugins  map[string]*LockEntry `yaml:"plugins"`
	Metadata LockMetadata          `yaml:"_metadata"`
}

// LockMetadata contains metadata about the lockfile
type LockMetadata struct {
	ContentHash string            `yaml:"content_hash"`
	Timestamp   string            `yaml:"timestamp"`
	Generator   string            `yaml:"generator"`
	Extra       map[string]string `yaml:"extra,omitempty"`
}

// Manager handles lockfile operations
type Manager struct {
	baseDir string
}

// NewManager creates a new lockfile manager
func NewManager(baseDir string) *Manager {
	return &Manager{baseDir: baseDir}
}

// GenerateLockfile creates a lockfile from active plugins
func (m *Manager) GenerateLockfile(registry *plugins.Registry, activePlugins []string) (*Lockfile, error) {
	lockfile := &Lockfile{
		Version: "1.0",
		Plugins: make(map[string]*LockEntry),
		Metadata: LockMetadata{
			Generator: "nexus",
			Extra:     make(map[string]string),
		},
	}

	// Resolve dependencies to get all plugins that need to be locked
	allPlugins, err := registry.ResolveDependencies(activePlugins)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve dependencies: %w", err)
	}

	// Generate lock entries for all plugins
	for _, pluginName := range allPlugins {
		plugin, exists := registry.GetPlugin(pluginName)
		if !exists {
			return nil, fmt.Errorf("plugin %s not found in registry", pluginName)
		}

		// Generate deterministic SHA for the plugin
		sha, err := m.generatePluginSHA(plugin)
		if err != nil {
			return nil, fmt.Errorf("failed to generate SHA for plugin %s: %w", pluginName, err)
		}

		lockfile.Plugins[pluginName] = &LockEntry{
			Name:         plugin.Name,
			Version:      plugin.Version,
			SHA:          sha,
			Repository:   plugin.Repository,
			Path:         plugin.Path,
			Dependencies: plugin.Dependencies,
			Metadata:     plugin.Metadata,
		}
	}

	// Generate content hash for deterministic verification
	contentHash, err := m.generateContentHash(lockfile)
	if err != nil {
		return nil, fmt.Errorf("failed to generate content hash: %w", err)
	}
	lockfile.Metadata.ContentHash = contentHash

	return lockfile, nil
}

// LoadLockfile loads a lockfile from disk
func (m *Manager) LoadLockfile() (*Lockfile, error) {
	lockPath := filepath.Join(m.baseDir, "nexus.lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("lockfile not found at %s", lockPath)
		}
		return nil, err
	}

	var lockfile Lockfile
	if err := yaml.Unmarshal(data, &lockfile); err != nil {
		return nil, fmt.Errorf("failed to parse lockfile: %w", err)
	}

	return &lockfile, nil
}

// SaveLockfile saves a lockfile to disk
func (m *Manager) SaveLockfile(lockfile *Lockfile) error {
	lockPath := filepath.Join(m.baseDir, "nexus.lock")
	data, err := yaml.Marshal(lockfile)
	if err != nil {
		return fmt.Errorf("failed to marshal lockfile: %w", err)
	}

	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write lockfile: %w", err)
	}

	return nil
}

// VerifyIntegrity checks if the lockfile content matches its hash
func (m *Manager) VerifyIntegrity(lockfile *Lockfile) error {
	expectedHash := lockfile.Metadata.ContentHash
	actualHash, err := m.generateContentHash(lockfile)
	if err != nil {
		return fmt.Errorf("failed to generate content hash for verification: %w", err)
	}

	if expectedHash != actualHash {
		return fmt.Errorf("lockfile integrity check failed: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

// IsUpToDate checks if the lockfile is up to date with the current plugin registry
func (m *Manager) IsUpToDate(registry *plugins.Registry, activePlugins []string) (bool, error) {
	currentLockfile, err := m.GenerateLockfile(registry, activePlugins)
	if err != nil {
		return false, err
	}

	existingLockfile, err := m.LoadLockfile()
	if err != nil {
		// If no lockfile exists, it's not up to date
		return false, nil
	}

	// Compare content hashes
	return existingLockfile.Metadata.ContentHash == currentLockfile.Metadata.ContentHash, nil
}

// generatePluginSHA generates a deterministic SHA for a plugin
func (m *Manager) generatePluginSHA(plugin *plugins.Plugin) (string, error) {
	// Create a canonical representation of the plugin
	canonical := fmt.Sprintf("%s|%s|%s|%s|%s|%s",
		plugin.Name,
		plugin.Version,
		plugin.Repository,
		plugin.Path,
		strings.Join(plugin.Dependencies, ","),
		m.canonicalizeMetadata(plugin.Metadata),
	)

	hash := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(hash[:]), nil
}

// generateContentHash generates a hash of the lockfile content for integrity verification
func (m *Manager) generateContentHash(lockfile *Lockfile) (string, error) {
	// Create canonical representation of plugins
	var pluginKeys []string
	for k := range lockfile.Plugins {
		pluginKeys = append(pluginKeys, k)
	}
	sort.Strings(pluginKeys)

	var canonical strings.Builder
	canonical.WriteString(fmt.Sprintf("version:%s\n", lockfile.Version))

	for _, key := range pluginKeys {
		entry := lockfile.Plugins[key]
		canonical.WriteString(fmt.Sprintf("plugin:%s|%s|%s\n",
			key, entry.Version, entry.SHA))
	}

	hash := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(hash[:]), nil
}

// canonicalizeMetadata creates a deterministic string representation of metadata
func (m *Manager) canonicalizeMetadata(metadata map[string]string) string {
	if len(metadata) == 0 {
		return ""
	}

	var keys []string
	for k := range metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var result strings.Builder
	for i, key := range keys {
		if i > 0 {
			result.WriteString(";")
		}
		result.WriteString(fmt.Sprintf("%s=%s", key, metadata[key]))
	}

	return result.String()
}
