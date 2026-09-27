package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/IniZio/nexus/internal/controller"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS tasks (
	thread_ref        TEXT    NOT NULL PRIMARY KEY,
	project           TEXT    NOT NULL DEFAULT '',
	owner             TEXT    NOT NULL DEFAULT '',
	last_author       TEXT    NOT NULL DEFAULT '',
	sandbox_id        TEXT    NOT NULL DEFAULT '',
	herdr_agent       TEXT    NOT NULL DEFAULT '',
	agent_session_id  TEXT    NOT NULL DEFAULT '',
	status            TEXT    NOT NULL DEFAULT 'starting',
	turn_id           TEXT    NOT NULL DEFAULT '',
	state_change_seq  INTEGER NOT NULL DEFAULT 0,
	created_at        INTEGER NOT NULL DEFAULT 0,
	last_activity_at  INTEGER NOT NULL DEFAULT 0,
	preview_slot      TEXT    NOT NULL DEFAULT ''
);
`

// Store is a SQLite-backed controller.TaskStore.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (or creates) the SQLite database at path and applies migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: schema: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Get(ctx context.Context, ref controller.ThreadRef) (controller.Task, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT thread_ref, project, owner, last_author, sandbox_id, herdr_agent,
		       agent_session_id, status, turn_id, state_change_seq,
		       created_at, last_activity_at, preview_slot
		FROM tasks WHERE thread_ref = ?`, string(ref))
	return scanTask(row)
}

func (s *Store) Upsert(ctx context.Context, t controller.Task) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks
			(thread_ref, project, owner, last_author, sandbox_id, herdr_agent,
			 agent_session_id, status, turn_id, state_change_seq,
			 created_at, last_activity_at, preview_slot)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(thread_ref) DO UPDATE SET
			project          = excluded.project,
			owner            = excluded.owner,
			last_author      = excluded.last_author,
			sandbox_id       = excluded.sandbox_id,
			herdr_agent      = excluded.herdr_agent,
			agent_session_id = excluded.agent_session_id,
			status           = excluded.status,
			turn_id          = excluded.turn_id,
			state_change_seq = excluded.state_change_seq,
			created_at       = excluded.created_at,
			last_activity_at = excluded.last_activity_at,
			preview_slot     = excluded.preview_slot`,
		string(t.ThreadRef),
		t.Project, t.Owner, t.LastAuthor, t.SandboxID, t.HerdrAgent,
		t.AgentSessionID, string(t.Status), t.TurnID, t.StateChangeSeq,
		t.CreatedAt.UTC().UnixMilli(), t.LastActivityAt.UTC().UnixMilli(),
		t.PreviewSlot,
	)
	return err
}

func (s *Store) Transition(ctx context.Context, ref controller.ThreadRef, from, to controller.Status, seq uint64) error {
	if !controller.ValidTransition(from, to) {
		return controller.ErrInvalidTransition
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = ?, state_change_seq = ? WHERE thread_ref = ? AND status = ?`,
		string(to), seq, string(ref), string(from),
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		_, err2 := s.Get(ctx, ref)
		if errors.Is(err2, controller.ErrNotFound) {
			return controller.ErrNotFound
		}
		return controller.ErrConflict
	}
	return nil
}

func (s *Store) ListIdle(ctx context.Context, before time.Time) ([]controller.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT thread_ref, project, owner, last_author, sandbox_id, herdr_agent,
		       agent_session_id, status, turn_id, state_change_seq,
		       created_at, last_activity_at, preview_slot
		FROM tasks
		WHERE status IN ('idle', 'waiting_on_user', 'paused')
		  AND last_activity_at < ?`,
		before.UTC().UnixMilli(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []controller.Task
	for rows.Next() {
		t, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TouchActivity(ctx context.Context, ref controller.ThreadRef, author string) error {
	now := s.now().UTC().UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET last_author = ?, last_activity_at = ? WHERE thread_ref = ?`,
		author, now, string(ref),
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return controller.ErrNotFound
	}
	return nil
}

func (s *Store) ResetStuck(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'failed' WHERE status IN ('starting', 'working')`)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row *sql.Row) (controller.Task, error) {
	var t controller.Task
	var status string
	var createdAtMs, lastActivityAtMs int64
	err := row.Scan(
		(*string)(&t.ThreadRef),
		&t.Project, &t.Owner, &t.LastAuthor, &t.SandboxID, &t.HerdrAgent,
		&t.AgentSessionID, &status, &t.TurnID, &t.StateChangeSeq,
		&createdAtMs, &lastActivityAtMs, &t.PreviewSlot,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return controller.Task{}, controller.ErrNotFound
	}
	if err != nil {
		return controller.Task{}, err
	}
	t.Status = controller.Status(status)
	t.CreatedAt = time.UnixMilli(createdAtMs).UTC()
	t.LastActivityAt = time.UnixMilli(lastActivityAtMs).UTC()
	return t, nil
}

func scanRow(rows *sql.Rows) (controller.Task, error) {
	var t controller.Task
	var status string
	var createdAtMs, lastActivityAtMs int64
	err := rows.Scan(
		(*string)(&t.ThreadRef),
		&t.Project, &t.Owner, &t.LastAuthor, &t.SandboxID, &t.HerdrAgent,
		&t.AgentSessionID, &status, &t.TurnID, &t.StateChangeSeq,
		&createdAtMs, &lastActivityAtMs, &t.PreviewSlot,
	)
	if err != nil {
		return controller.Task{}, err
	}
	t.Status = controller.Status(status)
	t.CreatedAt = time.UnixMilli(createdAtMs).UTC()
	t.LastActivityAt = time.UnixMilli(lastActivityAtMs).UTC()
	return t, nil
}

var _ controller.TaskStore = (*Store)(nil)
