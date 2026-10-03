package hub

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/IniZio/nexus/internal/hubclient"
)

const (
	// CoalesceWindow bounds how far apart same type+subject events may be to collapse.
	CoalesceWindow = 30 * time.Second
	// RetainAge and RetainRows are the default prune limits.
	RetainAge  = 7 * 24 * time.Hour
	RetainRows = 100000
)

// Deliver returns the watch lines for topic after cursor: at most one leading
// gap line when the cursor is below the oldest retained seq, then events with
// same type+subject runs within CoalesceWindow collapsed into the newest one
// carrying a count. Events with an empty subject are never coalesced.
// limit bounds the raw events read (<= 0 means no limit). Acking the last
// returned seq covers every collapsed event.
func (h *Hub) Deliver(ctx context.Context, topic string, cursor int64, limit int) ([]hubclient.WatchLine, error) {
	var out []hubclient.WatchLine
	var oldest sql.NullInt64
	if err := h.db.QueryRowContext(ctx, `SELECT MIN(seq) FROM events`).Scan(&oldest); err != nil {
		return nil, fmt.Errorf("hub: oldest seq: %w", err)
	}
	if oldest.Valid && cursor+1 < oldest.Int64 {
		out = append(out, hubclient.WatchLine{Kind: hubclient.KindGap, From: cursor + 1, To: oldest.Int64 - 1})
	}
	events, err := h.ReadAfter(ctx, topic, cursor, limit)
	if err != nil {
		return nil, err
	}
	return append(out, coalesce(events)...), nil
}

type group struct {
	line    hubclient.WatchLine
	firstTS int64
	dead    bool
}

func coalesce(events []hubclient.Event) []hubclient.WatchLine {
	var items []*group
	open := map[[2]string]*group{}
	for _, e := range events {
		g := &group{line: hubclient.WatchLine{Kind: hubclient.KindEvent, Event: e, Count: 1}, firstTS: e.TS}
		if e.Subject != "" {
			key := [2]string{e.Type, e.Subject}
			if prev := open[key]; prev != nil && e.TS-prev.firstTS <= CoalesceWindow.Milliseconds() {
				prev.dead = true
				g.line.Count = prev.line.Count + 1
				g.firstTS = prev.firstTS
			}
			open[key] = g
		}
		items = append(items, g)
	}
	out := make([]hubclient.WatchLine, 0, len(items))
	for _, g := range items {
		if !g.dead {
			out = append(out, g.line)
		}
	}
	return out
}

// Prune deletes events older than maxAge, then the oldest events beyond
// maxRows. A non-positive limit disables that limit. It returns rows deleted.
func (h *Hub) Prune(ctx context.Context, maxAge time.Duration, maxRows int) (int64, error) {
	var total int64
	if maxAge > 0 {
		res, err := h.db.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, h.now().Add(-maxAge).UnixMilli())
		if err != nil {
			return total, fmt.Errorf("hub: prune age: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	if maxRows > 0 {
		res, err := h.db.ExecContext(ctx,
			`DELETE FROM events WHERE seq <= (SELECT seq FROM events ORDER BY seq DESC LIMIT 1 OFFSET ?)`, maxRows)
		if err != nil {
			return total, fmt.Errorf("hub: prune rows: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
