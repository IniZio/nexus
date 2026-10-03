package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/IniZio/nexus/internal/hubclient"
)

const eventCols = `seq,ts,topic,type,actor,subject,payload_json`

type scanner interface{ Scan(...any) error }

func scanEvent(s scanner) (hubclient.Event, error) {
	var e hubclient.Event
	var p string
	if err := s.Scan(&e.Seq, &e.TS, &e.Topic, &e.Type, &e.Actor, &e.Subject, &p); err != nil {
		return e, err
	}
	e.Payload = []byte(p)
	return e, nil
}

func (h *Hub) query(ctx context.Context, q string, args ...any) ([]hubclient.Event, error) {
	rows, err := h.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("hub: query: %w", err)
	}
	defer rows.Close()
	var out []hubclient.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReadAfter returns up to limit events on topic with seq > after, ascending.
// limit <= 0 means no limit. An empty topic matches every topic.
func (h *Hub) ReadAfter(ctx context.Context, topic string, after int64, limit int) ([]hubclient.Event, error) {
	q := `SELECT ` + eventCols + ` FROM events WHERE seq > ?`
	args := []any{after}
	if topic != "" {
		q += ` AND topic = ?`
		args = append(args, topic)
	}
	q += ` ORDER BY seq ASC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return h.query(ctx, q, args...)
}

// LastBySubject returns the newest event for subject; ok is false if none.
func (h *Hub) LastBySubject(ctx context.Context, subject string) (hubclient.Event, bool, error) {
	e, err := scanEvent(h.db.QueryRowContext(ctx,
		`SELECT `+eventCols+` FROM events WHERE subject = ? ORDER BY seq DESC LIMIT 1`, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	return e, err == nil, err
}

// LastAll returns the newest event of each non-empty subject, ascending by seq.
func (h *Hub) LastAll(ctx context.Context) ([]hubclient.Event, error) {
	return h.query(ctx, `SELECT `+eventCols+` FROM events
		WHERE seq IN (SELECT MAX(seq) FROM events WHERE subject <> '' GROUP BY subject)
		ORDER BY seq ASC`)
}
