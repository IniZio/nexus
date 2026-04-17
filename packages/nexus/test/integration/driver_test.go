//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAllDrivers(t *testing.T) {
	for _, driver := range AllDrivers {
		driver := driver
		t.Run(driver.Backend+"/"+driver.Mode, func(t *testing.T) {
			t.Parallel()
			driver.SkipUnless(t)

			newRepo := func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				initGitRepo(t, dir)
				return dir
			}

			t.Run("Create", func(t *testing.T) {
				ws := CreateWorkspace(t, driver, newRepo(t))
				out := ExecInWorkspace(t, ws, "echo ok")
				if !strings.Contains(out, "ok") {
					t.Errorf("exec in workspace returned %q, want 'ok'", out)
				}
			})

			t.Run("PathNormalization", func(t *testing.T) {
				ws := CreateWorkspace(t, driver, newRepo(t))
				pwd := strings.TrimSpace(ExecInWorkspace(t, ws, "pwd"))
				if pwd != "/workspace" {
					t.Errorf("pwd = %q, want /workspace", pwd)
				}
			})

			t.Run("WriteRead", func(t *testing.T) {
				ws := CreateWorkspace(t, driver, newRepo(t))
				ExecInWorkspace(t, ws, "echo hello > /workspace/test-write-read.txt")
				content := strings.TrimSpace(ExecInWorkspace(t, ws, "cat /workspace/test-write-read.txt"))
				if content != "hello" {
					t.Errorf("read back %q, want 'hello'", content)
				}
			})

			t.Run("Fork", func(t *testing.T) {
				parent := CreateWorkspace(t, driver, newRepo(t))
				ExecInWorkspace(t, parent, "echo before-fork > /workspace/pre-fork.txt")

				start := time.Now()
				child := ForkWorkspace(t, parent)
				elapsed := time.Since(start)

				if elapsed > 5*time.Second {
					t.Errorf("fork took %v, want < 5s", elapsed)
				}

				preContent := strings.TrimSpace(ExecInWorkspace(t, child, "cat /workspace/pre-fork.txt"))
				if preContent != "before-fork" {
					t.Errorf("child missing pre-fork content, got %q", preContent)
				}

				ExecInWorkspace(t, parent, "echo after-fork > /workspace/post-fork-parent.txt")
				out := ExecInWorkspace(t, child, "test -f /workspace/post-fork-parent.txt && echo exists || echo absent")
				if strings.TrimSpace(out) != "absent" {
					t.Error("child must not see files written to parent after fork")
				}

				ExecInWorkspace(t, child, "echo child-write > /workspace/child-only.txt")
				out = ExecInWorkspace(t, parent, "test -f /workspace/child-only.txt && echo exists || echo absent")
				if strings.TrimSpace(out) != "absent" {
					t.Error("parent must not see files written to child after fork")
				}
			})

			t.Run("Git", func(t *testing.T) {
				ws := CreateWorkspace(t, driver, newRepo(t))
				out := ExecInWorkspace(t, ws, "git status; echo exit:$?")
				if !strings.Contains(out, "exit:0") {
					t.Errorf("git status failed inside workspace: %s", out)
				}
			})
		})
	}
}

func TestWorkspaceCoexistence(t *testing.T) {
	for _, driver := range AllDrivers {
		driver := driver
		t.Run(driver.Backend+"/"+driver.Mode, func(t *testing.T) {
			t.Parallel()
			driver.SkipUnless(t)

			projectRoot := t.TempDir()
			initGitRepo(t, projectRoot)

			wsA := CreateWorkspace(t, driver, projectRoot)
			wsB := CreateWorkspace(t, driver, projectRoot)

			for _, ws := range []WorkspaceHandle{wsA, wsB} {
				pwd := strings.TrimSpace(ExecInWorkspace(t, ws, "pwd"))
				if pwd != "/workspace" {
					t.Errorf("workspace %s pwd = %q, want /workspace", ws.ID, pwd)
				}
			}

			ExecInWorkspace(t, wsA, "echo from-a > /workspace/from-a.txt")
			out := ExecInWorkspace(t, wsB, "test -f /workspace/from-a.txt && echo exists || echo absent")
			if strings.TrimSpace(out) != "absent" {
				t.Error("workspace B must not see files from workspace A")
			}
		})
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "init")
	run("git", "config", "user.email", "test@nexus.test")
	run("git", "config", "user.name", "Nexus Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "initial commit")
}
