package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestForkCreatesGitWorktree(t *testing.T) {
	parentDir := t.TempDir()
	childDir := t.TempDir()

	run := func(dir, name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}

	run(parentDir, "git", "init")
	run(parentDir, "git", "config", "user.email", "test@test.com")
	run(parentDir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(parentDir, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	run(parentDir, "git", "add", ".")
	run(parentDir, "git", "commit", "-m", "initial")

	// Remove the pre-created child dir since git worktree add creates it
	os.Remove(childDir)

	err := ForkWorktree(parentDir, childDir)
	if err != nil {
		t.Fatalf("ForkWorktree: %v", err)
	}

	// child dir must contain file.txt
	content, err := os.ReadFile(filepath.Join(childDir, "file.txt"))
	if err != nil {
		t.Fatalf("child worktree missing file.txt: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("expected 'hello', got %q", content)
	}

	// child is independent — write to child does not affect parent
	os.WriteFile(filepath.Join(childDir, "child-only.txt"), []byte("child"), 0644)
	if _, err := os.Stat(filepath.Join(parentDir, "child-only.txt")); err == nil {
		t.Error("child file must not appear in parent")
	}
}
