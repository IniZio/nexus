package cli

// Unit tests for herdrResolveWorktreeImage: how a worktree sandbox chooses its
// bootable image. The key behavior (added 2026-08-25) is that a .nexus/
// Containerfile is a complete build definition on its own — its presence
// triggers a --file build with NO separate nexus3.yaml sentinel required.

import (
	"os"
	"path/filepath"
	"testing"
)

// mkGitRoot creates dir and marks it a repo root with an empty .git file so the
// config.Load / nexusContainerfileDir walk stops there.
func mkGitRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestHerdrResolveWorktreeImage(t *testing.T) {
	t.Run("no config, no Containerfile -> base image", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "repo")
		mkGitRoot(t, root)
		flag, val, err := herdrResolveWorktreeImage(root)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if flag != "--image" || val != herdrDefaultImage {
			t.Errorf("got (%q,%q), want (--image, %q)", flag, val, herdrDefaultImage)
		}
	})

	t.Run(".nexus/Containerfile alone -> --file <repo> (no nexus3.yaml needed)", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "repo")
		mkGitRoot(t, root)
		writeFile(t, filepath.Join(root, ".nexus", "Containerfile"), "FROM ubuntu:24.04\n")
		flag, val, err := herdrResolveWorktreeImage(root)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if flag != "--file" || val != root {
			t.Errorf("got (%q,%q), want (--file, %q)", flag, val, root)
		}
	})

	t.Run(".nexus/Dockerfile alone -> --file <repo>", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "repo")
		mkGitRoot(t, root)
		writeFile(t, filepath.Join(root, ".nexus", "Dockerfile"), "FROM ubuntu:24.04\n")
		flag, val, err := herdrResolveWorktreeImage(root)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if flag != "--file" || val != root {
			t.Errorf("got (%q,%q), want (--file, %q)", flag, val, root)
		}
	})

	t.Run("Containerfile found from a subdir (walks up to repo root)", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "repo")
		mkGitRoot(t, root)
		writeFile(t, filepath.Join(root, ".nexus", "Containerfile"), "FROM ubuntu:24.04\n")
		sub := filepath.Join(root, "packages", "api")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("mkdir sub: %v", err)
		}
		flag, val, err := herdrResolveWorktreeImage(sub)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if flag != "--file" || val != root {
			t.Errorf("got (%q,%q), want (--file, %q) — walk to repo root failed", flag, val, root)
		}
	})

	t.Run("nexus3.yaml takes precedence and yields its own dir", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "repo")
		mkGitRoot(t, root)
		writeFile(t, filepath.Join(root, "nexus3.yaml"), "version: 1\n")
		writeFile(t, filepath.Join(root, ".nexus", "Containerfile"), "FROM ubuntu:24.04\n")
		flag, val, err := herdrResolveWorktreeImage(root)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if flag != "--file" || val != root {
			t.Errorf("got (%q,%q), want (--file, %q)", flag, val, root)
		}
	})

	t.Run("Containerfile beyond the .git boundary is NOT used", func(t *testing.T) {
		// .nexus/Containerfile sits ABOVE the repo root; the walk must stop at
		// .git and fall back to the base image rather than escaping the repo.
		base := t.TempDir()
		writeFile(t, filepath.Join(base, ".nexus", "Containerfile"), "FROM ubuntu:24.04\n")
		root := filepath.Join(base, "repo")
		mkGitRoot(t, root)
		flag, val, err := herdrResolveWorktreeImage(root)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if flag != "--image" || val != herdrDefaultImage {
			t.Errorf("got (%q,%q), want (--image, %q) — walk escaped the .git boundary", flag, val, herdrDefaultImage)
		}
	})
}
