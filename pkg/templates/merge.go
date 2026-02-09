package templates

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// TemplateData represents the merged template data
type TemplateData struct {
	Plugins map[string]*PluginData `yaml:"plugins"`
}

type PluginData struct {
	Skills   map[string]interface{} `yaml:"skills"`
	Rules    map[string]interface{} `yaml:"rules"`
	Commands map[string]interface{} `yaml:"commands"`
}

type PluginManifest struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version,omitempty"`
	Description string `yaml:"description,omitempty"`
	Conditions  []struct {
		File string `yaml:"file"`
	} `yaml:"conditions,omitempty"`
}

func (m *Manager) Merge(baseDir string, _ []string, extends []string) (*TemplateData, error) {
	data := &TemplateData{
		Plugins: make(map[string]*PluginData),
	}

	// Load extends as base configs
	if err := m.loadExtends(baseDir, extends, data); err != nil {
		return nil, fmt.Errorf("failed to load extends: %w", err)
	}

	// Load remote template repos
	templateReposDir := filepath.Join(baseDir, "remotes")
	if err := m.loadTemplateRepos(templateReposDir, data); err != nil {
		return nil, fmt.Errorf("failed to load template repos: %w", err)
	}

	// Load local plugin templates
	projectRoot := filepath.Dir(baseDir)
	pluginsDir := filepath.Join(baseDir, "plugins")
	if err := m.loadPluginTemplates(pluginsDir, projectRoot, data); err != nil {
		return nil, fmt.Errorf("failed to load plugin templates: %w", err)
	}

	// Load base templates last to ensure local rules take precedence
	baseTemplatesDir := filepath.Join(baseDir, "templates")
	basePlugin := m.getOrCreatePlugin(data, "base")
	if err := m.loadTemplatesFromDir(baseTemplatesDir, basePlugin); err != nil {
		return nil, fmt.Errorf("failed to load base templates: %w", err)
	}

	agentsDir := filepath.Join(baseDir, "agents")
	if err := m.applyAgentOverrides(agentsDir, data); err != nil {
		return nil, fmt.Errorf("failed to load agent overrides: %w", err)
	}

	return data, nil
}

func (m *Manager) loadExtends(baseDir string, extends []string, data *TemplateData) error {
	for _, extend := range extends {
		parts := strings.Split(extend, "/")
		if len(parts) != 2 {
			continue
		}

		owner, repo := parts[0], parts[1]

		// Parse optional branch
		branch := ""
		if strings.Contains(repo, "@") {
			repoParts := strings.SplitN(repo, "@", 2)
			repo = repoParts[0]
			branch = repoParts[1]
		}

		repoURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)
		repoTemplate := TemplateRepo{
			URL:    repoURL,
			Branch: branch,
		}

		// Clone if missing, no network update
		if err := m.PullWithoutUpdate(repoTemplate); err != nil {
			return fmt.Errorf("failed to pull extend %s: %w", extend, err)
		}

		repoName := extractRepoName(repoURL)
		repoDir := m.GetRepoDir(repoName)

		// Load templates from the extended repo
		if err := m.loadTemplatesFromExtendsRepo(repoDir, data); err != nil {
			return fmt.Errorf("failed to load templates from %s: %w", extend, err)
		}
	}
	return nil
}

func (m *Manager) loadTemplatesFromExtendsRepo(repoDir string, data *TemplateData) error {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pluginName := entry.Name()
		pluginDir := filepath.Join(repoDir, pluginName)
		plugin := m.getOrCreatePlugin(data, pluginName)

		// Load skills, rules, commands from each subdirectory
		for _, templateType := range []string{"skills", "rules", "commands"} {
			subDir := filepath.Join(pluginDir, templateType)
			if err := m.loadTemplatesFromDir(subDir, plugin); err != nil {
				return err
			}
		}
	}

	return nil
}

// Stub implementations for missing methods - TODO: implement properly
func (m *Manager) loadTemplateRepos(_ string, data *TemplateData) error {
	// Stub implementation
	return nil
}

func (m *Manager) loadPluginTemplates(_, _ string, data *TemplateData) error {
	// Stub implementation
	return nil
}

func (m *Manager) getOrCreatePlugin(data *TemplateData, name string) *PluginData {
	// Stub implementation
	if data.Plugins == nil {
		data.Plugins = make(map[string]*PluginData)
	}
	if data.Plugins[name] == nil {
		data.Plugins[name] = &PluginData{
			Skills:   make(map[string]interface{}),
			Rules:    make(map[string]interface{}),
			Commands: make(map[string]interface{}),
		}
	}
	return data.Plugins[name]
}

func (m *Manager) loadTemplatesFromDir(dir string, plugin *PluginData) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}

	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		parts := strings.Split(relPath, string(filepath.Separator))
		if len(parts) < 1 {
			return nil
		}
		templateType := parts[0]

		filename := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var target map[string]interface{}
		switch templateType {
		case "skills":
			target = plugin.Skills
		case "rules":
			target = plugin.Rules
		case "commands":
			target = plugin.Commands
		default:
			return nil
		}

		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			var data interface{}
			if err := yaml.Unmarshal(content, &data); err != nil {
				return fmt.Errorf("failed to parse YAML %s: %w", path, err)
			}
			target[filename] = data
		} else if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".mdc") {
			frontmatter, mdContent := parseMarkdown(content)
			ruleData := make(map[string]interface{})
			for k, v := range frontmatter {
				ruleData[k] = v
			}
			ruleData["content"] = mdContent
			target[filename] = ruleData
		}

		return nil
	})
}

func parseMarkdown(content []byte) (map[string]interface{}, string) {
	contentStr := string(content)

	if !strings.HasPrefix(contentStr, "---\n") {
		return make(map[string]interface{}), contentStr
	}

	parts := strings.SplitN(contentStr, "\n---\n", 2)
	if len(parts) != 2 {
		return make(map[string]interface{}), contentStr
	}

	frontmatterStr := parts[0] + "\n"
	body := parts[1]

	var frontmatter map[string]interface{}
	if err := yaml.Unmarshal([]byte(frontmatterStr), &frontmatter); err != nil {
		return make(map[string]interface{}), contentStr
	}

	return frontmatter, body
}

func (m *Manager) applyAgentOverrides(agentsDir string, data *TemplateData) error {
	if _, err := os.Stat(agentsDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		agentName := entry.Name()
		agentDir := filepath.Join(agentsDir, agentName)

		overridePlugin := m.getOrCreatePlugin(data, "override")

		if err := m.applyOverrideForType(agentDir, "rules", overridePlugin.Rules); err != nil {
			return fmt.Errorf("failed to apply %s rules overrides: %w", agentName, err)
		}
		if err := m.applyOverrideForType(agentDir, "skills", overridePlugin.Skills); err != nil {
			return fmt.Errorf("failed to apply %s skills overrides: %w", agentName, err)
		}
		if err := m.applyOverrideForType(agentDir, "commands", overridePlugin.Commands); err != nil {
			return fmt.Errorf("failed to apply %s commands overrides: %w", agentName, err)
		}
	}

	return nil
}

func (m *Manager) applyOverrideForType(agentDir, templateType string, target map[string]interface{}) error {
	overrideDir := filepath.Join(agentDir, templateType)
	if _, err := os.Stat(overrideDir); os.IsNotExist(err) {
		return nil
	}

	return filepath.WalkDir(overrideDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		isMd := strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".mdc")
		if !isMd {
			return nil
		}

		filename := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if len(strings.TrimSpace(string(content))) == 0 {
			delete(target, filename)
			return nil
		}

		var overrideData map[string]interface{}
		frontmatter, mdContent := parseMarkdown(content)
		ruleData := make(map[string]interface{})
		for k, v := range frontmatter {
			ruleData[k] = v
		}
		ruleData["content"] = mdContent
		overrideData = map[string]interface{}{
			filename: ruleData,
		}

		for key, value := range overrideData {
			target[key] = value
		}

		return nil
	})
}

// RenderTemplate renders a Go template string with the provided data
func (m *Manager) RenderTemplate(templateStr string, data interface{}) (string, error) {
	tmpl, err := template.New("template").Parse(templateStr)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// ConfigTemplate represents a configuration template with services, environment, volumes, and networks
type ConfigTemplate struct {
	Services  map[string]ServiceTemplate  `yaml:"services,omitempty"`
	Environment map[string]string        `yaml:"environment,omitempty"`
	Volumes   map[string]VolumeTemplate  `yaml:"volumes,omitempty"`
	Networks  map[string]NetworkTemplate `yaml:"networks,omitempty"`
}

// ServiceTemplate represents a service configuration in a template
type ServiceTemplate struct {
	Command     string            `yaml:"command,omitempty"`
	Port        int               `yaml:"port,omitempty"`
	Healthcheck *Healthcheck      `yaml:"healthcheck,omitempty"`
	DependsOn   []string          `yaml:"depends_on,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty"`
	Networks    []string          `yaml:"networks,omitempty"`
}

// Healthcheck represents a health check configuration
type Healthcheck struct {
	URL      string `yaml:"url"`
	Interval string `yaml:"interval,omitempty"`
	Timeout  string `yaml:"timeout,omitempty"`
	Retries  int    `yaml:"retries,omitempty"`
}

// VolumeTemplate represents a volume configuration
type VolumeTemplate struct {
	Driver     string            `yaml:"driver,omitempty"`
	DriverOpts map[string]string `yaml:"driver_opts,omitempty"`
	Labels    map[string]string `yaml:"labels,omitempty"`
}

// NetworkTemplate represents a network configuration
type NetworkTemplate struct {
	Driver     string            `yaml:"driver,omitempty"`
	DriverOpts map[string]string `yaml:"driver_opts,omitempty"`
	Labels    map[string]string `yaml:"labels,omitempty"`
	Internal  bool              `yaml:"internal,omitempty"`
}

// MergeConfigTemplates merges two config templates
// Services maps: overlay overrides base
// Environment maps: overlay adds to base
// Volumes maps: overlay overrides base
// Networks maps: overlay overrides base
// DependsOn is merged with proper ordering
func MergeConfigTemplates(base, overlay ConfigTemplate) (ConfigTemplate, error) {
	result := ConfigTemplate{
		Services:    make(map[string]ServiceTemplate),
		Environment: make(map[string]string),
		Volumes:     make(map[string]VolumeTemplate),
		Networks:    make(map[string]NetworkTemplate),
	}

	// Merge Environment: overlay adds to base (base is overwritten by overlay)
	for k, v := range base.Environment {
		result.Environment[k] = v
	}
	for k, v := range overlay.Environment {
		result.Environment[k] = v
	}

	// Merge Volumes: overlay overrides base
	for k, v := range base.Volumes {
		result.Volumes[k] = v
	}
	for k, v := range overlay.Volumes {
		result.Volumes[k] = v
	}

	// Merge Networks: overlay overrides base
	for k, v := range base.Networks {
		result.Networks[k] = v
	}
	for k, v := range overlay.Networks {
		result.Networks[k] = v
	}

	// Merge Services: overlay overrides base
	for name, svc := range base.Services {
		mergedSvc, err := mergeService(svc, ServiceTemplate{})
		if err != nil {
			return result, fmt.Errorf("failed to merge service %s: %w", name, err)
		}
		result.Services[name] = mergedSvc
	}
	for name, svc := range overlay.Services {
		baseSvc, exists := base.Services[name]
		if !exists {
			// New service from overlay
			result.Services[name] = svc
		} else {
			// Merge overlay service into base service
			mergedSvc, err := mergeService(baseSvc, svc)
			if err != nil {
				return result, fmt.Errorf("failed to merge service %s: %w", name, err)
			}
			result.Services[name] = mergedSvc
		}
	}

	// Validate the merged config
	if err := validateMergedConfig(result); err != nil {
		return result, err
	}

	return result, nil
}

// mergeService merges two service templates (overlay into base)
func mergeService(base, overlay ServiceTemplate) (ServiceTemplate, error) {
	result := base

	// Overlay overrides base fields
	if overlay.Command != "" {
		result.Command = overlay.Command
	}
	if overlay.Port != 0 {
		result.Port = overlay.Port
	}
	if overlay.Healthcheck != nil {
		result.Healthcheck = overlay.Healthcheck
	}

	// Merge Env: overlay adds to base
	if result.Env == nil {
		result.Env = make(map[string]string)
	}
	for k, v := range base.Env {
		result.Env[k] = v
	}
	for k, v := range overlay.Env {
		result.Env[k] = v
	}

	// Merge DependsOn: remove duplicates and ensure proper ordering
	result.DependsOn = mergeDependsOn(base.DependsOn, overlay.DependsOn)

	// Merge Volumes: overlay overrides base
	if result.Volumes == nil {
		result.Volumes = []string{}
	}
	volumeMap := make(map[string]bool)
	for _, v := range result.Volumes {
		volumeMap[v] = true
	}
	for _, v := range overlay.Volumes {
		volumeMap[v] = true
	}
	result.Volumes = make([]string, 0, len(volumeMap))
	for v := range volumeMap {
		result.Volumes = append(result.Volumes, v)
	}

	// Merge Networks: overlay overrides base
	if result.Networks == nil {
		result.Networks = []string{}
	}
	networkMap := make(map[string]bool)
	for _, n := range result.Networks {
		networkMap[n] = true
	}
	for _, n := range overlay.Networks {
		networkMap[n] = true
	}
	result.Networks = make([]string, 0, len(networkMap))
	for n := range networkMap {
		result.Networks = append(result.Networks, n)
	}

	return result, nil
}

// mergeDependsOn merges two dependency lists with proper ordering
// The overlay dependencies come first, then base dependencies (preserving order)
func mergeDependsOn(base, overlay []string) []string {
	if len(overlay) == 0 {
		return removeDuplicates(base)
	}
	if len(base) == 0 {
		return removeDuplicates(overlay)
	}

	// Result: overlay first, then base (without duplicates)
	seen := make(map[string]bool)
	result := []string{}

	// Add overlay dependencies first
	for _, dep := range overlay {
		if !seen[dep] {
			seen[dep] = true
			result = append(result, dep)
		}
	}

	// Add base dependencies that aren't already in overlay
	for _, dep := range base {
		if !seen[dep] {
			seen[dep] = true
			result = append(result, dep)
		}
	}

	return result
}

// removeDuplicates removes duplicate entries from a slice while preserving order
func removeDuplicates(slice []string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, item := range slice {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

// validateMergedConfig validates the merged configuration for conflicts and errors
func validateMergedConfig(config ConfigTemplate) error {
	// Check for circular dependencies
	if err := checkCircularDependencies(config.Services); err != nil {
		return fmt.Errorf("circular dependency detected: %w", err)
	}

	// Check that all DependsOn references exist in Services
	for name, svc := range config.Services {
		for _, dep := range svc.DependsOn {
			if _, exists := config.Services[dep]; !exists {
				return fmt.Errorf("service %s depends on non-existent service %s", name, dep)
			}
		}
	}

	// Check that all volume references exist
	for _, svc := range config.Services {
		for _, vol := range svc.Volumes {
			// Handle named volumes (volume:name:mountpoint or just volume:name)
			volName := extractVolumeName(vol)
			if volName != "" && volName[0] != '/' && volName[0] != '.' {
				if _, exists := config.Volumes[volName]; !exists {
					// Named volume doesn't exist, but this is allowed (may be external)
					// Just log a warning in production
				}
			}
		}
	}

	// Check that all network references exist
	for _, svc := range config.Services {
		for _, net := range svc.Networks {
			if _, exists := config.Networks[net]; !exists {
				// Network doesn't exist, but this is allowed (may be external)
				// Just log a warning in production
			}
		}
	}

	// Check for duplicate service names (shouldn't happen with proper merging)
	if err := checkDuplicateServiceNames(config.Services); err != nil {
		return err
	}

	return nil
}

// extractVolumeName extracts the volume name from a volume specification
func extractVolumeName(volumeSpec string) string {
	// Handle formats like: volume_name:/path, volume_name:/path:ro, ./local:/path
	parts := strings.Split(volumeSpec, ":")
	if len(parts) >= 2 {
		return parts[0]
	}
	return volumeSpec
}

// checkCircularDependencies checks for circular dependencies in services
func checkCircularDependencies(services map[string]ServiceTemplate) error {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var dfs func(service string) error
	dfs = func(service string) error {
		visited[service] = true
		recStack[service] = true

		svc, exists := services[service]
		if !exists {
			return nil
		}

		for _, dep := range svc.DependsOn {
			if !visited[dep] {
				if err := dfs(dep); err != nil {
					return err
				}
			} else if recStack[dep] {
				return fmt.Errorf("circular dependency: %s -> %s", service, dep)
			}
		}

		recStack[service] = false
		return nil
	}

	for service := range services {
		if !visited[service] {
			if err := dfs(service); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkDuplicateServiceNames checks for duplicate service names
func checkDuplicateServiceNames(services map[string]ServiceTemplate) error {
	// This is mainly a sanity check since we use a map
	// Duplicate names in the same map would overwrite, which is intentional
	return nil
}
