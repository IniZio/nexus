package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/inizio/nexus/packages/nexus/internal/domain/spotlight"
)

// ForwardStore implements spotlight.Repository using SQLite.
type ForwardStore struct {
	db *sql.DB
}

// NewForwardStore creates a ForwardStore backed by the given DB.
func NewForwardStore(d *DB) *ForwardStore {
	return &ForwardStore{db: d.db}
}

func (s *ForwardStore) Create(ctx context.Context, fwd *spotlight.Forward) error {
	data, err := json.Marshal(fwd)
	if err != nil {
		return fmt.Errorf("marshal forward: %w", err)
	}
	created := fwd.CreatedAt.UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO spotlight_forwards(id, workspace_id, data, state, created_at) VALUES(?, ?, ?, ?, ?)`,
		fwd.ID, fwd.WorkspaceID, string(data), string(fwd.State), created,
	)
	if err != nil {
		return fmt.Errorf("insert forward: %w", err)
	}
	return nil
}

func (s *ForwardStore) Get(ctx context.Context, id string) (*spotlight.Forward, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM spotlight_forwards WHERE id = ?`, id,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, spotlight.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get forward: %w", err)
	}
	var fwd spotlight.Forward
	if err := json.Unmarshal([]byte(data), &fwd); err != nil {
		return nil, fmt.Errorf("unmarshal forward: %w", err)
	}
	return &fwd, nil
}

func (s *ForwardStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]*spotlight.Forward, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data FROM spotlight_forwards WHERE workspace_id = ? ORDER BY created_at ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list forwards: %w", err)
	}
	defer rows.Close()

	var all []*spotlight.Forward
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan forward: %w", err)
		}
		var fwd spotlight.Forward
		if err := json.Unmarshal([]byte(data), &fwd); err != nil {
			return nil, fmt.Errorf("unmarshal forward: %w", err)
		}
		all = append(all, &fwd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate forwards: %w", err)
	}
	return all, nil
}

func (s *ForwardStore) Update(ctx context.Context, fwd *spotlight.Forward) error {
	data, err := json.Marshal(fwd)
	if err != nil {
		return fmt.Errorf("marshal forward: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE spotlight_forwards SET data=?, state=?, workspace_id=? WHERE id=?`,
		string(data), string(fwd.State), fwd.WorkspaceID, fwd.ID,
	)
	if err != nil {
		return fmt.Errorf("update forward: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return spotlight.ErrNotFound
	}
	return nil
}

func (s *ForwardStore) Delete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM spotlight_forwards WHERE id=?`, id); err != nil {
		return fmt.Errorf("delete forward: %w", err)
	}
	return nil
}
