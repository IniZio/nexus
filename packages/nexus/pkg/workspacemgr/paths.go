package workspacemgr

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

func resolveHostWorkspaceRoot(repo string) string {
	if !isLikelyLocalPath(repo) {
		return ""
	}
	cleanRepo := strings.TrimSpace(repo)
	if strings.HasPrefix(cleanRepo, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			cleanRepo = filepath.Join(home, strings.TrimPrefix(cleanRepo, "~/"))
		}
	}
	absRepo, err := filepath.Abs(cleanRepo)
	if err != nil {
		return ""
	}
	return filepath.Join(absRepo, ".worktrees")
}

func HostWorkspaceDirName(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "main"
	}
	var b strings.Builder
	for _, r := range ref {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isNumber := r >= '0' && r <= '9'
		switch {
		case isLetter || isNumber || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r == '/' || r == '\\' || r == ' ':
			b.WriteByte('-')
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "main"
	}
	return out
}

func resolveHostWorkspacePath(hostWorkspaceRoot, ref, workspaceID string) string {
	base := HostWorkspaceDirName(ref)
	if strings.TrimSpace(base) == "" {
		base = strings.TrimSpace(workspaceID)
	}
	candidate := filepath.Join(hostWorkspaceRoot, base)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	if HasValidHostWorkspaceMarker(candidate, workspaceID) {
		return candidate
	}
	fallback := strings.TrimSpace(workspaceID)
	if fallback == "" {
		fallback = "workspace"
	}
	return filepath.Join(hostWorkspaceRoot, base+"-"+fallback)
}

func normalizeLegacyWorkspacePath(ws *Workspace) bool {
	if ws == nil {
		return false
	}
	current := strings.TrimSpace(ws.LocalWorktreePath)
	if current == "" {
		return false
	}
	legacyNeedle := string(filepath.Separator) + ".nexus" + string(filepath.Separator) + "workspaces" + string(filepath.Separator)
	if !strings.Contains(current, legacyNeedle) {
		return false
	}
	hostRoot := resolveHostWorkspaceRoot(ws.Repo)
	if hostRoot == "" {
		return false
	}
	ref := strings.TrimSpace(ws.CurrentRef)
	if ref == "" {
		ref = strings.TrimSpace(ws.TargetBranch)
	}
	if ref == "" {
		ref = strings.TrimSpace(ws.Ref)
	}
	candidate := resolveHostWorkspacePath(hostRoot, ref, ws.ID)
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return false
	}
	ws.LocalWorktreePath = candidate
	ws.HostWorkspacePath = candidate
	ws.UpdatedAt = time.Now().UTC()
	return true
}

func CanonicalExistingDir(path string) string {
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

func InferredWorktreePath(ws *Workspace) string {
	if ws == nil {
		return ""
	}
	repoPath := CanonicalExistingDir(strings.TrimSpace(ws.Repo))
	if repoPath == "" {
		return ""
	}
	ref := strings.TrimSpace(ws.CurrentRef)
	if ref == "" {
		ref = strings.TrimSpace(ws.TargetBranch)
	}
	if ref == "" {
		ref = strings.TrimSpace(ws.Ref)
	}
	return filepath.Join(repoPath, ".worktrees", HostWorkspaceDirName(ref))
}

func CanonicalWorkspaceCandidate(ws *Workspace, candidate string) string {
	canonical := CanonicalExistingDir(candidate)
	if canonical == "" {
		return ""
	}
	if ws == nil {
		return canonical
	}
	if IsManagedHostWorkspacePath(canonical) && !HasValidHostWorkspaceMarker(canonical, ws.ID) {
		return ""
	}
	return canonical
}
