// Package localws manages the local side of a remote sandbox workspace:
// it clones/fetches the repository into a per-user cache, creates a git
// worktree at a configured root directory, and optionally starts a mutagen
// sync session to keep the local worktree in sync with the sandbox.
package localws

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Config holds local workspace settings. Zero values use defaults.
type Config struct {
	// WorktreeRoot is the directory under which named worktrees are created.
	// Default: $XDG_DATA_HOME/nexus/workspaces (or ~/.nexus/workspaces)
	WorktreeRoot string
	// RepoCacheDir is the directory where bare repo clones are cached.
	// Default: ~/.cache/nexus/repos
	RepoCacheDir string
}

// Manager orchestrates the local side of a sandbox workspace.
type Manager struct {
	cfg Config
}

// NewManager creates a new Manager. If cfg fields are empty, defaults are applied.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.WorktreeRoot == "" {
		cfg.WorktreeRoot = defaultWorktreeRoot()
	}
	if cfg.RepoCacheDir == "" {
		cacheBase := os.Getenv("XDG_CACHE_HOME")
		if cacheBase == "" {
			home, _ := os.UserHomeDir()
			cacheBase = filepath.Join(home, ".cache")
		}
		cfg.RepoCacheDir = filepath.Join(cacheBase, "nexus", "repos")
	}
	return &Manager{cfg: cfg}, nil
}

// SetupSpec describes a workspace for which a local worktree should be set up.
type SetupSpec struct {
	// WorkspaceID is the unique workspace identifier (used for naming the mutagen session).
	WorkspaceID string
	// WorkspaceName is a short human-readable name (used as the worktree directory name).
	WorkspaceName string
	// Repo is the git remote URL.
	Repo string
	// Ref is the branch or commit to check out.
	Ref string
	// RemotePath is the sandbox-side path that mutagen should sync to (the beta endpoint).
	// If empty, mutagen sync is skipped.
	RemotePath string
}

// SetupResult holds the outcome of a Setup call.
type SetupResult struct {
	// WorktreePath is the absolute local path of the checked-out worktree.
	WorktreePath string
	// MutagenSessionID is the name of the started mutagen session, or empty if
	// mutagen is unavailable or RemotePath was not provided.
	MutagenSessionID string
}

// Setup performs:
//  1. Clone (bare) or fetch the repository into RepoCacheDir.
//  2. Create a git worktree at WorktreeRoot/<WorkspaceName>.
//  3. Start a mutagen sync session between the worktree and RemotePath
//     (gracefully skipped if mutagen is not installed or RemotePath is empty).
func (m *Manager) Setup(ctx context.Context, spec SetupSpec) (*SetupResult, error) {
	log.Printf("[localws] Setting up local worktree for %s...", spec.WorkspaceID)

	// 1 ── Ensure the bare repo cache exists and is up to date.
	cacheDir, err := m.ensureRepoCacheDir(ctx, spec.Repo)
	if err != nil {
		return nil, fmt.Errorf("localws: cache repo: %w", err)
	}

	plannedPath := filepath.Join(m.cfg.WorktreeRoot, spec.WorkspaceName)
	log.Printf("[localws] Creating worktree at %s...", plannedPath)

	// 2 ── Create the worktree.
	worktreePath, err := m.createWorktree(ctx, cacheDir, spec.WorkspaceName, spec.Ref)
	if err != nil {
		return nil, fmt.Errorf("localws: create worktree: %w", err)
	}

	log.Printf("[localws] Worktree created, setting up mutagen...")

	result := &SetupResult{WorktreePath: worktreePath}

	// 3 ── Start mutagen sync (best-effort; missing mutagen is not an error).
	if spec.RemotePath != "" {
		sessionID, mutagenErr := m.startSync(spec.WorkspaceID, worktreePath, spec.RemotePath)
		if mutagenErr != nil {
			// Log but do not fail — mutagen is optional.
			_, _ = fmt.Fprintf(os.Stderr,
				"localws: warning: mutagen sync not started: %v\n", mutagenErr)
		} else {
			result.MutagenSessionID = sessionID
		}
	}

	return result, nil
}

// TeardownSync terminates the mutagen sync session for a workspace.
// It is a no-op if sessionID is empty or mutagen is not installed.
func (m *Manager) TeardownSync(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if _, err := exec.LookPath("mutagen"); err != nil {
		return nil // mutagen not installed; nothing to do
	}
	cmd := exec.Command("mutagen", "sync", "terminate", sessionID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mutagen sync terminate %s: %w: %s", sessionID, err, stderr.String())
	}
	return nil
}

// ensureRepoCacheDir clones the repo as a bare clone into RepoCacheDir if it
// doesn't exist yet, or fetches all remotes if it does.
// Returns the path to the bare clone directory.
func (m *Manager) ensureRepoCacheDir(ctx context.Context, repoURL string) (string, error) {
	slug := urlToSlug(repoURL)
	cacheDir := filepath.Join(m.cfg.RepoCacheDir, slug)

	if err := os.MkdirAll(m.cfg.RepoCacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache base dir: %w", err)
	}

	if _, err := os.Stat(filepath.Join(cacheDir, "HEAD")); err == nil {
		// Bare clone already exists — fetch to update it.
		cmd := gitCmd(ctx, cacheDir, "fetch", "--all", "--prune", "--tags")
		if out, err := cmd.CombinedOutput(); err != nil {
			// Non-fatal: stale cache is still usable.
			_, _ = fmt.Fprintf(os.Stderr,
				"localws: warning: git fetch failed (using cached): %v\n%s", err, out)
		}
		return cacheDir, nil
	}

	// Clone as a bare repository.
	cmd := gitCmd(ctx, m.cfg.RepoCacheDir, "clone", "--bare", repoURL, slug)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clone --bare %s: %w\n%s", repoURL, err, out)
	}
	return cacheDir, nil
}

// createWorktree adds a git worktree at WorktreeRoot/<name> checked out at
// the given ref (or the bare clone's HEAD if ref is empty).
// Returns the absolute worktree path.
func (m *Manager) createWorktree(ctx context.Context, cacheDir, name, ref string) (string, error) {
	if err := os.MkdirAll(m.cfg.WorktreeRoot, 0o755); err != nil {
		return "", fmt.Errorf("create worktree root: %w", err)
	}

	worktreePath := filepath.Join(m.cfg.WorktreeRoot, name)

	// Idempotent only for valid git worktrees. If stale path exists but is not
	// a git worktree, remove and recreate it.
	if _, err := os.Stat(worktreePath); err == nil {
		if gitErr := gitCmd(ctx, worktreePath, "rev-parse", "--is-inside-work-tree").Run(); gitErr == nil {
			return worktreePath, nil
		}
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			return "", fmt.Errorf("remove stale worktree path: %w", rmErr)
		}
	}

	args := []string{"worktree", "add", worktreePath}
	if ref != "" {
		args = append(args, ref)
	}
	cmd := gitCmd(ctx, cacheDir, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %w\n%s", err, out)
	}
	return worktreePath, nil
}

// startSync starts a mutagen two-way-safe sync session between localPath (alpha)
// and remotePath (beta). Returns the session name or an error.
func (m *Manager) startSync(workspaceID, localPath, remotePath string) (string, error) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		return "", fmt.Errorf("mutagen not found in $PATH")
	}

	sessionName := fmt.Sprintf("nexus-%s", workspaceID)
	watchInterval := fmt.Sprintf("%.0f", (2 * time.Second).Seconds())

	args := []string{
		"sync", "create",
		"--name", sessionName,
		"--sync-mode", "two-way-safe",
		"--watch-polling-interval", watchInterval,
		localPath,
		remotePath,
	}
	cmd := exec.Command("mutagen", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mutagen sync create: %w: %s", err, stderr.String())
	}
	return sessionName, nil
}

// gitCmd constructs a git command with a context, working directory, and args.
func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd
}

// defaultWorktreeRoot returns the default path for workspace worktrees.
// It respects $XDG_DATA_HOME if set; otherwise falls back to ~/.nexus/workspaces.
func defaultWorktreeRoot() string {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "nexus", "workspaces")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nexus", "workspaces")
}

// urlToSlug converts a git remote URL to a filesystem-safe slug.
// Examples:
//
//	git@github.com:org/repo.git  →  github.com-org-repo
//	https://github.com/org/repo  →  github.com-org-repo
func urlToSlug(url string) string {
	// Strip protocol
	s := url
	for _, prefix := range []string{"https://", "http://", "git://", "ssh://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	// git@host:path → host/path
	if idx := strings.Index(s, "@"); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.ReplaceAll(s, ":", "/")
	s = strings.TrimSuffix(s, ".git")
	// Replace unsafe chars
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]`)
	s = re.ReplaceAllString(s, "-")
	// Collapse consecutive hyphens
	re2 := regexp.MustCompile(`-+`)
	s = re2.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "repo"
	}
	return s
}
