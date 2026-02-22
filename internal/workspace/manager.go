package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"nexus/pkg/git"
	"nexus/pkg/template"
)

type Manager struct {
	provider   Provider
	gitManager *git.Manager
}

func getDockerComposeCommand() []string {
	cmd := exec.Command("docker", "compose", "version")
	if err := cmd.Run(); err == nil {
		return []string{"docker", "compose"}
	}
	return []string{"docker-compose"}
}

type WorkspaceInfo struct {
	Name         string
	Status       string
	Port         string
	WorktreePath string
}

type Provider interface {
	Create(ctx context.Context, name string, worktreePath string) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Destroy(ctx context.Context, name string) error
	Shell(ctx context.Context, name string) error
	Exec(ctx context.Context, name string, command []string) error
	List(ctx context.Context) ([]WorkspaceInfo, error)
	Close() error
	ContainerExists(ctx context.Context, name string) (bool, error)
	StartSync(ctx context.Context, workspaceName, worktreePath string) (string, error)
	PauseSync(ctx context.Context, workspaceName string) error
	ResumeSync(ctx context.Context, workspaceName string) error
	StopSync(ctx context.Context, workspaceName string) error
	GetSyncStatus(ctx context.Context, workspaceName string) (interface{}, error)
	FlushSync(ctx context.Context, workspaceName string) error
}

func NewManager(provider Provider) *Manager {
	return &Manager{provider: provider, gitManager: git.NewManager()}
}

func NewManagerWithGitManager(provider Provider, gitManager *git.Manager) *Manager {
	return &Manager{provider: provider, gitManager: gitManager}
}

func (m *Manager) validateCreate(name string) error {
	if name == "" {
		return fmt.Errorf("workspace name cannot be empty")
	}

	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("workspace name contains invalid characters: only alphanumeric, hyphens, and underscores allowed")
		}
	}

	if m.gitManager.WorktreeExists(name) {
		return fmt.Errorf("workspace '%s' already exists as a worktree", name)
	}

	return nil
}

func (m *Manager) Repair(name string) error {
	ctx := context.Background()
	defer m.provider.Close()

	if name == "" {
		return fmt.Errorf("workspace name required")
	}

	worktreeExists := m.gitManager.WorktreeExists(name)
	containerExists, err := m.provider.ContainerExists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check container: %w", err)
	}

	if worktreeExists && containerExists {
		return fmt.Errorf("workspace '%s' is already healthy (worktree and container exist)", name)
	}

	if !worktreeExists && !containerExists {
		return fmt.Errorf("workspace '%s' does not exist - nothing to repair", name)
	}

	if worktreeExists && !containerExists {
		fmt.Printf("🔧 Worktree exists but container missing. Recreating container...\n")
		worktreePath := m.gitManager.GetWorktreePath(name)
		if err := m.provider.Create(ctx, name, worktreePath); err != nil {
			return fmt.Errorf("failed to create container: %w", err)
		}
		fmt.Printf("✅ Container recreated for workspace '%s'\n", name)
		return nil
	}

	if !worktreeExists && containerExists {
		return fmt.Errorf("container exists but worktree is missing - cannot repair automatically. Destroy and recreate the workspace.")
	}

	return nil
}

func (m *Manager) Create(name string) error {
	return m.CreateWithWorktree(name, "")
}

func (m *Manager) CreateWithWorktree(name, worktreePath string) error {
	ctx := context.Background()
	defer m.provider.Close()

	if name == "" {
		name = generateWorkspaceName()
	}

	if m.gitManager.WorktreeExists(name) {
		return fmt.Errorf("workspace '%s' already exists as a worktree", name)
	}

	if worktreePath == "" {
		var err error
		worktreePath, err = m.gitManager.CreateWorktree(name)
		if err != nil {
			return fmt.Errorf("failed to create worktree: %w", err)
		}
	} else {
		if err := m.gitManager.CreateBranch(name); err != nil {
			return fmt.Errorf("failed to create branch: %w", err)
		}
	}

	fmt.Printf("🚀 Creating workspace '%s'...\n", name)
	if err := m.provider.Create(ctx, name, worktreePath); err != nil {
		m.gitManager.RemoveWorktree(name)
		return err
	}

	if err := m.startSync(name, worktreePath); err != nil {
		fmt.Printf("⚠️  Warning: failed to start sync: %v\n", err)
	}

	nexusDir := filepath.Join(worktreePath, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		return fmt.Errorf("failed to create .nexus directory: %w", err)
	}

	currentPath := filepath.Join(nexusDir, "current")
	if err := os.WriteFile(currentPath, []byte(name), 0644); err != nil {
		return fmt.Errorf("failed to write current file: %w", err)
	}
	fmt.Printf("📝 Set current workspace to '%s'\n", name)

	fmt.Printf("\n✅ Workspace '%s' created successfully!\n", name)
	fmt.Printf("   Worktree: %s\n", worktreePath)
	fmt.Println("\nNext steps:")
	fmt.Printf("  nexus workspace up %s\n", name)
	fmt.Printf("  nexus workspace shell %s\n", name)
	return nil
}

func (m *Manager) startSync(name, worktreePath string) error {
	ctx := context.Background()
	_, err := m.provider.StartSync(ctx, name, worktreePath)
	if err != nil {
		return err
	}
	fmt.Printf("🔄 Started file sync for workspace '%s'\n", name)
	return nil
}

func (m *Manager) Up(name string) error {
	ctx := context.Background()
	defer m.provider.Close()

	if name == "" {
		name = detectWorkspaceName()
		if name == "" {
			return fmt.Errorf("no workspace specified and could not auto-detect")
		}
		fmt.Printf("Auto-detected workspace: %s\n", name)
	}

	exists, err := m.provider.ContainerExists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check container: %w", err)
	}

	if !exists {
		worktreeExists := m.gitManager.WorktreeExists(name)
		if worktreeExists {
			return fmt.Errorf("container not found for workspace '%s'. Run 'nexus workspace repair %s' to fix.", name, name)
		}
		return fmt.Errorf("workspace '%s' not found. Run 'nexus workspace create %s' first.", name, name)
	}

	fmt.Printf("🚀 Starting workspace '%s'...\n", name)
	if err := m.provider.Start(ctx, name); err != nil {
		return err
	}

	if err := m.provider.ResumeSync(ctx, name); err != nil {
		fmt.Printf("⚠️  Warning: failed to resume sync: %v\n", err)
	}

	hookPath := filepath.Join(".nexus", "hooks", "up.sh")
	if _, err := os.Stat(hookPath); err == nil {
		fmt.Println("🔧 Running up hook...")
		m.provider.Exec(ctx, name, []string{"/workspace/.nexus/hooks/up.sh"})
	}

	return nil
}

func (m *Manager) Down(name string) error {
	ctx := context.Background()
	defer m.provider.Close()

	if name == "" {
		name = detectWorkspaceName()
		if name == "" {
			return fmt.Errorf("no workspace specified and could not auto-detect")
		}
		fmt.Printf("Auto-detected workspace: %s\n", name)
	}

	fmt.Printf("🛑 Stopping workspace '%s'...\n", name)

	if err := m.provider.PauseSync(ctx, name); err != nil {
		fmt.Printf("⚠️  Warning: failed to pause sync: %v\n", err)
	}

	return m.provider.Stop(ctx, name)
}

func (m *Manager) Shell(name string) error {
	ctx := context.Background()
	defer m.provider.Close()

	if name == "" {
		name = detectWorkspaceName()
		if name == "" {
			return fmt.Errorf("no workspace specified and could not auto-detect")
		}
		fmt.Printf("Auto-detected workspace: %s\n", name)
	}

	fmt.Printf("🐚 Opening shell in workspace '%s'...\n", name)
	return m.provider.Shell(ctx, name)
}

func (m *Manager) Exec(name string, command []string) error {
	ctx := context.Background()
	defer m.provider.Close()

	if name == "" {
		return fmt.Errorf("workspace name required")
	}

	return m.provider.Exec(ctx, name, command)
}

func (m *Manager) Destroy(name string) error {
	ctx := context.Background()
	defer m.provider.Close()

	if name == "" {
		name = detectWorkspaceName()
		if name == "" {
			return fmt.Errorf("no workspace specified and could not auto-detect")
		}
		fmt.Printf("Auto-detected workspace: %s\n", name)
	}

	fmt.Printf("🗑️  Destroying workspace '%s'...\n", name)

	if err := m.provider.StopSync(ctx, name); err != nil {
		fmt.Printf("⚠️  Warning: failed to stop sync: %v\n", err)
	}

	if err := m.provider.Destroy(ctx, name); err != nil {
		return fmt.Errorf("failed to destroy workspace: %w", err)
	}

	if m.gitManager.WorktreeExists(name) {
		fmt.Printf("🌳 Removing worktree '%s'...\n", name)
		if err := m.gitManager.RemoveWorktree(name); err != nil {
			fmt.Printf("⚠️  Warning: failed to remove worktree: %v\n", err)
		}
	}

	currentPath := ".nexus/current"
	if data, err := os.ReadFile(currentPath); err == nil {
		currentWorkspace := string(data)
		currentWorkspace = strings.TrimSpace(currentWorkspace)
		if currentWorkspace == name {
			if err := os.Remove(currentPath); err == nil {
				fmt.Printf("🧹 Cleaned up .nexus/current file\n")
			}
		}
	}

	fmt.Printf("✅ Workspace '%s' destroyed successfully!\n", name)
	return nil
}

func (m *Manager) List() error {
	ctx := context.Background()
	defer m.provider.Close()

	workspaces, err := m.provider.List(ctx)
	if err != nil {
		return err
	}

	if len(workspaces) == 0 {
		fmt.Println("No workspaces found")
		return nil
	}

	fmt.Println("📦 Workspaces:")
	fmt.Println("────────────────────────────────────────────────")
	for _, ws := range workspaces {
		status := "🔴 stopped"
		if ws.Status == "running" {
			status = "🟢 running"
		}
		if ws.Port != "" {
			fmt.Printf("  %-30s %s (port %s)\n", ws.Name, status, ws.Port)
		} else {
			fmt.Printf("  %-30s %s\n", ws.Name, status)
		}
	}
	fmt.Println("────────────────────────────────────────────────")
	return nil
}

func (m *Manager) Sync(name string) error {
	defer m.provider.Close()

	if name == "" {
		name = detectWorkspaceName()
		if name == "" {
			return fmt.Errorf("no workspace specified and could not auto-detect")
		}
	}

	fmt.Printf("🔄 Syncing workspace '%s'...\n", name)

	if !m.gitManager.WorktreeExists(name) {
		return fmt.Errorf("worktree '%s' does not exist", name)
	}

	worktreePath := m.gitManager.GetWorktreePath(name)
	fmt.Printf("   Worktree path: %s\n", worktreePath)

	gitDir := filepath.Join(worktreePath, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return fmt.Errorf("not a git worktree: %s", worktreePath)
	}

	exec.Command("git", "checkout", "main").Dir = worktreePath
	exec.Command("git", "pull", "origin", "main").Dir = worktreePath

	fmt.Printf("✅ Workspace '%s' synced successfully!\n", name)
	return nil
}

func generateWorkspaceName() string {
	return fmt.Sprintf("workspace-%d", os.Getpid())
}

func detectWorkspaceName() string {
	if data, err := os.ReadFile(".nexus/current"); err == nil {
		return string(data)
	}

	if dir, err := os.Getwd(); err == nil {
		return filepath.Base(dir)
	}

	return ""
}

var validTemplates = map[string]bool{
	"node-postgres":   true,
	"python-postgres": true,
	"go-postgres":     true,
}

func (m *Manager) CreateWithTemplate(name, templateName string, vars map[string]string) error {
	ctx := context.Background()
	defer m.provider.Close()

	if name == "" {
		name = generateWorkspaceName()
	}

	if !validTemplates[templateName] {
		return fmt.Errorf("invalid template: %s. Available: node-postgres, python-postgres, go-postgres", templateName)
	}

	worktreePath, err := m.gitManager.CreateWorktree(name)
	if err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}

	fmt.Printf("🚀 Creating workspace '%s' with template '%s'...\n", name, templateName)

	if err := m.provider.Create(ctx, name, worktreePath); err != nil {
		m.gitManager.RemoveWorktree(name)
		return err
	}

	templateEngine := template.NewEngine()

	nexusDir := filepath.Join(worktreePath, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		return fmt.Errorf("failed to create .nexus directory: %w", err)
	}

	currentPath := filepath.Join(nexusDir, "current")
	if err := os.WriteFile(currentPath, []byte(name), 0644); err != nil {
		return fmt.Errorf("failed to write current file: %w", err)
	}

	if err := templateEngine.ApplyTemplate(templateName, worktreePath, vars); err != nil {
		return fmt.Errorf("failed to apply template: %w", err)
	}

	initScript := filepath.Join(worktreePath, "scripts", "init.sh")
	if _, err := os.Stat(initScript); err == nil {
		fmt.Println("🔧 Running init script...")
		cmd := exec.Command("bash", initScript)
		cmd.Dir = worktreePath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("⚠️  Init script warning: %v\n", err)
		}
	}

	composeFile := filepath.Join(worktreePath, "docker-compose.yml")
	if _, err := os.Stat(composeFile); err == nil {
		fmt.Println("🐳 Starting services with docker-compose...")
		composeCmd := getDockerComposeCommand()
		args := append(composeCmd[1:], "-f", composeFile, "up", "-d")
		cmd := exec.Command(composeCmd[0], args...)
		cmd.Dir = worktreePath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("⚠️  Docker compose warning: %v\n", err)
		}
	}

	fmt.Printf("\n✅ Workspace '%s' created successfully with template '%s'!\n", name, templateName)
	fmt.Println("\nNext steps:")
	fmt.Printf("  cd .worktree/%s\n", name)
	fmt.Printf("  nexus workspace shell %s\n", name)
	return nil
}

func (m *Manager) TemplateList() error {
	engine := template.NewEngine()
	templates := engine.ListTemplates()

	fmt.Println("📦 Available Templates:")
	fmt.Println("────────────────────────────────────────────────")
	for _, t := range templates {
		fmt.Printf("  %-20s %s\n", t.Name, t.Description)
	}
	fmt.Println("────────────────────────────────────────────────")
	return nil
}
