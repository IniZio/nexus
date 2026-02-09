package plugins

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Plugin represents a nexus plugin
type Plugin struct {
	Name         string            `yaml:"name"`
	Version      string            `yaml:"version"`
	Description  string            `yaml:"description,omitempty"`
	Repository   string            `yaml:"repository,omitempty"`
	Path         string            `yaml:"path,omitempty"` // Subpath within repo
	Dependencies []string          `yaml:"dependencies,omitempty"`
	Metadata     map[string]string `yaml:"metadata,omitempty"`
}

// PluginManifest is the plugin.yaml structure
type PluginManifest struct {
	Plugin Plugin `yaml:"plugin"`
}

// Registry manages plugin discovery and resolution
type Registry struct {
	plugins map[string]*Plugin
}

// NewRegistry creates a new plugin registry
func NewRegistry() *Registry {
	return &Registry{
		plugins: make(map[string]*Plugin),
	}
}

// DiscoverPlugins finds all plugins in the given base directory
func (r *Registry) DiscoverPlugins(baseDir string) error {
	pluginsDir := filepath.Join(baseDir, "plugins")

	// Discover local plugins
	if err := r.discoverLocalPlugins(pluginsDir); err != nil {
		return fmt.Errorf("failed to discover local plugins: %w", err)
	}

	// Discover remote plugins from nexus.lock
	if err := r.discoverPluginsFromLockfile(baseDir); err != nil {
		return fmt.Errorf("failed to discover remote plugins from lockfile: %w", err)
	}

	return nil
}

// discoverPluginsFromLockfile discovers plugins from the nexus.lock file
func (r *Registry) discoverPluginsFromLockfile(baseDir string) error {
	lockfilePath := filepath.Join(baseDir, "nexus.lock")

	lockfile, err := ReadLockfile(lockfilePath)
	if err != nil {
		if os.IsNotExist(err) {
			// No lockfile present, skip remote plugin discovery
			return nil
		}
		return fmt.Errorf("failed to read lockfile: %w", err)
	}

	plugins, err := DiscoverPlugins(lockfile)
	if err != nil {
		return fmt.Errorf("failed to discover plugins from lockfile: %w", err)
	}

	for i := range plugins {
		r.plugins[plugins[i].Name] = &plugins[i]
	}

	return nil
}

// discoverLocalPlugins finds plugins in .nexus/plugins/
func (r *Registry) discoverLocalPlugins(pluginsDir string) error {
	return filepath.WalkDir(pluginsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || !strings.HasSuffix(path, "plugin.yaml") {
			return nil
		}

		manifest, err := r.loadPluginManifest(path)
		if err != nil {
			return fmt.Errorf("failed to load plugin manifest %s: %w", path, err)
		}

		// Extract namespace from path relative to plugins dir
		relPath, err := filepath.Rel(pluginsDir, filepath.Dir(path))
		if err != nil {
			return err
		}

		namespace := strings.ReplaceAll(relPath, string(filepath.Separator), "/")
		pluginName := namespace

		r.plugins[pluginName] = &manifest.Plugin
		return nil
	})
}

// loadPluginManifest loads a plugin.yaml file
func (r *Registry) loadPluginManifest(path string) (*PluginManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var manifest PluginManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	return &manifest, nil
}

// ResolveDependencies performs topological sort with cycle detection
func (r *Registry) ResolveDependencies(pluginNames []string) ([]string, error) {
	// Build dependency graph
	graph := make(map[string][]string)
	inDegree := make(map[string]int)
	allPlugins := make(map[string]bool)

	// Initialize with requested plugins
	for _, name := range pluginNames {
		if _, exists := r.plugins[name]; !exists {
			return nil, fmt.Errorf("plugin %s not found", name)
		}
		allPlugins[name] = true
		inDegree[name] = 0
	}

	// Add all transitive dependencies
	toProcess := pluginNames
	processed := make(map[string]bool)

	for len(toProcess) > 0 {
		current := toProcess[0]
		toProcess = toProcess[1:]

		if processed[current] {
			continue
		}
		processed[current] = true

		plugin := r.plugins[current]
		for _, dep := range plugin.Dependencies {
			if _, exists := r.plugins[dep]; !exists {
				return nil, fmt.Errorf("dependency %s of plugin %s not found", dep, current)
			}

			if !allPlugins[dep] {
				allPlugins[dep] = true
				inDegree[dep] = 0
				toProcess = append(toProcess, dep)
			}

			graph[dep] = append(graph[dep], current)
			inDegree[current]++
		}
	}

	// Topological sort using Kahn's algorithm
	var result []string
	queue := make([]string, 0)

	// Find nodes with no incoming edges
	for plugin := range allPlugins {
		if inDegree[plugin] == 0 {
			queue = append(queue, plugin)
		}
	}

	for len(queue) > 0 {
		// Sort queue for deterministic output
		sort.Strings(queue)

		current := queue[0]
		queue = queue[1:]
		result = append(result, current)

		// For each dependent plugin
		for _, dependent := range graph[current] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	// Check for cycles
	if len(result) != len(allPlugins) {
		return nil, fmt.Errorf("circular dependency detected in plugin graph")
	}

	return result, nil
}

// GetPlugin returns a plugin by name
func (r *Registry) GetPlugin(name string) (*Plugin, bool) {
	plugin, exists := r.plugins[name]
	return plugin, exists
}

// ListPlugins returns all discovered plugins
func (r *Registry) ListPlugins() map[string]*Plugin {
	result := make(map[string]*Plugin)
	for k, v := range r.plugins {
		result[k] = v
	}
	return result
}

// AddPlugin adds a plugin to the registry (for testing)
func (r *Registry) AddPlugin(name string, plugin *Plugin) {
	r.plugins[name] = plugin
}
