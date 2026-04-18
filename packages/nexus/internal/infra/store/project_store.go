package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/inizio/nexus/packages/nexus/internal/domain/project"
)

// ProjectStore implements project.Repository using SQLite.
type ProjectStore struct {
	db *sql.DB
}

// NewProjectStore creates a ProjectStore backed by the given DB.
func NewProjectStore(d *DB) *ProjectStore {
	return &ProjectStore{db: d.db}
}

func (s *ProjectStore) Create(ctx context.Context, p *project.Project) error {
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal project: %w", err)
	}
	created := p.CreatedAt.UTC().Format(time.RFC3339Nano)
	updated := p.UpdatedAt.UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO projects(id, name, data, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`,
		p.ID, p.Name, string(data), created, updated,
	)
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

func (s *ProjectStore) Get(ctx context.Context, id string) (*project.Project, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM projects WHERE id = ?`, id,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, project.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	var p project.Project
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return nil, fmt.Errorf("unmarshal project: %w", err)
	}
	return &p, nil
}

func (s *ProjectStore) List(ctx context.Context) ([]*project.Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data FROM projects ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var all []*project.Project
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		var p project.Project
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, fmt.Errorf("unmarshal project: %w", err)
		}
		all = append(all, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return all, nil
}

func (s *ProjectStore) Update(ctx context.Context, p *project.Project) error {
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal project: %w", err)
	}
	updated := p.UpdatedAt.UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET name=?, data=?, updated_at=? WHERE id=?`,
		p.Name, string(data), updated, p.ID,
	)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return project.ErrNotFound
	}
	return nil
}

func (s *ProjectStore) Delete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id); err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}
