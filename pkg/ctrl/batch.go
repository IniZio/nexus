package ctrl

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/nexus/nexus/pkg/paths"
	"github.com/nexus/nexus/pkg/provider"

	"gopkg.in/yaml.v3"
)

// WorkspaceGroup represents a group of workspaces
type WorkspaceGroup struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	Workspaces  []string `yaml:"workspaces"`
	Tags        []string `yaml:"tags,omitempty"`
}

// WorkspaceGroupConfig manages workspace groups
type WorkspaceGroupConfig struct {
	Groups     []WorkspaceGroup `yaml:"groups"`
	Aliases    map[string]string `yaml:"aliases,omitempty"`
	DefaultGroup string          `yaml:"default_group,omitempty"`
}

// WorkspaceGroupManager manages workspace groups
type WorkspaceGroupManager struct {
	configPath string
	config     WorkspaceGroupConfig
}

func NewWorkspaceGroupManager() *WorkspaceGroupManager {
	projectRoot := paths.GetProjectRoot()
	configPath := filepath.Join(paths.GetConfigDir(projectRoot), "workspace-groups.yaml")
	return &WorkspaceGroupManager{
		configPath: configPath,
		config:     WorkspaceGroupConfig{},
	}
}

// Load loads the group configuration
func (m *WorkspaceGroupManager) Load() error {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to load group config: %w", err)
	}
	return yaml.Unmarshal(data, &m.config)
}

// Save saves the group configuration
func (m *WorkspaceGroupManager) Save() error {
	data, err := yaml.Marshal(m.config)
	if err != nil {
		return fmt.Errorf("failed to marshal group config: %w", err)
	}
	return os.WriteFile(m.configPath, data, 0600)
}

// AddGroup adds a new workspace group
func (m *WorkspaceGroupManager) AddGroup(name, description string, workspaces []string) error {
	for _, g := range m.config.Groups {
		if g.Name == name {
			return fmt.Errorf("group '%s' already exists", name)
		}
	}
	m.config.Groups = append(m.config.Groups, WorkspaceGroup{
		Name:        name,
		Description: description,
		Workspaces:  workspaces,
	})
	return m.Save()
}

// AddWorkspaceToGroup adds a workspace to a group
func (m *WorkspaceGroupManager) AddWorkspaceToGroup(groupName, workspace string) error {
	for i, g := range m.config.Groups {
		if g.Name == groupName {
			for _, w := range g.Workspaces {
				if w == workspace {
					return nil // already in group
				}
			}
			m.config.Groups[i].Workspaces = append(m.config.Groups[i].Workspaces, workspace)
			return m.Save()
		}
	}
	return fmt.Errorf("group '%s' not found", groupName)
}

// GetGroup returns a group by name
func (m *WorkspaceGroupManager) GetGroup(name string) *WorkspaceGroup {
	for i := range m.config.Groups {
		if m.config.Groups[i].Name == name {
			return &m.config.Groups[i]
		}
	}
	return nil
}

// ListGroups lists all groups
func (m *WorkspaceGroupManager) ListGroups() []WorkspaceGroup {
	return m.config.Groups
}

// SetAlias creates an alias for quick workspace access
func (m *WorkspaceGroupManager) SetAlias(alias, workspace string) error {
	if m.config.Aliases == nil {
		m.config.Aliases = make(map[string]string)
	}
	m.config.Aliases[alias] = workspace
	return m.Save()
}

// ResolveAlias resolves an alias to workspace name
func (m *WorkspaceGroupManager) ResolveAlias(alias string) string {
	if ws, ok := m.config.Aliases[alias]; ok {
		return ws
	}
	return alias
}

// GetWorkspacesForGroup returns all workspace names for a group (or single workspace)
func (m *WorkspaceGroupManager) GetWorkspacesForGroup(name string) ([]string, error) {
	// Check if it's a group
	group := m.GetGroup(name)
	if group != nil {
		return group.Workspaces, nil
	}

	// Check if it's an alias
	if ws := m.ResolveAlias(name); ws != name {
		return []string{ws}, nil
	}

	// Assume it's a single workspace
	return []string{name}, nil
}

// BatchResult holds the result of a batch operation
type BatchResult struct {
	Workspace string
	Success   bool
	Error     error
}

// WorkspaceBatchUp starts multiple workspaces in parallel
func (c *BaseController) WorkspaceBatchUp(ctx context.Context, workspaces []string) error {
	if len(workspaces) == 0 {
		return fmt.Errorf("no workspaces specified")
	}

	fmt.Printf("🚀 Starting %d workspaces in parallel...\n", len(workspaces))

	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]BatchResult, len(workspaces))

	for i, ws := range workspaces {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()

			err := c.WorkspaceUp(ctx, name)
			mu.Lock()
			results[idx] = BatchResult{
				Workspace: name,
				Success:   err == nil,
				Error:     err,
			}
			mu.Unlock()

			if err != nil {
				fmt.Printf("  ❌ %s: %v\n", name, err)
			} else {
				fmt.Printf("  ✅ %s started\n", name)
			}
		}(i, ws)
	}

	wg.Wait()

	// Report summary
	successCount := 0
	failCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}

	fmt.Printf("\n📊 Batch complete: %d successful, %d failed\n", successCount, failCount)

	if failCount > 0 {
		return fmt.Errorf("some workspaces failed to start")
	}
	return nil
}

// WorkspaceBatchDown stops multiple workspaces in parallel
func (c *BaseController) WorkspaceBatchDown(ctx context.Context, workspaces []string) error {
	if len(workspaces) == 0 {
		return fmt.Errorf("no workspaces specified")
	}

	fmt.Printf("🛑 Stopping %d workspaces in parallel...\n", len(workspaces))

	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]BatchResult, len(workspaces))

	for i, ws := range workspaces {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()

			err := c.WorkspaceDown(ctx, name)
			mu.Lock()
			results[idx] = BatchResult{
				Workspace: name,
				Success:   err == nil,
				Error:     err,
			}
			mu.Unlock()

			if err != nil {
				fmt.Printf("  ❌ %s: %v\n", name, err)
			} else {
				fmt.Printf("  ⏹️  %s stopped\n", name)
			}
		}(i, ws)
	}

	wg.Wait()

	// Report summary
	successCount := 0
	failCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}

	fmt.Printf("\n📊 Batch complete: %d successful, %d failed\n", successCount, failCount)

	if failCount > 0 {
		return fmt.Errorf("some workspaces failed to stop")
	}
	return nil
}

// WorkspaceExecAll executes a command in multiple workspaces
func (c *BaseController) WorkspaceExecAll(ctx context.Context, workspaces []string, cmd []string, parallel bool) error {
	if len(workspaces) == 0 {
		return fmt.Errorf("no workspaces specified")
	}

	if parallel {
		fmt.Printf("⚡ Executing in %d workspaces in parallel...\n", len(workspaces))
	} else {
		fmt.Printf("🔄 Executing in %d workspaces sequentially...\n", len(workspaces))
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]BatchResult, len(workspaces))

	execFn := func(idx int, name string) {
		defer wg.Done()

		err := c.Exec(ctx, name, cmd)
		mu.Lock()
		results[idx] = BatchResult{
			Workspace: name,
			Success:   err == nil,
			Error:     err,
		}
		mu.Unlock()

		if err != nil {
			fmt.Printf("  ❌ %s: %v\n", name, err)
		} else {
			fmt.Printf("  ✅ %s: command completed\n", name)
		}
	}

	for i, ws := range workspaces {
		if parallel {
			wg.Add(1)
			go execFn(i, ws)
		} else {
			execFn(i, ws)
		}
	}

	if parallel {
		wg.Wait()
	}

	// Report summary
	successCount := 0
	failCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}

	fmt.Printf("\n📊 Execution complete: %d successful, %d failed\n", successCount, failCount)

	if failCount > 0 {
		return fmt.Errorf("some executions failed")
	}
	return nil
}

// WorkspaceListAll lists all workspaces with their status
func (c *BaseController) WorkspaceListAll(ctx context.Context) error {
	projectRoot := paths.GetProjectRoot()
	worktreesDir := paths.GetWorktreesDir(projectRoot)

	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return fmt.Errorf("failed to read worktrees: %w", err)
	}

	fmt.Println("📦 Workspaces")
	fmt.Println(strings.Repeat("─", 60))

	var workspaces []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			workspaces = append(workspaces, e.Name())
		}
	}

	sort.Strings(workspaces)

	for _, ws := range workspaces {
		wtPath := filepath.Join(worktreesDir, ws)

		// Check if running
		sessions, _ := c.List(ctx)
		var status string
		running := false
		for _, s := range sessions {
			if s.ID == ws || strings.Contains(s.ID, ws) {
				status = fmt.Sprintf("🟢 running (port %d)", s.SSHPort)
				running = true
				break
			}
		}
		if !running {
			// Check if worktree exists
			if _, err := os.Stat(filepath.Join(wtPath, ".git")); err == nil {
				status = "🔴 stopped"
			} else {
				status = "⚪ empty"
			}
		}

		fmt.Printf("  %-30s %s\n", ws, status)
	}

	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("  Total: %d workspaces\n", len(workspaces))

	return nil
}

// WorkspaceStatus gets detailed status of a workspace
func (c *BaseController) WorkspaceStatus(ctx context.Context, name string) error {
	fmt.Printf("📊 Workspace Status: %s\n", name)
	fmt.Println(strings.Repeat("─", 40))

	projectRoot := paths.GetProjectRoot()
	worktreesDir := paths.GetWorktreesDir(projectRoot)
	wtPath := filepath.Join(worktreesDir, name)

	// Check if exists
	if _, err := os.Stat(wtPath); os.IsNotExist(err) {
		return fmt.Errorf("workspace '%s' does not exist", name)
	}

	// Get provider session
	sessions, err := c.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}

	var session provider.Session
	found := false
	for _, s := range sessions {
		if s.ID == name || strings.Contains(s.ID, name) {
			session = s
			found = true
			break
		}
	}

	if found {
		fmt.Printf("  Status:     🟢 Running\n")
		fmt.Printf("  SSH Port:   %d\n", session.SSHPort)
		fmt.Printf("  Provider:   %s\n", session.Provider)
		fmt.Printf("  Session ID: %s\n", session.ID)

		// Get services
		if len(session.Services) > 0 {
			fmt.Println("  Services:")
			for svcName, port := range session.Services {
				fmt.Printf("    - %s: %d\n", svcName, port)
			}
		}
	} else {
		fmt.Printf("  Status:  🔴 Stopped\n")
	}

	// Show git branch
	gitBranchCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	gitBranchCmd.Dir = wtPath
	if branch, err := gitBranchCmd.Output(); err == nil {
		fmt.Printf("  Branch:   %s", string(branch))
	}

	fmt.Println(strings.Repeat("─", 40))
	return nil
}
