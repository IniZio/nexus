// Package herdrworktree is the host-side core for creating and tearing down a
// herdr linked git worktree with a nexus sandbox bound to it. It drives the
// herdr and nexus CLIs through injectable runners and is shared by the
// `nexus herdr worktree-*` verbs and the MCP delegate tools.
package herdrworktree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Default teardown polling for the sandbox to disappear after worktree removal.
const (
	DefaultPollInterval = 500 * time.Millisecond
	DefaultPollTimeout  = 15 * time.Second
)

// Runners are the herdr, nexus and git CLI executors the core drives.
// Git, PollInterval and PollTimeout fall back to defaults when zero.
type Runners struct {
	Herdr        func(ctx context.Context, herdrBin string, argv ...string) (string, error)
	Host         func(ctx context.Context, argv ...string) (string, error)
	Git          func(ctx context.Context, argv ...string) (string, error)
	PollInterval time.Duration
	PollTimeout  time.Duration
	SpriteSync   SpriteSyncFor // nil = resolve the real sprites driver
}

// DefaultRunners returns runners backed by the real binaries.
func DefaultRunners() Runners {
	return Runners{Herdr: RunHerdrCLI, Host: RunHostCLI, Git: RunGitCLI}
}

func (r Runners) git(ctx context.Context, argv ...string) (string, error) {
	if r.Git != nil {
		return r.Git(ctx, argv...)
	}
	return RunGitCLI(ctx, argv...)
}

// RunBinary runs bin with argv and returns its combined output.
func RunBinary(ctx context.Context, bin string, argv ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// RunHostCLI runs the nexus host binary with argv.
func RunHostCLI(ctx context.Context, argv ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	return RunBinary(ctx, exe, argv...)
}

// RunHerdrCLI runs the herdr binary at herdrBin with argv.
func RunHerdrCLI(ctx context.Context, herdrBin string, argv ...string) (string, error) {
	return RunBinary(ctx, herdrBin, argv...)
}

// RunGitCLI runs git with argv.
func RunGitCLI(ctx context.Context, argv ...string) (string, error) {
	return RunBinary(ctx, "git", argv...)
}

// ResolveHerdrBin mirrors the CLI's herdr binary lookup: HERDR_BIN_PATH, else PATH.
func ResolveHerdrBin() (string, error) {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf(`herdr not found: HERDR_BIN_PATH is unset and no "herdr" binary is on PATH`)
}
