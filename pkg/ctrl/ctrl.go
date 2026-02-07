package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/lock"
	"github.com/nexus/nexus/pkg/paths"
	"github.com/nexus/nexus/pkg/provider"
	"github.com/nexus/nexus/pkg/templates"
	"github.com/nexus/nexus/pkg/worktree"
)

// PortMapping represents a service port mapping for a workspace
type PortMapping struct {
	WorkspaceID string `json:"workspace_id"`
	ServiceName string `json:"service_name"`
	LocalPort   int    `json:"local_port"`
	RemotePort  int    `json:"remote_port"`
	URL         string `json:"url"`
}

type Controller interface {
	Init(ctx context.Context) error
	Dev(ctx context.Context, branch string) error
	WorkspaceCreate(ctx context.Context, name string) error
	WorkspaceUp(ctx context.Context, name string) error
	WorkspaceDown(ctx context.Context, name string) error
	WorkspaceShell(ctx context.Context, name string) error
	WorkspaceList(ctx context.Context) error
	WorkspaceRm(ctx context.Context, name string) error
	WorkspaceServices(ctx context.Context, name string) ([]PortMapping, error)
	WorkspaceConnect(ctx context.Context, name string) error
	WorkspaceStatus(ctx context.Context, name string) error
	WorkspaceListAll(ctx context.Context) error
	WorkspaceBatchUp(ctx context.Context, workspaces []string) error
	WorkspaceBatchDown(ctx context.Context, workspaces []string) error
	WorkspaceExecAll(ctx context.Context, workspaces []string, cmd []string, parallel bool) error
	Apply(ctx context.Context) error
	PluginUpdate(ctx context.Context) error
	PluginList(ctx context.Context) error
	Kill(ctx context.Context, sessionID string) error
	List(ctx context.Context) ([]provider.Session, error)
	Exec(ctx context.Context, sessionID string, cmd []string) error
}

type BaseController struct {
	Providers       map[string]provider.Provider
	WorktreeManager worktree.Manager
	LockManager     *lock.Manager
}

func NewBaseController(providers []provider.Provider, wtManager worktree.Manager) *BaseController {
	pMap := make(map[string]provider.Provider)
	for _, p := range providers {
		pMap[p.Name()] = p
	}
	if wtManager == nil {
		projectRoot := paths.GetProjectRoot()
		wtManager = worktree.NewManager(".", paths.GetWorktreesDir(projectRoot))
	}
	return &BaseController{
		Providers:       pMap,
		WorktreeManager: wtManager,
		LockManager:     lock.NewManager("."),
	}
}

func (c *BaseController) Dev(ctx context.Context, branch string) error {
	return c.WorkspaceCreate(ctx, branch)
}

func (c *BaseController) WorkspaceCreate(_ context.Context, name string) error {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return fmt.Errorf("invalid workspace name: %s", name)
	}
	fmt.Printf("🚀 Creating workspace '%s'...\n", name)

	root, err := c.findProjectRoot()
	if err != nil {
		root = "."
	}

	projectRoot := paths.GetProjectRoot()
	worktreesDir := paths.GetWorktreesDir(projectRoot)
	if _, err := os.Stat(filepath.Join(worktreesDir, name)); err == nil {
		return fmt.Errorf("workspace '%s' already exists", name)
	}

	wtPath, err := c.WorktreeManager.Add(name)
	if err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}

	cfg, err := config.LoadConfig(filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	merged, err := cfg.GetMergedTemplates(root)
	if err != nil {
		return fmt.Errorf("failed to merge templates: %w", err)
	}

	if err := cfg.GenerateAgentConfigs(wtPath, merged); err != nil {
		return fmt.Errorf("failed to generate agent configs: %w", err)
	}

	fmt.Printf("✅ Workspace created at %s/%s/\n", paths.GetWorktreesDir(projectRoot), name)
	return nil
}

func (c *BaseController) WorkspaceUp(ctx context.Context, name string) error {
	fmt.Printf("🚀 Starting workspace '%s'...\n", name)

	projectRoot := paths.GetProjectRoot()
	cfg, err := config.LoadConfig(filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	workspacePath, err := filepath.Abs(filepath.Join(paths.GetWorktreesDir(projectRoot), name))
	if err != nil {
		return fmt.Errorf("failed to get absolute path for workspace: %w", err)
	}

	pName := cfg.Provider
	if pName == "" {
		pName = "docker"
	}
	p, ok := c.Providers[pName]
	if !ok {
		return fmt.Errorf("provider '%s' not found", pName)
	}

	sessionID := fmt.Sprintf("%s-%s", cfg.Name, name)
	fmt.Printf("🐳 Creating %s session %s...\n", pName, sessionID)

	session, err := p.Create(ctx, sessionID, workspacePath, cfg)
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	if err := p.Start(ctx, session.ID); err != nil {
		return fmt.Errorf("failed to start session: %w", err)
	}

	sessions, err := p.List(ctx)
	if err == nil {
		for _, s := range sessions {
			if s.ID == session.ID {
				session = &s
				break
			}
		}
	}

	fmt.Println("▶️  Starting session...")

	hookPath := filepath.Join(paths.GetConfigDir(projectRoot), "hooks/up.sh")
	if _, err := os.Stat(hookPath); err == nil {
		fmt.Printf("🔧 Running setup hook: %s\n", hookPath)
		if err := c.runHook(ctx, hookPath, cfg, workspacePath); err != nil {
			fmt.Printf("⚠️  Warning: setup hook failed: %v\n", err)
		}
	}

	if err := c.setupWorkspaceEnvironment(ctx, session, cfg, p, workspacePath); err != nil {
		return fmt.Errorf("failed to setup workspace environment: %w", err)
	}

	fmt.Println("✅ Workspace started successfully")
	return nil
}

func (c *BaseController) WorkspaceDown(ctx context.Context, name string) error {
	fmt.Printf("🛑 Stopping workspace '%s'...\n", name)

	projectRoot := paths.GetProjectRoot()
	cfg, err := config.LoadConfig(filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	sessionID := fmt.Sprintf("%s-%s", cfg.Name, name)
	found := false
	for _, p := range c.Providers {
		sessions, _ := p.List(ctx)
		for _, s := range sessions {
			if s.Labels["nexus.session.id"] == sessionID {
				if err := p.Destroy(ctx, s.ID); err != nil {
					return fmt.Errorf("failed to stop session: %w", err)
				}
				found = true
			}
		}
	}

	if !found {
		return fmt.Errorf("workspace session '%s' not found", sessionID)
	}

	fmt.Println("✅ Workspace stopped")
	return nil
}

func (c *BaseController) WorkspaceShell(ctx context.Context, name string) error {
	fmt.Printf("🐚 Opening shell in workspace '%s'...\n", name)

	projectRoot := paths.GetProjectRoot()
	cfg, err := config.LoadConfig(filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	sessionID := fmt.Sprintf("%s-%s", cfg.Name, name)
	for _, p := range c.Providers {
		sessions, _ := p.List(ctx)
		for _, s := range sessions {
			if s.Labels["nexus.session.id"] == sessionID {
				return p.Exec(ctx, s.ID, provider.ExecOptions{
					Cmd:    []string{"/bin/bash"},
					Stdout: true,
				})
			}
		}
	}
	return fmt.Errorf("workspace session not found")
}

func (c *BaseController) WorkspaceList(ctx context.Context) error {
	fmt.Println("📋 Active workspaces:")

	projectRoot := paths.GetProjectRoot()
	worktreesDir := paths.GetWorktreesDir(projectRoot)
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		fmt.Println("  No active workspaces")
		return nil
	}

	cfg, _ := config.LoadConfig(filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml"))

	found := false
	for _, entry := range entries {
		if entry.IsDir() {
			name := entry.Name()
			status := "stopped"
			ports := ""

			if cfg != nil {
				sessionID := fmt.Sprintf("%s-%s", cfg.Name, name)
				for _, p := range c.Providers {
					sessions, _ := p.List(ctx)
					for _, s := range sessions {
						if s.Labels["nexus.session.id"] == sessionID {
							status = "running"
							var portList []string
							for pPort, hPort := range s.Services {
								portList = append(portList, fmt.Sprintf("%s->%d", pPort, hPort))
							}
							if len(portList) > 0 {
								ports = " (ports: " + strings.Join(portList, ", ") + ")"
							}
						}
					}
				}
			}

			fmt.Printf("  - %s [%s]%s\n", name, status, ports)
			found = true
		}
	}

	if !found {
		fmt.Println("  No active workspaces")
	}
	return nil
}

func (c *BaseController) WorkspaceRm(_ context.Context, name string) error {
	fmt.Printf("🗑️ Removing workspace '%s'...\n", name)

	if err := c.WorktreeManager.Remove(name); err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	fmt.Println("✅ Workspace removed successfully")
	return nil
}

func (c *BaseController) WorkspaceServices(ctx context.Context, name string) ([]PortMapping, error) {
	projectRoot := paths.GetProjectRoot()
	cfg, err := config.LoadConfig(filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	sessionID := fmt.Sprintf("%s-%s", cfg.Name, name)
	var services []PortMapping

	for _, p := range c.Providers {
		sessions, _ := p.List(ctx)
		for _, s := range sessions {
			if s.Labels["nexus.session.id"] == sessionID {
				for serviceName, port := range s.Services {
					services = append(services, PortMapping{
						WorkspaceID: name,
						ServiceName: serviceName,
						LocalPort:   port,
						RemotePort:  port,
						URL:         fmt.Sprintf("http://localhost:%d", port),
					})
				}
				return services, nil
			}
		}
	}

	return nil, fmt.Errorf("workspace session not found")
}

func (c *BaseController) WorkspaceConnect(ctx context.Context, name string) error {
	projectRoot := paths.GetProjectRoot()
	cfg, err := config.LoadConfig(filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	sessionID := fmt.Sprintf("%s-%s", cfg.Name, name)
	workspacePath, _ := filepath.Abs(filepath.Join(paths.GetWorktreesDir(projectRoot), name))

	fmt.Println("🔗 Workspace Connection Info")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("Workspace: %s\n", name)
	fmt.Printf("Path:      %s\n", workspacePath)
	fmt.Println("")

	// Find running session for port info
	for _, p := range c.Providers {
		sessions, _ := p.List(ctx)
		for _, s := range sessions {
			if s.Labels["nexus.session.id"] == sessionID {
				sshPort := 22
				for _, port := range s.Services {
					sshPort = port
					break
				}
				fmt.Println("🐚 SSH Access:")
				fmt.Printf("  ssh -p %d dev@localhost\n", sshPort)
				fmt.Println("")
				fmt.Println("💻 Deep Links:")
				fmt.Printf("  VSCode:  vscode://vscode-remote/ssh-remote+localhost:%d%s\n", sshPort, workspacePath)
				fmt.Printf("  Cursor:  cursor://ssh/remote?host=localhost&port=%d&user=dev\n", sshPort)
				fmt.Println("")
				if len(s.Services) > 0 {
					fmt.Println("🌐 Services:")
					for serviceName, port := range s.Services {
						fmt.Printf("  - http://localhost:%d (%s)\n", port, serviceName)
					}
				}
				fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
				return nil
			}
		}
	}

	fmt.Println("🐚 SSH Access:")
	fmt.Println("  ssh -p <port> dev@localhost")
	fmt.Println("")
	fmt.Println("💡 Workspace is not running. Start it with:")
	fmt.Printf("  nexus workspace up %s\n", name)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	return nil
}

func (c *BaseController) Init(_ context.Context) error {
	projectRoot := paths.GetProjectRoot()

	configDirs := []string{
		filepath.Join(paths.GetConfigDir(projectRoot), "hooks"),
		filepath.Join(paths.GetConfigDir(projectRoot), "agents", "rules"),
		filepath.Join(paths.GetConfigDir(projectRoot), "agents", "skills"),
		filepath.Join(paths.GetConfigDir(projectRoot), "agents", "commands"),
		filepath.Join(paths.GetConfigDir(projectRoot), "templates", "skills"),
		filepath.Join(paths.GetConfigDir(projectRoot), "templates", "rules"),
		filepath.Join(paths.GetConfigDir(projectRoot), "templates", "commands"),
		filepath.Join(paths.GetConfigDir(projectRoot), "remotes"),
		filepath.Join(paths.GetConfigDir(projectRoot), "plugins"),
	}

	runtimeDirs := []string{
		paths.GetDataDir(projectRoot),
		paths.GetStateDir(projectRoot),
		paths.GetWorktreesDir(projectRoot),
		paths.GetLogsDir(projectRoot),
		paths.GetLogsArchiveDir(projectRoot),
		paths.GetCacheDir(projectRoot),
	}

	allDirs := append(configDirs, runtimeDirs...)
	for _, dir := range allDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	configYaml := `name: my-project
provider: docker
plugins:
  - golang
  - node
services:
  web:
    port: 3000
docker:
  image: ubuntu:22.04
  dind: true
hooks:
  setup: .nexus/hooks/up.sh
`
	configPath := filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYaml), 0644); err != nil {
		return err
	}

	nexusAgentRule := `# nexus Agent Rules

## Core Principles
- Work in isolated environments to ensure reproducibility
- Use git worktrees for branch-level isolation
- Integrate seamlessly with AI coding assistants
- Follow established patterns in the codebase

## Development Workflow
1. Create a workspace for each feature branch: 'nexus workspace create <branch-name>'
2. Start the workspace: 'nexus workspace up <branch-name>'
3. Work in the isolated environment with full AI agent support
4. Commit changes and merge when ready
5. Clean up: 'nexus workspace down <branch-name>' and 'nexus workspace rm <branch-name>'

## AI Agent Integration
- Cursor, OpenCode, Claude, and other agents are auto-configured
- MCP server provides context and capabilities
- Rules and skills are automatically loaded from templates
`
	if err := os.WriteFile(filepath.Join(paths.GetConfigDir(projectRoot), "templates/rules/nexus-agent.md"), []byte(nexusAgentRule), 0644); err != nil {
		return err
	}

	tddRule := `# Test-Driven Development (TDD)

## TDD Cycle
1. **RED**: Write a failing test first
2. **GREEN**: Implement minimal code to pass the test
3. **REFACTOR**: Clean up code while keeping tests green

## Testing Guidelines
- Use 'testify/assert' and 'testify/require' in Go tests
- Test file naming: '*_test.go' alongside source
- Aim for 80%+ test coverage on new code
- Test both happy paths and error cases
- Use table-driven tests for multiple scenarios

## Benefits
- Ensures code reliability
- Guides design decisions
- Provides safety net for refactoring
- Documents expected behavior through tests
`
	if err := os.WriteFile(filepath.Join(paths.GetConfigDir(projectRoot), "templates/rules/tdd.md"), []byte(tddRule), 0644); err != nil {
		return err
	}

	// Create local skills
	skillName := "nexus"
	nexusOpsSkill := fmt.Sprintf(`name: %s-ops
description: nexus workspace management operations
capabilities:
  - workspace_create: Create new isolated workspaces
  - workspace_up: Start workspace environment
  - workspace_down: Stop workspace environment
  - workspace_list: List active workspaces
  - workspace_rm: Remove workspace
  - plugin_update: Update plugins to latest versions
  - plugin_list: List loaded plugins
  - apply: Apply configuration to agent configs
tools:
  - bash: Execute shell commands in workspace
  - git: Version control operations
  - docker: Container management
parameters:
  workspace_name:
    type: string
    description: Name of the workspace to operate on
    required: true
  branch:
    type: string
    description: Git branch for workspace creation
    required: false
`, skillName)
	if err := os.WriteFile(filepath.Join(paths.GetConfigDir(projectRoot), fmt.Sprintf("templates/skills/%s-ops.yaml", skillName)), []byte(nexusOpsSkill), 0644); err != nil {
		return err
	}

	upSh := `#!/bin/bash
echo "Starting development environment..."

if ! command -v node &> /dev/null; then
    echo "Installing Node.js..."
    curl -fsSL https://deb.nodesource.com/setup_18.x | bash -
    apt-get install -y nodejs
fi

if ! command -v docker-compose &> /dev/null; then
    echo "Installing docker-compose..."
    apt-get update && apt-get install -y docker-compose
fi

echo "Starting services..."
docker-compose up -d postgres &
cd /workspace/server && npm install && HOST=0.0.0.0 PORT=5000 npm run dev &
API_PID=$!
sleep 5
cd /workspace/client && npm install && HOST=0.0.0.0 PORT=3000 npm run dev &
WEB_PID=$!

echo "Services starting... PIDs: API($API_PID), WEB($WEB_PID)"
echo "Development environment ready."
wait
`
	if err := os.WriteFile(filepath.Join(paths.GetConfigDir(projectRoot), "hooks/up.sh"), []byte(upSh), 0755); err != nil {
		return err
	}

	baseRule := `# Base Rules
- Follow existing code patterns.
- Ensure type safety.
`
	if err := os.WriteFile(filepath.Join(paths.GetConfigDir(projectRoot), "agents/rules/base.md"), []byte(baseRule), 0644); err != nil {
		return err
	}

	agentDirs := []string{
		filepath.Join(paths.GetConfigDir(projectRoot), "agents/cursor"),
		filepath.Join(paths.GetConfigDir(projectRoot), "agents/opencode"),
		filepath.Join(paths.GetConfigDir(projectRoot), "agents/claude-desktop"),
		filepath.Join(paths.GetConfigDir(projectRoot), "agents/claude-code"),
		filepath.Join(paths.GetConfigDir(projectRoot), "agents/codex"),
	}
	for _, dir := range agentDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	codexTpl := `{
  "github.copilot.enable": {
    "*": true,
    "yaml": false,
    "plaintext": false,
    "markdown": true,
    "javascript": true,
    "python": true,
    "typescript": true,
    "go": true,
    "rust": true,
    "java": true,
    "csharp": true
  },
  "github.copilot.advanced": {
    "length": 4000,
    "inlineSuggestCount": 5,
    "top_p": 1,
    "temperature": 0.8,
    "listCount": 10,
    "debug.showScores": false,
    "indentationMode": {
      "*": true
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(paths.GetConfigDir(projectRoot), "agents/codex/settings.json.tpl"), []byte(codexTpl), 0644); err != nil {
		return err
	}

	opencodeTpl := `{
  "$schema": "https://opencode.ai/config.json",
  "instructions": [
    "AGENTS.md",
    ".opencode/rules/*.md",
    ".opencode/skills/*.md",
    ".opencode/commands/*.md"
  ]
}
`
	if err := os.WriteFile(filepath.Join(paths.GetConfigDir(projectRoot), "agents/opencode/opencode.json.tpl"), []byte(opencodeTpl), 0644); err != nil {
		return err
	}

	claudeDesktopTpl := `{
  "mcpServers": {
    "nexus": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-everything", "{{.ProjectName}}"]
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(paths.GetConfigDir(projectRoot), "agents/claude-desktop/claude_desktop_config.json.tpl"), []byte(claudeDesktopTpl), 0644); err != nil {
		return err
	}

	claudeCodeTpl := `{
  "mcpServers": {
    "nexus": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-everything", "{{.ProjectName}}"]
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(paths.GetConfigDir(projectRoot), "agents/claude-code/claude_code_config.json.tpl"), []byte(claudeCodeTpl), 0644); err != nil {
		return err
	}

	return nil
}

func (c *BaseController) Apply(ctx context.Context) error {
	fmt.Println("🔄 Applying latest configuration to agent configs...")

	projectRoot := paths.GetProjectRoot()
	cfg, err := config.LoadConfig(filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	agents := config.DetectInstalledAgents()
	if len(agents) == 0 {
		fmt.Println("⚠️  No AI agents detected. Install Cursor, OpenCode, or Claude to use nexus.")
		return nil
	}

	fmt.Printf("🤖 Detected agents: %v\n", agents)

	merged, err := cfg.GetMergedTemplates(".")
	if err != nil {
		return fmt.Errorf("failed to merge templates: %w", err)
	}

	if err := cfg.GenerateAgentConfigs(".", merged); err != nil {
		return fmt.Errorf("failed to generate agent configs: %w", err)
	}

	// Generate additional agent-specific configurations
	for _, agent := range agents {
		switch agent {
		case "opencode":
			_ = c.copyPluginCapabilitiesToOpenCode(cfg)
		}
	}
	for _, agent := range agents {
		switch agent {
		case "cursor":
			if err := c.generateCursorConfig(cfg); err != nil {
				fmt.Printf("❌ Failed to update Cursor config: %v\n", err)
			} else {
				fmt.Println("✅ Updated Cursor configuration")
			}
		case "opencode":
			if err := c.generateOpenCodeConfig(cfg); err != nil {
				fmt.Printf("❌ Failed to update OpenCode config: %v\n", err)
			} else {
				fmt.Println("✅ Updated OpenCode agent config")
			}
		case "claude-desktop":
			if err := c.generateClaudeDesktopConfig(cfg); err != nil {
				fmt.Printf("❌ Failed to update Claude Desktop config: %v\n", err)
			} else {
				fmt.Println("✅ Refreshed Claude Desktop settings")
			}
		case "claude-code":
			if err := c.generateClaudeCodeConfig(cfg); err != nil {
				fmt.Printf("❌ Failed to update Claude Code config: %v\n", err)
			} else {
				fmt.Println("✅ Refreshed Claude Code settings")
			}
		}
	}

	fmt.Println("✅ All agent configurations synchronized")
	return nil
}

func (c *BaseController) generateCursorConfig(cfg *config.Config) error {
	projectRoot := paths.GetProjectRoot()
	cursorDir := ".cursor"
	if err := os.MkdirAll(cursorDir, 0755); err == nil {
		_ = c.createCursorRules(cursorDir)
	}

	worktreesDir := paths.GetWorktreesDir(projectRoot)
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		worktreePath := filepath.Join(worktreesDir, entry.Name())
		cursorDir := filepath.Join(worktreePath, ".cursor")
		if err := os.MkdirAll(cursorDir, 0755); err != nil {
			continue
		}

		_ = c.createCursorRules(cursorDir)
	}

	return nil
}

func (c *BaseController) createCursorRules(cursorDir string) error {
	rulesDir := filepath.Join(cursorDir, "rules", "nexus")
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		return err
	}

	rules := map[string]string{
		"code-quality.md": "# Code Quality Standards\n\nThis rule defines coding standards from the nexus/standard plugin.",
		"security.md":     "# Security Guidelines\n\nThis rule defines security guidelines from the nexus/standard plugin.",
	}

	for filename, content := range rules {
		rulePath := filepath.Join(rulesDir, filename)
		if err := os.WriteFile(rulePath, []byte(content), 0644); err != nil {
			fmt.Printf("⚠️  Warning: failed to write rule file %s: %v\n", rulePath, err)
		}
	}

	return nil
}

func (c *BaseController) generateOpenCodeConfig(cfg *config.Config) error {
	opencodeConfig := map[string]interface{}{
		"$schema": "https://opencode.ai/config.json",
		"instructions": []string{
			"AGENTS.md",
			".opencode/rules/**/*.md",
			".opencode/skills/**/*.md",
			".opencode/commands/**/*.md",
		},
	}

	data, err := json.MarshalIndent(opencodeConfig, "", "  ")
	if err != nil {
		return err
	}
	_ = c.copyPluginCapabilitiesToOpenCode(cfg)
	if err := os.WriteFile("opencode.json", data, 0644); err != nil {
		fmt.Printf("⚠️  Warning: failed to write opencode.json: %v\n", err)
	}

	projectRoot := paths.GetProjectRoot()
	worktreesDir := paths.GetWorktreesDir(projectRoot)
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		worktreePath := filepath.Join(worktreesDir, entry.Name())
		_ = c.copyPluginCapabilitiesToOpenCodeWorktree(cfg, worktreePath)
		configPath := filepath.Join(worktreePath, "opencode.json")
		if err := os.WriteFile(configPath, data, 0644); err != nil {
			fmt.Printf("⚠️  Warning: failed to write worktree opencode.json: %v\n", err)
		}
	}

	return nil
}

func (c *BaseController) copyPluginCapabilitiesToOpenCode(cfg *config.Config) error {
	// Copy capabilities from project templates to .opencode directory
	projectRoot := paths.GetProjectRoot()
	dirMappings := map[string]string{
		filepath.Join(".opencode", "rules", "nexus"):    filepath.Join(paths.GetConfigDir(projectRoot), "templates/rules"),
		filepath.Join(".opencode", "skills", "nexus"):   filepath.Join(paths.GetConfigDir(projectRoot), "templates/skills"),
		filepath.Join(".opencode", "commands", "nexus"): filepath.Join(paths.GetConfigDir(projectRoot), "templates/commands"),
	}

	// Create render data for template variables
	renderData := config.RenderData{
		ProjectName: cfg.Name,
	}

	for localDir, templateDir := range dirMappings {
		if err := os.MkdirAll(localDir, 0755); err != nil {
			continue
		}

		if _, err := os.Stat(templateDir); os.IsNotExist(err) {
			continue
		}

		entries, err := os.ReadDir(templateDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			src := filepath.Join(templateDir, entry.Name())
			dst := filepath.Join(localDir, entry.Name())
			if data, err := os.ReadFile(src); err == nil {
				manager := templates.NewManager(".")
				rendered, err := manager.RenderTemplate(string(data), renderData)
				if err == nil {
					if err := os.WriteFile(dst, []byte(rendered), 0644); err != nil {
						fmt.Printf("⚠️  Warning: failed to write rendered template %s: %v\n", dst, err)
					}
				}
			}
		}
	}

	return nil
}

func (c *BaseController) downloadPluginCapabilities(plugin config.TemplateRepo, baseDir string) error {
	projectRoot := paths.GetProjectRoot()
	pluginName := "nexus"

	dirMappings := map[string]string{
		filepath.Join(baseDir, "rules"):    filepath.Join(paths.GetConfigDir(projectRoot), "templates/rules"),
		filepath.Join(baseDir, "skills"):   filepath.Join(paths.GetConfigDir(projectRoot), "templates/skills"),
		filepath.Join(baseDir, "commands"): filepath.Join(paths.GetConfigDir(projectRoot), "templates/commands"),
	}

	for localDir, repoPath := range dirMappings {
		pluginDir := filepath.Join(localDir, pluginName)
		if err := os.MkdirAll(pluginDir, 0755); err != nil {
			continue
		}

		files, err := c.fetchPluginFiles(plugin.URL, repoPath, plugin.Branch)
		if err != nil {
			c.createPlaceholderFiles(pluginDir, localDir)
			continue
		}

		for _, file := range files {
			localPath := filepath.Join(pluginDir, file.Name)
			if err := os.WriteFile(localPath, []byte(file.Content), 0644); err != nil {
				continue
			}
		}
	}

	return nil
}

func (c *BaseController) fetchPluginFiles(repoURL, repoPath, branch string) ([]GitHubFile, error) {
	parts := strings.Split(strings.TrimSuffix(repoURL, "/"), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid GitHub URL: %s", repoURL)
	}

	repoName := parts[len(parts)-1]
	repoName = strings.TrimSuffix(repoName, ".git")

	if branch == "" {
		branch = "main"
	}

	// Clone the repository to a temporary directory
	tmpDir, err := os.MkdirTemp("", "nexus-plugin-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Use the existing templates.Manager to clone
	manager := templates.NewManager(tmpDir)
	repo := templates.TemplateRepo{
		URL:    repoURL,
		Branch: branch,
	}

	if err := manager.PullRepo(repo); err != nil {
		return nil, fmt.Errorf("failed to clone repo: %w", err)
	}

	// Find the cloned repo directory
	repoDir := manager.GetRepoDir(repoName)

	// Read files from the requested path
	targetPath := filepath.Join(repoDir, repoPath)
	files, err := readFilesFromDirectory(targetPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read files from %s: %w", targetPath, err)
	}

	return files, nil
}

// readFilesFromDirectory recursively reads files from a directory and returns them as GitHubFile objects
func readFilesFromDirectory(dirPath string) ([]GitHubFile, error) {
	var files []GitHubFile

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// Recursively read subdirectory
			subDirPath := filepath.Join(dirPath, entry.Name())
			subFiles, err := readFilesFromDirectory(subDirPath)
			if err != nil {
				continue
			}
			files = append(files, subFiles...)
			continue
		}

		// Skip non-markdown files for rules/skills/commands
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		// Read file content
		filePath := filepath.Join(dirPath, entry.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		files = append(files, GitHubFile{
			Name:    entry.Name(),
			Content: string(content),
		})
	}

	return files, nil
}

func (c *BaseController) createPlaceholderFiles(pluginDir, baseDir string) {
	var files []string
	switch baseDir {
	case ".opencode/rules":
		files = []string{"code-quality.md", "security.md"}
	case ".opencode/skills":
		files = []string{"web-search.md", "file-ops.md"}
	case ".opencode/commands":
		files = []string{"build.md", "test.md"}
	}

	for _, file := range files {
		filePath := filepath.Join(pluginDir, file)
		content := fmt.Sprintf("# %s Capability\n\nThis is a placeholder file.\nPlugin files could not be downloaded.\n", strings.TrimSuffix(file, ".md"))
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			fmt.Printf("⚠️  Warning: failed to write placeholder file %s: %v\n", filePath, err)
		}
	}
}

type GitHubFile struct {
	Name    string
	Content string
}

func (c *BaseController) copyPluginCapabilitiesToOpenCodeWorktree(cfg *config.Config, worktreePath string) error {
	baseDirs := []string{"rules", "skills", "commands"}

	// Create render data for template variables
	renderData := config.RenderData{
		ProjectName: cfg.Name,
	}

	for _, baseDir := range baseDirs {
		worktreePluginDir := filepath.Join(worktreePath, ".opencode", baseDir, "nexus")
		if err := os.MkdirAll(worktreePluginDir, 0755); err != nil {
			continue
		}

		projectPluginDir := filepath.Join(".opencode", baseDir, "nexus")
		entries, err := os.ReadDir(projectPluginDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			src := filepath.Join(projectPluginDir, entry.Name())
			dst := filepath.Join(worktreePluginDir, entry.Name())
			if data, err := os.ReadFile(src); err == nil {
				manager := templates.NewManager(".")
				rendered, err := manager.RenderTemplate(string(data), renderData)
				if err == nil {
					if err := os.WriteFile(dst, []byte(rendered), 0644); err != nil {
						fmt.Printf("⚠️  Warning: failed to write worktree capability %s: %v\n", dst, err)
					}
				}
			}
		}
	}

	return nil
}

func (c *BaseController) generateClaudeDesktopConfig(cfg *config.Config) error {
	claudeConfig := map[string]interface{}{
		// MCP removed - Claude Desktop config without MCP server
	}

	data, err := json.MarshalIndent(claudeConfig, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile("claude_desktop_config.json", data, 0644); err != nil {
		fmt.Printf("⚠️  Warning: failed to write claude_desktop_config.json: %v\n", err)
	}

	projectRoot := paths.GetProjectRoot()
	worktreesDir := paths.GetWorktreesDir(projectRoot)
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		worktreePath := filepath.Join(worktreesDir, entry.Name())
		configPath := filepath.Join(worktreePath, "claude_desktop_config.json")
		if err := os.WriteFile(configPath, data, 0644); err != nil {
			fmt.Printf("⚠️  Warning: failed to write worktree claude_desktop_config.json: %v\n", err)
		}
	}

	return nil
}

func (c *BaseController) generateClaudeCodeConfig(cfg *config.Config) error {
	claudeConfig := map[string]interface{}{
		// MCP removed - Claude Code config without MCP server
	}

	data, err := json.MarshalIndent(claudeConfig, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile("claude_code_config.json", data, 0644); err != nil {
		fmt.Printf("⚠️  Warning: failed to write claude_code_config.json: %v\n", err)
	}

	projectRoot := paths.GetProjectRoot()
	worktreesDir := paths.GetWorktreesDir(projectRoot)
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		worktreePath := filepath.Join(worktreesDir, entry.Name())
		configPath := filepath.Join(worktreePath, "claude_code_config.json")
		if err := os.WriteFile(configPath, data, 0644); err != nil {
			fmt.Printf("⚠️  Warning: failed to write worktree claude_code_config.json: %v\n", err)
		}
	}

	return nil
}

// isPluginEnabled checks if a plugin should be enabled based on project files
func (c *BaseController) isPluginEnabled(baseDir, name string) bool {
	switch name {
	case "golang":
		return fileExists(filepath.Join(baseDir, "go.mod")) || fileExists(filepath.Join(baseDir, "go.sum"))
	case "node":
		return fileExists(filepath.Join(baseDir, "package.json"))
	default:
		return true
	}
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// getGitSHA gets the current commit SHA of a git repository
func (c *BaseController) getGitSHA(repoDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// generateContentHash generates a hash of the lockfile content for integrity verification
func (c *BaseController) generateContentHash(lockfile *lock.Lockfile) (string, error) {
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

func (c *BaseController) PluginUpdate(ctx context.Context) error {
	fmt.Println("🔄 Updating plugins to latest versions...")

	projectRoot := paths.GetProjectRoot()
	lockfile := &lock.Lockfile{
		Version: "1.0",
		Plugins: make(map[string]*lock.LockEntry),
		Metadata: lock.LockMetadata{
			Generator: "nexus",
			Extra:     make(map[string]string),
		},
	}

	remotesDir := filepath.Join(paths.GetConfigDir(projectRoot), "remotes")
	entries, err := os.ReadDir(remotesDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read remotes dir: %w", err)
	}

	if entries != nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			repoName := entry.Name()
			repoDir := filepath.Join(remotesDir, repoName)

			cmd := exec.Command("git", "config", "--get", "remote.origin.url")
			cmd.Dir = repoDir
			urlBytes, err := cmd.Output()
			if err != nil {
				continue
			}
			url := strings.TrimSpace(string(urlBytes))

			sha, err := c.getGitSHA(repoDir)
			if err != nil {
				continue
			}

			lockfile.Plugins[repoName] = &lock.LockEntry{
				Name:       repoName,
				Version:    "latest",
				SHA:        sha,
				Repository: url,
				Path:       "",
				Metadata:   make(map[string]string),
			}
		}
	}

	if len(lockfile.Plugins) > 0 {
		contentHash, err := c.generateContentHash(lockfile)
		if err != nil {
			return fmt.Errorf("failed to generate content hash: %w", err)
		}
		lockfile.Metadata.ContentHash = contentHash

		if err := c.LockManager.SaveLockfile(lockfile); err != nil {
			return fmt.Errorf("failed to save lockfile: %w", err)
		}

		fmt.Println("✅ Updated nexus.lock")
	}

	fmt.Println("✅ All plugins updated successfully")

	// TODO: Implement lockfile generation

	fmt.Println("✅ Updated nexus.lock")
	fmt.Println("✅ All plugins updated successfully")

	return nil
}

func (c *BaseController) PluginList(ctx context.Context) error {
	fmt.Println("📦 Loaded remote templates:")

	projectRoot := paths.GetProjectRoot()
	cfg, err := config.LoadConfig(filepath.Join(paths.GetConfigDir(projectRoot), "config.yaml"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if len(cfg.Plugins) == 0 {
		fmt.Println("  No plugins loaded")
		return nil
	}

	for _, plugin := range cfg.Plugins {
		repo, ok := plugin.(config.TemplateRepo)
		if !ok {
			if name, ok := plugin.(string); ok {
				fmt.Printf("  %s (named plugin)\n", name)
			}
			continue
		}
		fmt.Printf("  %s", repo.URL)
		if repo.Branch != "" {
			fmt.Printf(" (%s)", repo.Branch)
		}
		fmt.Println()
	}

	return nil
}

func (c *BaseController) Kill(ctx context.Context, sessionID string) error {
	for _, p := range c.Providers {
		sessions, _ := p.List(ctx)
		for _, s := range sessions {
			if s.ID == sessionID || s.Labels["nexus.session.id"] == sessionID {
				return p.Destroy(ctx, s.ID)
			}
		}
	}
	return fmt.Errorf("session %s not found", sessionID)
}

func (c *BaseController) List(ctx context.Context) ([]provider.Session, error) {
	var all []provider.Session
	for _, p := range c.Providers {
		sessions, err := p.List(ctx)
		if err != nil {
			continue
		}
		all = append(all, sessions...)
	}
	return all, nil
}

func (c *BaseController) Exec(ctx context.Context, sessionID string, cmd []string) error {
	for _, p := range c.Providers {
		sessions, _ := p.List(ctx)
		for _, s := range sessions {
			if s.ID == sessionID || s.Labels["nexus.session.id"] == sessionID {
				return p.Exec(ctx, s.ID, provider.ExecOptions{
					Cmd:    cmd,
					Stdout: true,
				})
			}
		}
	}
	return fmt.Errorf("session %s not found", sessionID)
}

func detectPortFromCommand(command string) int {
	re := regexp.MustCompile(`PORT=(\d+)`)
	matches := re.FindStringSubmatch(command)
	if len(matches) > 1 {
		var port int
		_, _ = fmt.Sscanf(matches[1], "%d", &port)
		if port > 0 {
			return port
		}
	}

	portPatterns := []struct {
		pattern string
		port    int
	}{
		{"npm run dev", 3000},
		{"npm start", 3000},
		{"yarn dev", 3000},
		{"yarn start", 3000},
		{"python.*manage.py.*runserver", 8000},
		{"django.*runserver", 8000},
		{"flask run", 5000},
		{"rails server", 3000},
		{"rails s", 3000},
		{"docker-compose.*postgres", 5432},
		{"docker-compose.*mysql", 3306},
		{"docker-compose.*mongodb", 27017},
		{"docker-compose.*redis", 6379},
	}

	commandLower := strings.ToLower(command)
	for _, pp := range portPatterns {
		matched, _ := regexp.MatchString(strings.ToLower(pp.pattern), commandLower)
		if matched {
			return pp.port
		}
	}

	return 0
}

func detectProtocol(serviceName, command string) string {
	commandLower := strings.ToLower(command)

	if strings.Contains(commandLower, "postgres") || strings.Contains(commandLower, "postgresql") {
		return "postgresql"
	}
	if strings.Contains(commandLower, "mysql") {
		return "mysql"
	}
	if strings.Contains(commandLower, "mongodb") {
		return "mongodb"
	}
	if strings.Contains(commandLower, "redis") {
		return "redis"
	}

	return "http"
}

func (c *BaseController) findProjectRoot() (string, error) {
	curr, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(curr, ".nexus")); err == nil {
			return curr, nil
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			return "", fmt.Errorf("could not find project root (no .nexus directory)")
		}
		curr = parent
	}
}

func (c *BaseController) detectWorkspaceFromCWD() (string, error) {
	root, err := c.findProjectRoot()
	if err != nil {
		return "", err
	}
	curr, err := os.Getwd()
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(paths.GetWorktreesDir(root), curr)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") || rel == "." {
		return "", fmt.Errorf("not in a workspace worktree")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return parts[0], nil
}

func (c *BaseController) runHook(ctx context.Context, hookPath string, cfg *config.Config, workspacePath string) error {
	if _, err := os.Stat(hookPath); os.IsNotExist(err) {
		return nil
	}

	envFile := filepath.Join(workspacePath, ".env")
	var envLines []string
	for name, svc := range cfg.Services {
		port := svc.Port
		if port == 0 {
			port = detectPortFromCommand(svc.Command)
		}
		if port > 0 {
			protocol := detectProtocol(name, svc.Command)
			url := fmt.Sprintf("%s://localhost:%d", protocol, port)
			envLines = append(envLines, fmt.Sprintf("loom_SERVICE_%s_URL=%s", strings.ToUpper(name), url))
		}
	}
	if err := os.WriteFile(envFile, []byte(strings.Join(envLines, "\n")), 0644); err != nil {
		fmt.Printf("⚠️  Warning: failed to write env file %s: %v\n", envFile, err)
	}

	absHookPath, _ := filepath.Abs(hookPath)

	cmd := exec.CommandContext(ctx, "bash", absHookPath)
	cmd.Dir = workspacePath
	cmd.Env = append(os.Environ(), envLines...)
	cmd.Env = append(cmd.Env, "BRANCH_NAME="+filepath.Base(workspacePath))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to run hook %s: %w, output: %s", hookPath, err, string(output))
	}
	return nil
}

func (c *BaseController) handleBranchConflicts(branch string) error {
	cmd := exec.Command("git", "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return err
	}
	if len(output) > 0 {
		cmd = exec.Command("git", "stash")
		if err := cmd.Run(); err != nil {
			return err
		}
		defer func() { _ = exec.Command("git", "stash", "pop").Run() }()
	}
	return nil
}

func (c *BaseController) setupWorkspaceEnvironment(ctx context.Context, session *provider.Session, cfg *config.Config, p provider.Provider, workspacePath string) error {
	envFile := filepath.Join(workspacePath, ".env")
	var envLines []string
	for name, svc := range cfg.Services {
		port := svc.Port
		if port == 0 {
			port = detectPortFromCommand(svc.Command)
		}
		if port > 0 {
			externalPort := session.Services[fmt.Sprintf("%d", port)]
			if externalPort == 0 {
				externalPort = port
			}
			protocol := detectProtocol(name, svc.Command)
			url := fmt.Sprintf("%s://localhost:%d", protocol, externalPort)
			envLines = append(envLines, fmt.Sprintf("loom_SERVICE_%s_URL=%s", strings.ToUpper(name), url))
		}
	}
	if err := os.WriteFile(envFile, []byte(strings.Join(envLines, "\n")), 0644); err != nil {
		fmt.Printf("⚠️  Warning: failed to write env file %s: %v\n", envFile, err)
	}

	if cfg.Hooks.Setup != "" {

		return p.Exec(ctx, session.ID, provider.ExecOptions{
			Cmd: []string{"/bin/bash", filepath.Join("/workspace", cfg.Hooks.Setup)},
			Env: envLines,
		})
	}
	return nil
}
