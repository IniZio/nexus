package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/inizio/nexus/packages/nexus/internal/domain/workspace"
)

// WorkspaceStore implements workspace.Repository using SQLite.
type WorkspaceStore struct {
	db *sql.DB
}

// NewWorkspaceStore creates a WorkspaceStore backed by the given DB.
func NewWorkspaceStore(d *DB) *WorkspaceStore {
	return &WorkspaceStore{db: d.db}
}

func (s *WorkspaceStore) Create(ctx context.Context, ws *workspace.Workspace) error {
	data, err := json.Marshal(ws)
	if err != nil {
		return fmt.Errorf("marshal workspace: %w", err)
	}
	created := ws.CreatedAt.UTC().Format(time.RFC3339Nano)
	updated := ws.UpdatedAt.UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workspaces(id, project_id, data, state, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		ws.ID, ws.ProjectID, string(data), string(ws.State), created, updated,
	)
	if err != nil {
		return fmt.Errorf("insert workspace: %w", err)
	}
	return nil
}

func (s *WorkspaceStore) Get(ctx context.Context, id string) (*workspace.Workspace, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM workspaces WHERE id = ?`, id,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, workspace.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	var ws workspace.Workspace
	if err := json.Unmarshal([]byte(data), &ws); err != nil {
		return nil, fmt.Errorf("unmarshal workspace: %w", err)
	}
	return &ws, nil
}

func (s *WorkspaceStore) List(ctx context.Context) ([]*workspace.Workspace, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data FROM workspaces ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()

	var all []*workspace.Workspace
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan workspace: %w", err)
		}
		var ws workspace.Workspace
		if err := json.Unmarshal([]byte(data), &ws); err != nil {
			return nil, fmt.Errorf("unmarshal workspace: %w", err)
		}
		all = append(all, &ws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspaces: %w", err)
	}
	return all, nil
}

func (s *WorkspaceStore) Update(ctx context.Context, ws *workspace.Workspace) error {
	data, err := json.Marshal(ws)
	if err != nil {
		return fmt.Errorf("marshal workspace: %w", err)
	}
	updated := ws.UpdatedAt.UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE workspaces SET data=?, state=?, project_id=?, updated_at=? WHERE id=?`,
		string(data), string(ws.State), ws.ProjectID, updated, ws.ID,
	)
	if err != nil {
		return fmt.Errorf("update workspace: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return workspace.ErrNotFound
	}
	return nil
}

func (s *WorkspaceStore) Delete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id=?`, id); err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	return nil
}
