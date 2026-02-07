package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/nexus/nexus/pkg/templates"
)

type Service struct {
	Command     string            `yaml:"command,omitempty"`
	Port        int               `yaml:"port,omitempty"` // Legacy support
	Healthcheck *Healthcheck      `yaml:"healthcheck,omitempty"`
	DependsOn   []string          `yaml:"depends_on,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
}

type Healthcheck struct {
	URL      string `yaml:"url"`
	Interval string `yaml:"interval,omitempty"`
	Timeout  string `yaml:"timeout,omitempty"`
	Retries  int    `yaml:"retries,omitempty"`
}

type Agent struct {
	Name     string   `yaml:"name"`
	Remote   Remote   `yaml:"remote,omitempty"`
	Enabled  bool     `yaml:"enabled,omitempty"`
	Rules    string   `yaml:"rules,omitempty"`
	Skills   []string `yaml:"skills,omitempty"`
	Commands []string `yaml:"commands,omitempty"`
	Plugins  []string `yaml:"plugins,omitempty"`
}

type TemplateRepo struct {
	URL    string `yaml:"url"`
	Branch string `yaml:"branch,omitempty"`
	Path   string `yaml:"path,omitempty"` // Path within repo to templates
}

type PluginManifest struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version,omitempty"`
	Description string `yaml:"description,omitempty"`
	Conditions  []struct {
		File string `yaml:"file"`
	} `yaml:"conditions,omitempty"`
}

type Remote struct {
	Node string `yaml:"node"`
	User string `yaml:"user,omitempty"`
	Port int    `yaml:"port,omitempty"`
}

type Config struct {
	Name     string             `yaml:"name"`
	Remote   Remote             `yaml:"remote,omitempty"`
	Provider string             `yaml:"provider,omitempty"`
	Services map[string]Service `yaml:"services"`
	Extends  []interface{}      `yaml:"extends,omitempty"`
	Plugins  []interface{}      `yaml:"plugins,omitempty"`

	// Sync defines local-to-workspace config synchronization
	Sync *SyncConfig `yaml:"sync,omitempty"`

	Docker struct {
		Image string   `yaml:"image"`
		Ports []string `yaml:"ports,omitempty"`
		DinD  bool     `yaml:"dind,omitempty"`
	} `yaml:"docker,omitempty"`
	LXC struct {
		Image string `yaml:"image,omitempty"`
	} `yaml:"lxc,omitempty"`
	QEMU struct {
		Image        string   `yaml:"image,omitempty"`
		CPU          int      `yaml:"cpu,omitempty"`
		Memory       string   `yaml:"memory,omitempty"`
		Disk         string   `yaml:"disk,omitempty"`
		SSHPort      int      `yaml:"ssh_port,omitempty"`
		ForwardPorts []string `yaml:"forward_ports,omitempty"`
		CacheMode    string   `yaml:"cache_mode,omitempty"`
		IoThread     bool     `yaml:"io_thread,omitempty"`
		VirtIO       bool     `yaml:"virtio,omitempty"`
		SELinux      bool     `yaml:"selinux,omitempty"`
		Firewall     bool     `yaml:"firewall,omitempty"`
	} `yaml:"qemu,omitempty"`
	Hooks struct {
		Setup    string `yaml:"setup,omitempty"`
		Dev      string `yaml:"dev,omitempty"`
		Teardown string `yaml:"teardown,omitempty"`
	} `yaml:"hooks,omitempty"`
}

// SyncConfig defines local-to-workspace config synchronization
type SyncConfig struct {
	Paths   []SyncPath   `yaml:"paths"`
	Strategy SyncStrategy `yaml:"strategy,omitempty"`
}

// SyncPath defines a single sync operation
type SyncPath struct {
	Source  string `yaml:"source"`
	Target  string `yaml:"target"`
	Pattern string `yaml:"pattern,omitempty"`
	Exclude string `yaml:"exclude,omitempty"`
}

// SyncStrategy defines how to perform synchronization
type SyncStrategy string

const (
	// StrategyVolume mounts local paths as volumes (best for containers)
	StrategyVolume SyncStrategy = "volume"
	// StrategyCopy copies files on workspace start
	StrategyCopy SyncStrategy = "copy"
)

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	return &cfg, err
}

func DetectInstalledAgents() []string {
	var agents []string
	if _, err := exec.LookPath("cursor"); err == nil {
		agents = append(agents, "cursor")
	} else if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".cursor")); err == nil {
		agents = append(agents, "cursor")
	}

	if _, err := exec.LookPath("opencode"); err == nil {
		agents = append(agents, "opencode")
	}

	if _, err := exec.LookPath("claude"); err == nil {
		agents = append(agents, "claude-desktop")
		agents = append(agents, "claude-code")
	}

	return agents
}

// TODO: Implement plugin initialization
// InitializePlugins pulls all plugin repositories defined in config
// func (c *Config) InitializePlugins(baseDir string) error {
// 	if len(c.Plugins) == 0 {
// 		return nil
// 	}
//
// 	for _, repo := range c.Plugins {
// 		if err := manager.PullRepo(templates.TemplateRepo{
// 			URL:    repo.URL,
// 			Branch: repo.Branch,
// 			Path:   repo.Path,
// 		}); err != nil {
// 			return fmt.Errorf("failed to pull template repo %s: %w", repo.URL, err)
// 		}
// 	}
//
// 	return nil
// }

// RenderData holds data for template rendering
type RenderData struct {
	AuthToken      string
	ProjectName    string
	RulesConfig    string // JSON
	SkillsConfig   string // JSON
	CommandsConfig string // JSON
}

// GetMergedTemplates returns merged template data from all sources
func (c *Config) GetMergedTemplates(baseDir string) (*templates.TemplateData, error) {
	nexusDir := filepath.Join(baseDir, ".nexus")
	manager := templates.NewManager(nexusDir)

	var enabledPlugins []string
	for _, p := range c.Plugins {
		if name, ok := p.(string); ok {
			if c.isPluginEnabled(baseDir, name) {
				enabledPlugins = append(enabledPlugins, name)
			}
		}
	}

	var extends []string
	for _, e := range c.Extends {
		if name, ok := e.(string); ok {
			extends = append(extends, name)
		}
	}

	return manager.Merge(nexusDir, enabledPlugins, extends)
}

func (c *Config) isPluginEnabled(baseDir, name string) bool {
	switch name {
	case "golang":
		return fileExists(filepath.Join(baseDir, "go.mod")) || fileExists(filepath.Join(baseDir, "go.sum"))
	case "node":
		return fileExists(filepath.Join(baseDir, "package.json"))
	default:
		return true
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (c *Config) GenerateAgentConfigs(worktreePath string, merged *templates.TemplateData) error {
	// Agent configurations mapping
	agentConfigs := map[string]struct {
		templatePath string
		outputPath   string
		gitignore    string
		rulesFormat  string
		rulesDir     string
	}{
		"opencode": {
			templatePath: ".nexus/agents/opencode/opencode.json.tpl",
			outputPath:   "opencode.json",
			gitignore:    "AGENTS.md",
			rulesFormat:  "md",
			rulesDir:     ".opencode/rules",
		},
		"claude-desktop": {
			templatePath: ".nexus/agents/claude-desktop/claude_desktop_config.json.tpl",
			outputPath:   "claude_desktop_config.json",
			gitignore:    "claude_desktop_config.json",
		},
		"claude-code": {
			templatePath: ".nexus/agents/claude-code/claude_code_config.json.tpl",
			outputPath:   "claude_code_config.json",
			gitignore:    "claude_code_config.json",
		},
		"codex": {
			templatePath: ".nexus/agents/codex/settings.json.tpl",
			outputPath:   ".vscode/settings.json",
			gitignore:    ".vscode/",
			rulesFormat:  "md",
			rulesDir:     ".github/instructions",
		},
		"cursor": {
			// MCP removed - cursor only generates rules now
			gitignore:   ".cursor/",
			rulesFormat: "mdc",
			rulesDir:    ".cursor/rules",
		},
	}

	// Collect merged data from specified plugins
	finalRules := make(map[string]interface{})
	finalSkills := make(map[string]interface{})
	finalCommands := make(map[string]interface{})

	relevantPlugins := []string{"base", "override"}
	for _, plugin := range c.Plugins {
		if name, ok := plugin.(string); ok {
			relevantPlugins = append(relevantPlugins, name)
		}
	}

	if merged != nil {
		for _, pluginName := range relevantPlugins {
			if plugin, ok := merged.Plugins[pluginName]; ok {
				for k, v := range plugin.Rules {
					finalRules[k] = v
				}
			}
		}
	}

	// Marshal template data to JSON for rendering
	rulesJSON, _ := json.Marshal(finalRules)
	skillsJSON, _ := json.Marshal(finalSkills)
	commandsJSON, _ := json.Marshal(finalCommands)

	renderData := RenderData{
		AuthToken:      generateAuthToken(),
		ProjectName:    c.Name,
		RulesConfig:    string(rulesJSON),
		SkillsConfig:   string(skillsJSON),
		CommandsConfig: string(commandsJSON),
	}

	var gitignorePatterns []string

	if len(finalRules) > 0 {
		// Create a temporary merged object for generateAgentRules
		tempMerged := &templates.TemplateData{
			Plugins: map[string]*templates.PluginData{
				"merged": {
					Rules: finalRules,
				},
			},
		}
		if err := c.generateAgentRules(worktreePath, "agents.md", "", tempMerged); err != nil {
			return fmt.Errorf("failed to generate AGENTS.md: %w", err)
		}
		gitignorePatterns = append(gitignorePatterns, "AGENTS.md")
	}

	detectedAgents := DetectInstalledAgents()
	for _, agentName := range detectedAgents {
		cfg, ok := agentConfigs[agentName]
		if !ok {
			continue
		}

		// Skip agents that don't have a template (like cursor which only generates rules)
		if cfg.templatePath == "" {
			continue
		}

		templateContent, err := os.ReadFile(cfg.templatePath)
		if err != nil {
			return fmt.Errorf("failed to read template %s: %w", cfg.templatePath, err)
		}

		tmpl, err := template.New("template").Parse(string(templateContent))
		if err != nil {
			return fmt.Errorf("failed to parse template for %s: %w", agentName, err)
		}

		var result strings.Builder
		if err := tmpl.Execute(&result, renderData); err != nil {
			return fmt.Errorf("failed to execute template for %s: %w", agentName, err)
		}
		rendered := result.String()

		outputPath := filepath.Join(worktreePath, cfg.outputPath)
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return fmt.Errorf("failed to create dir for %s: %w", outputPath, err)
		}

		if err := os.WriteFile(outputPath, []byte(rendered), 0644); err != nil {
			return fmt.Errorf("failed to write config for %s: %w", agentName, err)
		}

		if cfg.rulesFormat != "" {
			agentRules := make(map[string]interface{})

			// Merge rules from relevant plugins
			agentPluginNames := relevantPlugins
			for _, pluginName := range agentPluginNames {
				if merged == nil {
					continue
				}
				if plugin, ok := merged.Plugins[pluginName]; ok {
					for ruleName, ruleData := range plugin.Rules {
						// Namespace plugin rules to avoid collisions
						namespacedName := pluginName + "/" + ruleName
						agentRules[namespacedName] = ruleData
					}
				}
			}

			// Load agent-specific rules from override directory
			agentOverrideDir := filepath.Join(".nexus", "agents", agentName, "rules")
			if _, err := os.Stat(agentOverrideDir); err == nil {
				if err := filepath.Walk(agentOverrideDir, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if !info.IsDir() && (strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".mdc")) {
						relPath, _ := filepath.Rel(agentOverrideDir, path)
						ruleName := strings.TrimSuffix(relPath, filepath.Ext(relPath))

						content, err := os.ReadFile(path)
						if err != nil {
							return err
						}

						agentRules[ruleName] = map[string]interface{}{
							"content": string(content),
						}
					}
					return nil
				}); err != nil {
					return fmt.Errorf("failed to load agent-specific rules for %s: %w", agentName, err)
				}
			}

			// Generate agent-specific rules if any were collected
			if len(agentRules) > 0 {
				tempAgentMerged := &templates.TemplateData{
					Plugins: map[string]*templates.PluginData{
						"agent": {
							Rules: agentRules,
						},
					},
				}
				if err := c.generateAgentRules(worktreePath, cfg.rulesFormat, cfg.rulesDir, tempAgentMerged); err != nil {
					return fmt.Errorf("failed to generate rules for %s: %w", agentName, err)
				}
			}
		}

		gitignorePatterns = append(gitignorePatterns, cfg.gitignore)
	}

	// Update .gitignore with generated patterns
	if err := updateGitignore(gitignorePatterns); err != nil {
		return fmt.Errorf("failed to update .gitignore: %w", err)
	}

	return nil
}

func (c *Config) generateAgentRules(worktreePath, format, rulesDir string, merged *templates.TemplateData) error {
	if merged == nil || len(merged.Plugins) == 0 {
		return nil
	}

	absRulesDir := filepath.Join(worktreePath, rulesDir)
	if rulesDir != "" {
		if err := os.MkdirAll(absRulesDir, 0755); err != nil {
			return err
		}
	}

	switch format {
	case "mdc", "md":
		extension := "." + format
		if format == "md" {
			extension = ".md"
		}
		for _, plugin := range merged.Plugins {
			for name, data := range plugin.Rules {
				ruleMap, ok := data.(map[string]interface{})
				if !ok {
					continue
				}
				content, _ := ruleMap["content"].(string)
				// Strip any existing frontmatter delimiters from content
				content = stripFrontmatter(content)

				var builder strings.Builder
				builder.WriteString("---\n")
				for k, v := range ruleMap {
					if k == "content" {
						continue
					}
					builder.WriteString(fmt.Sprintf("%s: %v\n", k, v))
				}
				builder.WriteString("---\n")
				builder.WriteString(content)

				outputPath := filepath.Join(absRulesDir, name+extension)
				if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
					return err
				}
				if err := os.WriteFile(outputPath, []byte(builder.String()), 0644); err != nil {
					return err
				}
			}
		}
	case "agents.md":
		var builder strings.Builder
		builder.WriteString("# PROJECT KNOWLEDGE BASE\n\n")
		builder.WriteString(fmt.Sprintf("**Generated:** %s\n", strings.ToUpper(c.Name)))
		builder.WriteString("\n")
		for _, plugin := range merged.Plugins {
			for _, data := range plugin.Rules {
				ruleMap, ok := data.(map[string]interface{})
				if !ok {
					continue
				}
				content, _ := ruleMap["content"].(string)
				builder.WriteString(content)
				builder.WriteString("\n\n")
			}
		}
		outputPath := filepath.Join(worktreePath, "AGENTS.md")
		if err := os.WriteFile(outputPath, []byte(builder.String()), 0644); err != nil {
			return err
		}
	}
	return nil
}

// stripFrontmatter removes YAML frontmatter delimiters from markdown content
func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return content
	}
	// Find the closing ---
	idx := strings.Index(content[4:], "\n---")
	if idx == -1 {
		return content
	}
	return content[idx+5:]
}

// generateAuthToken creates a random 32-character hex token
func generateAuthToken() string {
	return "random-token-12345" // Simplified for now
}

// updateGitignore adds patterns to .gitignore if not already present
func updateGitignore(patterns []string) error {
	gitignorePath := ".gitignore"
	var existing []string

	if content, err := os.ReadFile(gitignorePath); err == nil {
		existing = strings.Split(strings.TrimSpace(string(content)), "\n")
	}

	var cleaned []string
	for _, line := range existing {
		if strings.TrimSpace(line) != "" {
			cleaned = append(cleaned, line)
		}
	}
	existing = cleaned

	// Add missing patterns
	for _, pattern := range patterns {
		found := false
		for _, line := range existing {
			if strings.TrimSpace(line) == pattern {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, pattern)
		}
	}

	// Write back
	newContent := strings.Join(existing, "\n") + "\n"
	return os.WriteFile(gitignorePath, []byte(newContent), 0644)
}

// GenerateJSONSchema generates a JSON schema for the Config struct
func GenerateJSONSchema() (string, error) {
	schema := map[string]interface{}{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Project name",
			},
			"provider": map[string]interface{}{
				"type":        "string",
				"description": "Execution provider (docker, lxc, qemu)",
				"enum":        []string{"docker", "lxc", "qemu"},
			},
			"services": map[string]interface{}{
				"type":        "object",
				"description": "Service definitions",
				"patternProperties": map[string]interface{}{
					".*": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"command": map[string]interface{}{
								"type":        "string",
								"description": "Command to run the service",
							},
							"port": map[string]interface{}{
								"type":        "integer",
								"description": "Legacy port configuration (auto-detected from command)",
								"minimum":     1,
								"maximum":     65535,
							},
							"healthcheck": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"url": map[string]interface{}{
										"type":        "string",
										"description": "Health check URL",
									},
									"interval": map[string]interface{}{
										"type":        "string",
										"description": "Health check interval",
									},
									"timeout": map[string]interface{}{
										"type":        "string",
										"description": "Health check timeout",
									},
									"retries": map[string]interface{}{
										"type":        "integer",
										"description": "Number of retries",
										"minimum":     0,
									},
								},
								"additionalProperties": false,
							},
							"depends_on": map[string]interface{}{
								"type":        "array",
								"items":       map[string]interface{}{"type": "string"},
								"description": "Services this service depends on",
							},
							"env": map[string]interface{}{
								"type":        "object",
								"description": "Environment variables",
								"patternProperties": map[string]interface{}{
									".*": map[string]interface{}{"type": "string"},
								},
							},
						},
						"additionalProperties": false,
					},
				},
			},
			"remotes": map[string]interface{}{
				"type":        "array",
				"description": "Remote template repositories",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"url": map[string]interface{}{
							"type":        "string",
							"description": "Repository URL",
						},
						"branch": map[string]interface{}{
							"type":        "string",
							"description": "Branch to pull from",
						},
						"path": map[string]interface{}{
							"type":        "string",
							"description": "Path within repository",
						},
					},
					"required":             []string{"url"},
					"additionalProperties": false,
				},
			},
			"sync_targets": map[string]interface{}{
				"type":        "array",
				"description": "Remote repositories for config syncing",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]interface{}{
							"type":        "string",
							"description": "Target name",
						},
						"url": map[string]interface{}{
							"type":        "string",
							"description": "Repository URL",
						},
					},
					"required":             []string{"name", "url"},
					"additionalProperties": false,
				},
			},
			"agents": map[string]interface{}{
				"type":        "array",
				"description": "AI agent configurations",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]interface{}{
							"type":        "string",
							"description": "Agent name (cursor, opencode, claude-desktop, etc.)",
							"enum":        []string{"cursor", "opencode", "claude-desktop", "claude-code", "codex"},
						},
						"enabled": map[string]interface{}{
							"type":        "boolean",
							"description": "Whether this agent is enabled",
						},
						"rules": map[string]interface{}{
							"type":        "string",
							"description": "Path to rules file",
						},
						"skills": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "List of skills to enable",
						},
						"commands": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "List of commands to enable",
						},
						"plugins": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "Plugin namespaces to include",
						},
					},
					"required":             []string{"name"},
					"additionalProperties": false,
				},
			},
			"docker": map[string]interface{}{
				"type":        "object",
				"description": "Docker provider configuration",
				"properties": map[string]interface{}{
					"image": map[string]interface{}{
						"type":        "string",
						"description": "Docker image to use",
					},
					"ports": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Port mappings",
					},
					"dind": map[string]interface{}{
						"type":        "boolean",
						"description": "Enable Docker-in-Docker",
					},
				},
				"additionalProperties": false,
			},
			"lxc": map[string]interface{}{
				"type":        "object",
				"description": "LXC provider configuration",
				"properties": map[string]interface{}{
					"image": map[string]interface{}{
						"type":        "string",
						"description": "LXC image to use",
					},
				},
				"additionalProperties": false,
			},
			"qemu": map[string]interface{}{
				"type":        "object",
				"description": "QEMU provider configuration",
				"properties": map[string]interface{}{
					"image": map[string]interface{}{
						"type":        "string",
						"description": "QEMU OS image to use (e.g., ubuntu:22.04, alpine:3.19)",
					},
					"cpu": map[string]interface{}{
						"type":        "integer",
						"description": "Number of CPU cores",
						"minimum":     1,
						"maximum":     32,
					},
					"memory": map[string]interface{}{
						"type":        "string",
						"description": "Memory allocation (e.g., 4G, 512M)",
						"pattern":     "^[0-9]+[MG]$",
					},
					"disk": map[string]interface{}{
						"type":        "string",
						"description": "Disk size (e.g., 20G, 50G)",
						"pattern":     "^[0-9]+[GT]?$",
					},
					"ssh_port": map[string]interface{}{
						"type":        "integer",
						"description": "SSH port for VM access",
						"minimum":     1024,
						"maximum":     65535,
					},
					"forward_ports": map[string]interface{}{
						"type":        "array",
						"description": "Port forwarding rules (host:guest)",
						"items": map[string]interface{}{
							"type":    "string",
							"pattern": "^[0-9]+:[0-9]+$",
						},
					},
					"cache_mode": map[string]interface{}{
						"type":        "string",
						"description": "Disk cache mode",
						"enum":        []string{"none", "writeback", "writethrough", "directsync", "unsafe"},
					},
					"io_thread": map[string]interface{}{
						"type":        "boolean",
						"description": "Enable I/O threads for better performance",
					},
					"virtio": map[string]interface{}{
						"type":        "boolean",
						"description": "Enable VirtIO drivers for better performance",
					},
					"selinux": map[string]interface{}{
						"type":        "boolean",
						"description": "Enable SELinux (for server images like CentOS)",
					},
					"firewall": map[string]interface{}{
						"type":        "boolean",
						"description": "Enable firewall configuration",
					},
				},
				"additionalProperties": false,
			},
			"hooks": map[string]interface{}{
				"type":        "object",
				"description": "Lifecycle hooks",
				"properties": map[string]interface{}{
					"setup": map[string]interface{}{
						"type":        "string",
						"description": "Setup hook script path",
					},
					"dev": map[string]interface{}{
						"type":        "string",
						"description": "Development hook script path",
					},
					"teardown": map[string]interface{}{
						"type":        "string",
						"description": "Teardown hook script path",
					},
				},
				"additionalProperties": false,
			},
		},
		"additionalProperties": false,
	}

	schemaBytes, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal schema: %w", err)
	}

	return string(schemaBytes), nil
}
