package workspacemgr

import (
	"log"
	"os"
	"strings"
)

func (m *Manager) Remove(id string) bool {
	removed, _ := m.RemoveWithOptions(id, RemoveOptions{DeleteHostPath: true})
	return removed
}

func (m *Manager) RemoveWithOptions(id string, opts RemoveOptions) (bool, error) {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if ok {
		delete(m.workspaces, id)
	}
	m.mu.Unlock()

	if ok {
		if err := os.RemoveAll(ws.RootPath); err != nil {
			log.Printf("workspace.remove: RemoveAll %s: %v", ws.RootPath, err)
		}
		if opts.DeleteHostPath && strings.TrimSpace(ws.LocalWorktreePath) != "" {
			cleanupLocalWorkspaceCheckout(ws.Repo, ws.LocalWorktreePath)
			if _, err := os.Stat(ws.LocalWorktreePath); err == nil {
				if err := os.RemoveAll(ws.LocalWorktreePath); err != nil {
					log.Printf("workspace.remove: RemoveAll %s: %v", ws.LocalWorktreePath, err)
				}
			}
		}
		m.deleteRecord(id)
	}

	return ok, nil
}
