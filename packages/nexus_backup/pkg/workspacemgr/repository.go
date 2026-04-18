package workspacemgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

func nodeStorePathForRoot(root string, defaultPath string) string {
	cleanRoot := filepath.Clean(root)
	if cleanRoot == "" || defaultPath == "" {
		return defaultPath
	}

	resolvedRoot := cleanRoot
	if real, err := filepath.EvalSymlinks(cleanRoot); err == nil {
		resolvedRoot = filepath.Clean(real)
	}

	resolvedTemp := filepath.Clean(os.TempDir())
	if real, err := filepath.EvalSymlinks(resolvedTemp); err == nil {
		resolvedTemp = filepath.Clean(real)
	}

	tmpPrefix := resolvedTemp + string(filepath.Separator)
	if strings.HasPrefix(resolvedRoot+string(filepath.Separator), tmpPrefix) {
		return filepath.Join(cleanRoot, ".nexus", "state", "node.db")
	}

	return defaultPath
}

func (m *Manager) WorkspaceRepository() store.WorkspaceRepository {
	if m == nil {
		return nil
	}
	return m.workspaceRepo
}

func (m *Manager) ProjectRepository() store.ProjectRepository {
	if m == nil {
		return nil
	}
	return m.workspaceRepo
}

func (m *Manager) SpotlightRepository() store.SpotlightRepository {
	if m == nil {
		return nil
	}
	return m.workspaceRepo
}

func (m *Manager) SandboxResourceSettingsRepository() store.SandboxResourceSettingsRepository {
	if m == nil {
		return nil
	}
	return m.workspaceRepo
}

func (m *Manager) loadAll() error {
	if m.workspaceRepo == nil {
		return nil
	}

	all, err := m.workspaceRepo.ListWorkspaceRows()
	if err != nil {
		return fmt.Errorf("list sqlite workspaces: %w", err)
	}
	for _, row := range all {
		if len(row.Payload) == 0 {
			continue
		}
		var ws Workspace
		if err := json.Unmarshal(row.Payload, &ws); err != nil {
			continue
		}
		if ws.RepoID == "" {
			ws.RepoID = deriveRepoID(ws.Repo)
		}
		if ws.RepoKind == "" {
			ws.RepoKind = deriveRepoKind(ws.Repo)
		}
		if strings.TrimSpace(ws.TargetBranch) == "" {
			ws.TargetBranch = normalizeWorkspaceRef(ws.Ref)
		}
		if strings.TrimSpace(ws.CurrentRef) == "" {
			ws.CurrentRef = normalizeWorkspaceRef(ws.Ref)
		}
		if strings.TrimSpace(ws.HostWorkspacePath) == "" {
			ws.HostWorkspacePath = strings.TrimSpace(ws.LocalWorktreePath)
		}
		if ws.LineageRootID == "" {
			if ws.ParentWorkspaceID == "" {
				ws.LineageRootID = ws.ID
			} else {
				ws.LineageRootID = ws.ParentWorkspaceID
			}
		}
		if normalized := normalizeLegacyWorkspacePath(&ws); normalized {
			_ = m.persistWorkspace(&ws)
		}
		copy := ws
		m.workspaces[ws.ID] = &copy
	}
	return nil
}

func (m *Manager) persistWorkspace(ws *Workspace) error {
	if m.workspaceRepo == nil {
		return fmt.Errorf("sqlite workspace store unavailable")
	}

	payload, err := json.Marshal(ws)
	if err != nil {
		return fmt.Errorf("marshal sqlite workspace payload: %w", err)
	}
	if err := m.workspaceRepo.UpsertWorkspaceRow(store.WorkspaceRow{
		ID:        ws.ID,
		Payload:   payload,
		CreatedAt: ws.CreatedAt,
		UpdatedAt: ws.UpdatedAt,
	}); err != nil {
		return fmt.Errorf("upsert sqlite workspace: %w", err)
	}

	return nil
}

func (m *Manager) deleteRecord(id string) {
	if m.workspaceRepo != nil {
		_ = m.workspaceRepo.DeleteWorkspace(id)
	}
}
