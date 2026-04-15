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
	var _ store.SandboxResourceSettingsRepository = (*store.NodeStore)(nil)
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

func TestNodeStore_ReplaceSpotlightForwardRows_ReplacesSetIncludingPortTakeover(t *testing.T) {
	now := time.Date(2026, time.April, 9, 13, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "node.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	seed := []store.SpotlightForwardRow{
		{ID: "old", WorkspaceID: "ws-old", LocalPort: 18000, Payload: []byte(`{"id":"old","v":1}`), CreatedAt: now},
		{ID: "keep", WorkspaceID: "ws-keep", LocalPort: 18001, Payload: []byte(`{"id":"keep","v":1}`), CreatedAt: now},
	}
	for _, row := range seed {
		if err := st.UpsertSpotlightForwardRow(row); err != nil {
			t.Fatalf("seed spotlight row %q: %v", row.ID, err)
		}
	}

	desired := []store.SpotlightForwardRow{
		{ID: "keep", WorkspaceID: "ws-keep-updated", LocalPort: 18001, Payload: []byte(`{"id":"keep","v":2}`), CreatedAt: now.Add(time.Minute)},
		{ID: "new", WorkspaceID: "ws-new", LocalPort: 18000, Payload: []byte(`{"id":"new","v":1}`), CreatedAt: now.Add(2 * time.Minute)},
	}

	if err := st.ReplaceSpotlightForwardRows(desired); err != nil {
		t.Fatalf("replace spotlight rows: %v", err)
	}

	rows, err := st.ListSpotlightForwardRows()
	if err != nil {
		t.Fatalf("list spotlight rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 spotlight rows after replace, got %d: %#v", len(rows), rows)
	}

	byID := map[string]store.SpotlightForwardRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}

	if _, ok := byID["old"]; ok {
		t.Fatalf("stale row \"old\" should be deleted: %#v", rows)
	}

	keep, ok := byID["keep"]
	if !ok {
		t.Fatalf("expected updated row \"keep\" to exist: %#v", rows)
	}
	if keep.WorkspaceID != "ws-keep-updated" || string(keep.Payload) != `{"id":"keep","v":2}` {
		t.Fatalf("expected updated \"keep\" row, got: %#v", keep)
	}

	newRow, ok := byID["new"]
	if !ok {
		t.Fatalf("expected new row \"new\" to exist: %#v", rows)
	}
	if newRow.LocalPort != 18000 {
		t.Fatalf("expected new row to take over stale local port 18000, got: %#v", newRow)
	}
}

func TestNodeStore_ReplaceSpotlightForwardRows_RollsBackOnFailure(t *testing.T) {
	now := time.Date(2026, time.April, 9, 14, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "node.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	seed := []store.SpotlightForwardRow{
		{ID: "old", WorkspaceID: "ws-old", LocalPort: 19000, Payload: []byte(`{"id":"old"}`), CreatedAt: now},
		{ID: "keep", WorkspaceID: "ws-keep", LocalPort: 19001, Payload: []byte(`{"id":"keep"}`), CreatedAt: now},
	}
	for _, row := range seed {
		if err := st.UpsertSpotlightForwardRow(row); err != nil {
			t.Fatalf("seed spotlight row %q: %v", row.ID, err)
		}
	}

	err = st.ReplaceSpotlightForwardRows([]store.SpotlightForwardRow{
		{ID: "new", WorkspaceID: "ws-new", LocalPort: 19000, Payload: []byte(`{"id":"new"}`), CreatedAt: now.Add(time.Minute)},
		{ID: "dup", WorkspaceID: "ws-dup", LocalPort: 19000, Payload: []byte(`{"id":"dup"}`), CreatedAt: now.Add(2 * time.Minute)},
	})
	if err == nil {
		t.Fatalf("expected replace to fail due to duplicate local port in desired set")
	}

	rows, listErr := st.ListSpotlightForwardRows()
	if listErr != nil {
		t.Fatalf("list spotlight rows: %v", listErr)
	}
	if len(rows) != 2 {
		t.Fatalf("expected rollback to preserve 2 original rows, got %d: %#v", len(rows), rows)
	}

	byID := map[string]store.SpotlightForwardRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}

	if _, ok := byID["old"]; !ok {
		t.Fatalf("expected rollback to restore stale row \"old\": %#v", rows)
	}
	if _, ok := byID["keep"]; !ok {
		t.Fatalf("expected rollback to preserve row \"keep\": %#v", rows)
	}
	if _, ok := byID["new"]; ok {
		t.Fatalf("expected rollback to remove partially inserted row \"new\": %#v", rows)
	}
}

func TestNodeStore_PersistAndLoadSandboxResourceSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	_, ok, err := st.GetSandboxResourceSettings()
	if err != nil {
		t.Fatalf("get sandbox settings before upsert: %v", err)
	}
	if ok {
		t.Fatal("expected no sandbox settings row before upsert")
	}

	upsert := store.SandboxResourceSettingsRow{
		DefaultMemoryMiB: 2048,
		DefaultVCPUs:     2,
		MaxMemoryMiB:     8192,
		MaxVCPUs:         8,
		UpdatedAt:        time.Now().UTC(),
	}
	if err := st.UpsertSandboxResourceSettings(upsert); err != nil {
		t.Fatalf("upsert sandbox settings: %v", err)
	}

	got, ok, err := st.GetSandboxResourceSettings()
	if err != nil {
		t.Fatalf("get sandbox settings: %v", err)
	}
	if !ok {
		t.Fatal("expected sandbox settings row to exist")
	}
	if got.DefaultMemoryMiB != upsert.DefaultMemoryMiB || got.MaxVCPUs != upsert.MaxVCPUs {
		t.Fatalf("unexpected sandbox settings row: %#v", got)
	}
}
