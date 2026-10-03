package hubclient

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/IniZio/nexus/internal/hubstate"
)

// MailStore is the durable mail and cursor state; *hubstate.Store satisfies it.
type MailStore interface {
	PutMail(m hubstate.Mail) error
	ListMail(seat string) ([]hubstate.Mail, error)
	Cursor(seat string) (string, error)
	AckMail(seat, cursor string, ids []string) error
}

// MailEmitter emits the journal event; a Transport satisfies it.
type MailEmitter interface {
	Emit(ctx context.Context, ev Event) error
}

// MailReader reads journal events; a Transport satisfies it.
type MailReader interface {
	Read(ctx context.Context, f Filter, cursor string) (<-chan Item, error)
}

// Mailbox joins durable mail files with the journal.
type Mailbox struct {
	Store   MailStore
	Emitter MailEmitter
	Reader  MailReader
	// Idle ends a journal read once no item arrives for this long; the
	// journal reader follows until cancelled. Default 300ms.
	Idle time.Duration
	Now  func() time.Time
	// Sandboxes lists sandbox ids owned by a seat; their guest-forwarded
	// events are read regardless of CE_SUBJECT. Optional.
	Sandboxes func(seat string) []string
	// SeatStart is where a seat with no stored cursor starts reading. Optional;
	// zero means from now.
	SeatStart func(seat string) time.Time
}

// Inbox is the merged unread view for a seat.
type Inbox struct {
	Items []Item
	// Cursor is the last journal cursor seen, or the stored one when none.
	Cursor string
	// MailIDs are the mail files included, for AckMail.
	MailIDs []string
}

func (m *Mailbox) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func newMailID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Send writes the mail file, then emits a message event with the same id.
// The file is the durable copy, so an emit failure does not fail the send.
func (m *Mailbox) Send(ctx context.Context, from, to, text string) (string, error) {
	id := newMailID()
	ts := m.now().UnixMilli()
	if err := m.Store.PutMail(hubstate.Mail{ID: id, To: to, From: from, Text: text, TS: ts}); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(MessagePayload{To: to, From: from, Text: text})
	_ = m.Emitter.Emit(ctx, Event{
		ID: id, TS: ts, Topic: SeatTopic(to), Type: TypeMessage,
		Actor: from, Payload: payload,
	})
	return id, nil
}

// Inbox merges unacked mail with journal events after the seat cursor that
// match topics (the seat topic is always included). Items dedupe by id.
func (m *Mailbox) Inbox(ctx context.Context, seat string, topics ...string) (Inbox, error) {
	cursor, err := m.Store.Cursor(seat)
	if err != nil {
		return Inbox{}, err
	}
	mails, err := m.Store.ListMail(seat)
	if err != nil {
		return Inbox{}, err
	}
	res := Inbox{Cursor: cursor}
	seen := map[string]bool{}
	for _, ml := range mails {
		payload, _ := json.Marshal(MessagePayload{To: ml.To, From: ml.From, Text: ml.Text})
		res.Items = append(res.Items, Item{Event: Event{
			ID: ml.ID, TS: ml.TS, Topic: SeatTopic(seat), Type: TypeMessage,
			Actor: ml.From, Payload: payload,
		}})
		res.MailIDs = append(res.MailIDs, ml.ID)
		seen[ml.ID] = true
	}
	if m.Reader == nil {
		return res, nil
	}
	f := Filter{Topics: append([]string{SeatTopic(seat)}, topics...)}
	if m.Sandboxes != nil {
		f.Sandboxes = m.Sandboxes(seat)
	}
	if cursor == "" && m.SeatStart != nil {
		f.Since = m.SeatStart(seat)
	}
	idle := m.Idle
	if idle <= 0 {
		idle = 300 * time.Millisecond
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := m.Reader.Read(rctx, f, cursor)
	if err != nil {
		return res, err
	}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for done := false; !done; {
		select {
		case it, ok := <-ch:
			if !ok {
				done = true
				break
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
			if it.Gap {
				res.Items = append(res.Items, it)
				break
			}
			if it.Event.Cursor != "" {
				res.Cursor = it.Event.Cursor
			}
			if it.Event.ID != "" && seen[it.Event.ID] {
				break
			}
			seen[it.Event.ID] = true
			res.Items = append(res.Items, it)
		case <-timer.C:
			done = true
		case <-ctx.Done():
			return res, ctx.Err()
		}
	}
	return res, nil
}

// Ack persists the cursor and deletes the listed mail files together.
func (m *Mailbox) Ack(seat, cursor string, mailIDs []string) error {
	return m.Store.AckMail(seat, cursor, mailIDs)
}
