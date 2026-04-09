package store_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/store"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestNodeStore_ImplementsRepositories(t *testing.T) {
	var _ store.WorkspaceRepository = (*store.NodeStore)(nil)
	var _ store.SpotlightRepository = (*store.NodeStore)(nil)
}

func TestNodeStore_PersistAndLoadWorkspaceAndSpotlight(t *testing.T) {
	now := time.Date(2026, time.April, 9, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "node.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ws := &workspacemgr.Workspace{
		ID:            "ws-1",
		RepoID:        "repo-1",
		RepoKind:      "local",
		Repo:          "/tmp/repo",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		State:         workspacemgr.StateCreated,
		RootPath:      "/tmp/root/ws-1",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	wsPayload, err := json.Marshal(ws)
	if err != nil {
		t.Fatalf("marshal workspace: %v", err)
	}
	if err := st.UpsertWorkspaceRow(store.WorkspaceRow{
		ID:        ws.ID,
		Payload:   wsPayload,
		CreatedAt: ws.CreatedAt,
		UpdatedAt: ws.UpdatedAt,
	}); err != nil {
		t.Fatalf("upsert workspace: %v", err)
	}

	fwd := &spotlight.Forward{
		ID:          "spot-1",
		WorkspaceID: ws.ID,
		Service:     "api",
		RemotePort:  8000,
		LocalPort:   18000,
		Host:        "127.0.0.1",
		CreatedAt:   now,
	}
	fwdPayload, err := json.Marshal(fwd)
	if err != nil {
		t.Fatalf("marshal spotlight forward: %v", err)
	}
	if err := st.UpsertSpotlightForwardRow(store.SpotlightForwardRow{
		ID:          fwd.ID,
		WorkspaceID: fwd.WorkspaceID,
		LocalPort:   fwd.LocalPort,
		Payload:     fwdPayload,
		CreatedAt:   fwd.CreatedAt,
	}); err != nil {
		t.Fatalf("upsert spotlight forward: %v", err)
	}

	allWS, err := st.ListWorkspaceRows()
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(allWS) != 1 || allWS[0].ID != ws.ID {
		t.Fatalf("unexpected workspace rows: %#v", allWS)
	}

	allFwd, err := st.ListSpotlightForwardRows()
	if err != nil {
		t.Fatalf("list spotlight forwards: %v", err)
	}
	if len(allFwd) != 1 || allFwd[0].ID != fwd.ID {
		t.Fatalf("unexpected spotlight rows: %#v", allFwd)
	}

	if err := st.DeleteSpotlightForwardRow(fwd.ID); err != nil {
		t.Fatalf("delete spotlight forward: %v", err)
	}

	allFwd, err = st.ListSpotlightForwardRows()
	if err != nil {
		t.Fatalf("list spotlight forwards after delete: %v", err)
	}
	if len(allFwd) != 0 {
		t.Fatalf("unexpected spotlight rows after delete: %#v", allFwd)
	}
}
