package hubstate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Mail is one unacked direct message, kept until acked.
type Mail struct {
	ID   string `json:"id"`
	To   string `json:"to"`
	From string `json:"from"`
	Text string `json:"text"`
	TS   int64  `json:"ts"`
}

const (
	cursorFile       = "cursor"
	urgentCursorFile = "cursor-urgent"
)

func (s *Store) mailDir(seat string) string { return filepath.Join(s.dir("mail"), seat) }

// PutMail writes the mail file under mail/<to>/ atomically.
func (s *Store) PutMail(m Mail) error {
	if err := validName(m.To); err != nil {
		return err
	}
	if err := validName(m.ID); err != nil {
		return err
	}
	unlock, err := s.Lock("mail")
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.MkdirAll(s.mailDir(m.To), 0o700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.mailDir(m.To), m.ID+".json"), m)
}

// ListMail returns the seat's unacked mail, oldest first.
func (s *Store) ListMail(seat string) ([]Mail, error) {
	if err := validName(seat); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(s.mailDir(seat))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Mail
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		var m Mail
		if ok, err := readJSON(filepath.Join(s.mailDir(seat), e.Name()), &m); err != nil {
			return nil, err
		} else if ok {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TS != out[j].TS {
			return out[i].TS < out[j].TS
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Cursor returns the seat's journal cursor, "" when none.
func (s *Store) Cursor(seat string) (string, error) { return s.readCursor(seat, cursorFile) }

// UrgentCursor is the separate cursor advanced by urgent-only acks.
func (s *Store) UrgentCursor(seat string) (string, error) {
	return s.readCursor(seat, urgentCursorFile)
}

func (s *Store) readCursor(seat, file string) (string, error) {
	if err := validName(seat); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(s.mailDir(seat), file))
	if os.IsNotExist(err) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// AckMail persists cursor (when non-empty) and deletes the listed mail ids
// under one lock hold. A nil ids deletes nothing.
func (s *Store) AckMail(seat, cursor string, ids []string) error {
	return s.ack(seat, cursorFile, cursor, ids)
}

// AckMailUrgent is AckMail against the urgent cursor.
func (s *Store) AckMailUrgent(seat, cursor string, ids []string) error {
	return s.ack(seat, urgentCursorFile, cursor, ids)
}

func (s *Store) ack(seat, file, cursor string, ids []string) error {
	if err := validName(seat); err != nil {
		return err
	}
	for _, id := range ids {
		if err := validName(id); err != nil {
			return err
		}
	}
	unlock, err := s.Lock("mail")
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.MkdirAll(s.mailDir(seat), 0o700); err != nil {
		return err
	}
	if cursor != "" {
		if err := WriteFileAtomic(filepath.Join(s.mailDir(seat), file), []byte(cursor+"\n"), 0o600); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if err := os.Remove(filepath.Join(s.mailDir(seat), id+".json")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
