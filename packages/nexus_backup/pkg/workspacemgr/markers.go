package workspacemgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const hostWorkspaceMarkerFile = ".nexus-workspace-marker.json"

type hostWorkspaceMarker struct {
	WorkspaceID string `json:"workspaceId"`
}

func HostWorkspaceMarkerPath(workspacePath string) string {
	return filepath.Join(strings.TrimSpace(workspacePath), hostWorkspaceMarkerFile)
}

func WriteHostWorkspaceMarker(workspacePath string, workspaceID string) error {
	markerPath := HostWorkspaceMarkerPath(workspacePath)
	payload, err := json.Marshal(hostWorkspaceMarker{WorkspaceID: strings.TrimSpace(workspaceID)})
	if err != nil {
		return err
	}
	return os.WriteFile(markerPath, payload, 0o644)
}

func HasValidHostWorkspaceMarker(workspacePath string, workspaceID string) bool {
	markerPath := HostWorkspaceMarkerPath(workspacePath)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return false
	}
	var marker hostWorkspaceMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false
	}
	return strings.TrimSpace(marker.WorkspaceID) != "" && strings.TrimSpace(marker.WorkspaceID) == strings.TrimSpace(workspaceID)
}

func IsManagedHostWorkspacePath(path string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return false
	}
	legacyNeedle := string(filepath.Separator) + ".nexus" + string(filepath.Separator) + "workspaces" + string(filepath.Separator)
	worktreesNeedle := string(filepath.Separator) + ".worktrees" + string(filepath.Separator)
	candidate := cleaned + string(filepath.Separator)
	return strings.Contains(candidate, legacyNeedle) || strings.Contains(candidate, worktreesNeedle)
}

func EnsureNexusGitignore(hostWorkspaceRoot string) error {
	root := strings.TrimSpace(hostWorkspaceRoot)
	if root == "" {
		return nil
	}
	cleanRoot := filepath.Clean(root)
	gitignorePath := ""
	requiredEntries := []string{}
	switch filepath.Base(cleanRoot) {
	case "workspaces":
		// Legacy managed root at <repo>/.nexus/workspaces.
		nexusDir := filepath.Clean(filepath.Dir(cleanRoot))
		if filepath.Base(nexusDir) != ".nexus" {
			return nil
		}
		if err := os.MkdirAll(nexusDir, 0o755); err != nil {
			return fmt.Errorf("create .nexus dir: %w", err)
		}
		gitignorePath = filepath.Join(nexusDir, ".gitignore")
		requiredEntries = []string{"workspaces/"}
	case ".worktrees":
		// Current managed root at <repo>/.worktrees.
		repoRoot := filepath.Clean(filepath.Dir(cleanRoot))
		if err := os.MkdirAll(repoRoot, 0o755); err != nil {
			return fmt.Errorf("create repo dir: %w", err)
		}
		gitignorePath = filepath.Join(repoRoot, ".gitignore")
		requiredEntries = []string{".worktrees/"}
	default:
		return nil
	}

	data, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .nexus/.gitignore: %w", err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := []string{}
	if strings.TrimSpace(content) != "" {
		lines = strings.Split(strings.TrimRight(content, "\n"), "\n")
	}
	existing := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		existing[strings.TrimSpace(line)] = struct{}{}
	}
	for _, entry := range requiredEntries {
		if _, ok := existing[entry]; ok {
			continue
		}
		lines = append(lines, entry)
	}

	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if out == "\n" {
		out = requiredEntries[0] + "\n"
	}
	if writeErr := os.WriteFile(gitignorePath, []byte(out), 0o644); writeErr != nil {
		return fmt.Errorf("write .nexus/.gitignore: %w", writeErr)
	}
	return nil
}
