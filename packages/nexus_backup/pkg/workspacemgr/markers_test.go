package workspacemgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureNexusGitignore_CreatesWithWorkspacesEntry(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, ".worktrees")
	if err := EnsureNexusGitignore(root); err != nil {
		t.Fatalf("ensure gitignore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if strings.TrimSpace(string(data)) != ".worktrees/" {
		t.Fatalf("expected .worktrees entry, got %q", string(data))
	}
}

func TestEnsureNexusGitignore_AppendsMissingEntryWithoutDuplicates(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gitignorePath := filepath.Join(repo, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("tmp/\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}

	root := filepath.Join(repo, ".worktrees")
	if err := EnsureNexusGitignore(root); err != nil {
		t.Fatalf("ensure gitignore: %v", err)
	}
	if err := EnsureNexusGitignore(root); err != nil {
		t.Fatalf("ensure gitignore second pass: %v", err)
	}
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "tmp/") || !strings.Contains(content, ".worktrees/") {
		t.Fatalf("expected tmp and .worktrees entries, got %q", content)
	}
	if strings.Count(content, ".worktrees/") != 1 {
		t.Fatalf("expected single .worktrees entry, got %q", content)
	}
}
