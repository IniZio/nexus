//go:build herdr_live

// Package cli — live end-to-end proof that the herdr worktree-sandbox
// auto-create path resolves the REAL cached base image and boots a sandbox.
//
// # Why this test exists
//
// Every other worktree-sandbox test (cmd_herdr_plugin_worktree_sandbox_test.go)
// stubs image resolution: the createFn is a fake that records its argv and never
// touches the image store. That is exactly why the failure
//
//	sandbox create: service: create-and-boot ...:
//	    resolve image: no cached image with ref "nexus3-agent-base"
//
// fell straight through a fully green unit suite — nothing drove the worktree
// path through the real resolve-and-boot code. This test closes that gap: it
// builds the create argv from the SAME production helpers the worktree path uses
// (herdrResolveWorktreeImage → herdrWorktreeSandboxCreateArgs, with the same
// `<checkout>:/workspace` mountSpec and .git extra-mount as
// herdrWorktreeSandbox), then runs a real `sandbox create` against the operator's
// cached nexus3-agent-base image and asserts the guest boots.
//
// It catches three regression classes that the stubbed tests cannot:
//   - default-image REF DRIFT — herdrResolveWorktreeImage returning a ref the
//     rebuild tool (cmd/rebuild-agent-base) no longer registers;
//   - create ARGV breakage — a change to herdrWorktreeSandboxCreateArgs that
//     produces an unbootable command;
//   - resolve/boot-path regressions in `sandbox create` itself when the image
//     IS present (the normal operator state).
//
// It cannot catch "the operator never built the base image" — that is an
// environment precondition, not a code defect, and the pre-check below turns it
// into a clear skip (or a hard failure under NEXUS3_LIVE_REQUIRED=1).
//
// Run with:
//
//	TMPDIR=/tmp NEXUS3_KERNEL_PATH=$(pwd)/images/kernel/vmlinux-x86_64 \
//	  NEXUS3_LIVE_REQUIRED=1 \
//	  go test -tags herdr_live ./internal/cli/ \
//	  -run TestHerdrWorktreeSandbox_Live_ResolvesBaseImageAndBoots -v -count=1
//
// Prerequisites:
//   - /dev/kvm available
//   - NEXUS3_KERNEL_PATH set to a vmlinux image
//   - nexus3-agent-base cached (else: `go run ./cmd/rebuild-agent-base`)
//   - git in PATH
//
// # Negative-control guide
//
// To prove this test catches the ref-drift bug class:
//
//	# In cmd_herdr_plugin.go, break the default ref:
//	sed -i 's/const herdrDefaultImage = "nexus3-agent-base"/const herdrDefaultImage = "nexus3-agent-BOGUS"/' \
//	    internal/cli/cmd_herdr_plugin.go
//	go test -tags herdr_live ./internal/cli/ -run TestHerdrWorktreeSandbox_Live -v -count=1
//	# FAIL: herdrResolveWorktreeImage returned image "nexus3-agent-BOGUS", want "nexus3-agent-base"
//	# Revert before continuing.
package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/image"
)

// worktreeLiveStateHome resolves the durable state dir the real nexus3 image
// store lives under, so the create subprocess can reach the operator's cached
// nexus3-agent-base image.
//
// This package's TestMain clobbers XDG_STATE_HOME to a throwaway temp dir for
// the whole run, so reading XDG_STATE_HOME in-process yields the empty temp
// store — never the operator's real one. HOME is NOT clobbered in-process, so we
// derive the real state dir from it (store.DefaultRoot's fallback layout), with
// NEXUS3_WT_LIVE_STATE_HOME as an escape hatch for a custom XDG layout.
func worktreeLiveStateHome() string {
	if v := os.Getenv("NEXUS3_WT_LIVE_STATE_HOME"); v != "" {
		return v
	}
	if h := os.Getenv("HOME"); h != "" {
		return filepath.Join(h, ".local", "state")
	}
	return ""
}

// baseImageCached reports whether an image with ref nexus3-agent-base is present
// in the operator's real image store (<state>/nexus3/images).
func baseImageCached(t *testing.T, ref string) bool {
	t.Helper()
	state := worktreeLiveStateHome()
	if state == "" {
		return false
	}
	cacheRoot := filepath.Join(state, "nexus3", "images")
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Logf("open image cache %s: %v", cacheRoot, err)
		return false
	}
	imgs, err := cache.List(context.Background())
	if err != nil {
		t.Logf("list image cache: %v", err)
		return false
	}
	for _, img := range imgs {
		if img.Ref == ref {
			return true
		}
	}
	return false
}

// worktreeLiveCmd builds a nexus3 subprocess with XDG_STATE_HOME pinned to the
// real state dir so `sandbox create`/`exec`/`rm` address the operator's real
// image store even though this package's TestMain clobbered XDG_STATE_HOME.
func worktreeLiveCmd(binary string, args ...string) *exec.Cmd {
	realState := worktreeLiveStateHome()
	var env []string
	for _, kv := range os.Environ() {
		if len(kv) >= len("XDG_STATE_HOME=") && kv[:len("XDG_STATE_HOME=")] == "XDG_STATE_HOME=" {
			continue
		}
		env = append(env, kv)
	}
	if realState != "" {
		env = append(env, "XDG_STATE_HOME="+realState)
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	return cmd
}

func TestHerdrWorktreeSandbox_Live_ResolvesBaseImageAndBoots(t *testing.T) {
	if os.Getenv("NEXUS3_LIVE_REQUIRED") == "" {
		t.Skip("set NEXUS3_LIVE_REQUIRED=1 to run live tests (requires KVM + built images)")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		liveSkip(t, "worktree-live: /dev/kvm not available: %v", err)
	}
	if os.Getenv("NEXUS3_KERNEL_PATH") == "" {
		liveSkip(t, "worktree-live: NEXUS3_KERNEL_PATH is not set; set it to a vmlinux image")
	}
	if _, err := exec.LookPath("git"); err != nil {
		liveSkip(t, "worktree-live: git not in PATH: %v", err)
	}
	if !baseImageCached(t, herdrDefaultImage) {
		liveSkip(t, "worktree-live: %q not cached — run `go run ./cmd/rebuild-agent-base`", herdrDefaultImage)
	}

	// Build the nexus3 binary.
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3-worktree-live")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus3")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		liveSkip(t, "worktree-live: nexus3 binary cannot be built: %v\n%s", err, out)
	}

	// --- Build a REAL linked git worktree. ---
	//
	// A linked worktree's <checkout>/.git is a file ("gitdir: <main>/.git/
	// worktrees/<name>"), which is what herdrWorktreeGitDirMount parses to derive
	// the .git extra-mount. A plain `git init` directory would not exercise that.
	repoRoot := t.TempDir()
	mainDir := filepath.Join(repoRoot, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatalf("mkdir main repo: %v", err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
		}
	}
	git(mainDir, "init")
	git(mainDir, "config", "user.email", "worktree-live@nexus3.test")
	git(mainDir, "config", "user.name", "worktree-live")
	if err := os.WriteFile(filepath.Join(mainDir, "README"), []byte("worktree-live tracer\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	git(mainDir, "add", "-A")
	git(mainDir, "commit", "-m", "init")

	branch := "wt-live-" + time.Now().Format("20060102-150405")
	wtDir := filepath.Join(repoRoot, "wt")
	git(mainDir, "worktree", "add", "-b", branch, wtDir)

	// --- Reconstruct the create argv via the PRODUCTION helpers. ---
	//
	// These are the exact calls herdrWorktreeSandbox makes (steps 6, 6.5, 7):
	//   mountSpec  := info.Path + ":/workspace"
	//   extraMounts = [herdrWorktreeGitDirMount(info.Path)]
	//   imageFlag/imageVal := herdrResolveWorktreeImage(info.Path)
	//   args := herdrWorktreeSandboxCreateArgs(handle, mountSpec, imageFlag, imageVal, extraMounts)
	imageFlag, imageVal, err := herdrResolveWorktreeImage(wtDir)
	if err != nil {
		t.Fatalf("herdrResolveWorktreeImage(%s): %v", wtDir, err)
	}
	// Ref-drift guard: a worktree with no nexus3.yaml must resolve to the named
	// default base image — the same ref cmd/rebuild-agent-base registers.
	if imageFlag != "--image" || imageVal != herdrDefaultImage {
		t.Fatalf("herdrResolveWorktreeImage = (%q, %q), want (--image, %q) — default image ref drifted",
			imageFlag, imageVal, herdrDefaultImage)
	}

	gitMount := herdrWorktreeGitDirMount(wtDir)
	if gitMount == "" {
		t.Fatalf("herdrWorktreeGitDirMount(%s) = empty — linked-worktree .git mount not derived", wtDir)
	}
	extraMounts := []string{gitMount}

	handle := herdrWorktreeSandboxHandle("repo", branch)
	mountSpec := wtDir + ":/workspace"
	args := herdrWorktreeSandboxCreateArgs(handle, mountSpec, imageFlag, imageVal, extraMounts)

	t.Cleanup(func() {
		rmOut, rmErr := worktreeLiveCmd(binary, "rm", handle).CombinedOutput()
		if rmErr != nil {
			t.Logf("cleanup: nexus3 rm %s: %v\n%s", handle, rmErr, rmOut)
		} else {
			t.Logf("cleanup: nexus3 rm %s: %s", handle, rmOut)
		}
	})

	// --- Core proof: real `sandbox create` resolves nexus3-agent-base and boots. ---
	createArgs := append([]string{"sandbox", "create"}, args...)
	createOut, createErr := worktreeLiveCmd(binary, createArgs...).CombinedOutput()
	t.Logf("nexus3 sandbox create:\n%s", createOut)
	if createErr != nil {
		t.Fatalf("nexus3 sandbox create: %v\n%s\n(check NEXUS3_KERNEL_PATH and that %q is cached)",
			createErr, createOut, imageVal)
	}
	// The exact failure this test guards against must not reappear silently.
	if bytes.Contains(createOut, []byte("no cached image with ref")) {
		t.Fatalf("create succeeded-exit but output shows unresolved image:\n%s", createOut)
	}

	// --- Liveness: the guest booted and the worktree is mounted at /workspace. ---
	const tracerToken = "WORKTREE_LIVE_OK"
	script := `set -euo pipefail
test -d /workspace || { echo "FAIL: /workspace missing" >&2; exit 1; }
test -f /workspace/README || { echo "FAIL: /workspace/README missing (worktree not mounted)" >&2; exit 1; }
grep -q 'worktree-live tracer' /workspace/README || { echo "FAIL: README content wrong" >&2; exit 1; }
echo ` + tracerToken + `
`
	execOut, execErr := worktreeLiveCmd(binary, "exec", handle, "--", "/bin/bash", "-c", script).CombinedOutput()
	t.Logf("nexus3 exec (liveness):\n%s", execOut)
	if execErr != nil {
		t.Fatalf("nexus3 exec %s: %v\n%s", handle, execErr, execOut)
	}
	if !bytes.Contains(execOut, []byte(tracerToken)) {
		t.Errorf("tracer token %q absent — guest did not boot cleanly or worktree not mounted\n%s", tracerToken, execOut)
	}
}
