// Package hub is the SQLite-backed session-hub store. It is linked only by
// cmd/nexus-hub, never by the core nexus binary (decision D7).
package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/IniZio/nexus/internal/hubclient"
)

// Hub is an open hub database.
type Hub struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) the hub database at path in WAL mode.
// It refuses a database directory on NFS, 9p, or FUSE (including virtiofs).
func Open(path string) (*Hub, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("hub: mkdir %q: %w", dir, err)
	}
	if err := checkFS(dir); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("hub: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, p := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL"} {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("hub: %s: %w", p, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("hub: schema: %w", err)
	}
	return &Hub{db: db, now: time.Now}, nil
}

// Close closes the database.
func (h *Hub) Close() error { return h.db.Close() }

// Append stores e (Seq and TS are assigned, TS only when zero) and returns its seq.
func (h *Hub) Append(ctx context.Context, e hubclient.Event) (int64, error) {
	if e.Topic == "" || e.Type == "" {
		return 0, errors.New("hub: event needs topic and type")
	}
	if e.Actor == "" {
		e.Actor = hubclient.ActorAnonymous
	}
	if e.TS == 0 {
		e.TS = h.now().UnixMilli()
	}
	payload := e.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	res, err := h.db.ExecContext(ctx,
		`INSERT INTO events(ts,topic,type,actor,subject,payload_json) VALUES (?,?,?,?,?,?)`,
		e.TS, e.Topic, e.Type, e.Actor, e.Subject, string(payload))
	if err != nil {
		return 0, fmt.Errorf("hub: append: %w", err)
	}
	return res.LastInsertId()
}

// Ack records seat's read position for topic (monotonic: never moves back).
func (h *Hub) Ack(ctx context.Context, seat, topic string, seq int64) error {
	_, err := h.db.ExecContext(ctx,
		`INSERT INTO cursors(seat,topic,seq) VALUES (?,?,?)
		 ON CONFLICT(seat,topic) DO UPDATE SET seq = MAX(seq, excluded.seq)`,
		seat, topic, seq)
	if err != nil {
		return fmt.Errorf("hub: ack: %w", err)
	}
	return nil
}

// Cursor returns seat's position for topic (0 if none).
func (h *Hub) Cursor(ctx context.Context, seat, topic string) (int64, error) {
	var seq int64
	err := h.db.QueryRowContext(ctx, `SELECT seq FROM cursors WHERE seat=? AND topic=?`, seat, topic).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}
