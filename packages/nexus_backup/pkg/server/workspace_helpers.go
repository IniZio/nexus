package server

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func preferredWorkspaceRoot(wsRecord *workspacemgr.Workspace) string {
	if wsRecord == nil {
		return ""
	}

	candidates := make([]string, 0, 4)
	candidates = append(candidates, strings.TrimSpace(wsRecord.HostWorkspacePath))
	candidates = append(candidates, strings.TrimSpace(wsRecord.LocalWorktreePath))
	if inferred := inferredWorkspaceWorktree(wsRecord); inferred != "" {
		candidates = append(candidates, inferred)
	}
	candidates = append(candidates,
		strings.TrimSpace(wsRecord.Repo),
		strings.TrimSpace(wsRecord.RootPath),
	)
	for _, candidate := range candidates {
		if canonical := canonicalWorkspaceCandidate(wsRecord, candidate); canonical != "" {
			return canonical
		}
	}
	return ""
}

func inferredWorkspaceWorktree(wsRecord *workspacemgr.Workspace) string {
	if wsRecord == nil {
		return ""
	}
	repoPath := canonicalExistingDir(strings.TrimSpace(wsRecord.Repo))
	if repoPath == "" {
		return ""
	}
	ref := strings.TrimSpace(wsRecord.CurrentRef)
	if ref == "" {
		ref = strings.TrimSpace(wsRecord.TargetBranch)
	}
	if ref == "" {
		ref = strings.TrimSpace(wsRecord.Ref)
	}
	return filepath.Join(repoPath, ".worktrees", workspacemgr.HostWorkspaceDirName(ref))
}

func canonicalExistingDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ""
	}
	resolved := filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(resolved); err == nil && strings.TrimSpace(real) != "" {
		resolved = filepath.Clean(real)
	}
	return resolved
}

func canonicalWorkspaceCandidate(wsRecord *workspacemgr.Workspace, candidate string) string {
	canonical := canonicalExistingDir(candidate)
	if canonical == "" {
		return ""
	}
	if wsRecord == nil {
		return canonical
	}
	if workspacemgr.IsManagedHostWorkspacePath(canonical) && !workspacemgr.HasValidHostWorkspaceMarker(canonical, wsRecord.ID) {
		return ""
	}
	return canonical
}
