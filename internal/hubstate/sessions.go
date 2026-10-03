package hubstate

import (
	"path/filepath"
	"time"
)

// Proc identifies a process across pid reuse and reboots.
type Proc struct {
	PID       int    `json:"pid"`
	Starttime uint64 `json:"starttime"`
	BootID    string `json:"boot_id"`
}

type Session struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Agent     string    `json:"agent,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	RepoRoot  string    `json:"repo_root,omitempty"`
	Seat      string    `json:"seat,omitempty"`
	Proc      Proc      `json:"proc"`
	Host      string    `json:"host,omitempty"`
	Started   time.Time `json:"started"`
	Heartbeat time.Time `json:"heartbeat"`
}

func (s *Store) sessionPath(id string) string {
	return filepath.Join(s.dir("sessions"), id+".json")
}

// RegisterSession writes the record, preserving Started on re-register.
func (s *Store) RegisterSession(sess Session) error {
	if err := validName(sess.ID); err != nil {
		return err
	}
	unlock, err := s.Lock("sessions")
	if err != nil {
		return err
	}
	defer unlock()
	now := time.Now().UTC()
	var old Session
	if ok, err := readJSON(s.sessionPath(sess.ID), &old); err != nil {
		return err
	} else if ok && !old.Started.IsZero() {
		sess.Started = old.Started
	}
	if sess.Started.IsZero() {
		sess.Started = now
	}
	sess.Heartbeat = now
	return writeJSON(s.sessionPath(sess.ID), sess)
}

// Heartbeat refreshes the record; ErrNotFound if unregistered.
func (s *Store) Heartbeat(id string) error {
	if err := validName(id); err != nil {
		return err
	}
	unlock, err := s.Lock("sessions")
	if err != nil {
		return err
	}
	defer unlock()
	var sess Session
	ok, err := readJSON(s.sessionPath(id), &sess)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	sess.Heartbeat = time.Now().UTC()
	return writeJSON(s.sessionPath(id), sess)
}

// LookupSession returns ErrNotFound when absent. Reads need no lock: writes
// are atomic renames.
func (s *Store) LookupSession(id string) (Session, error) {
	if err := validName(id); err != nil {
		return Session{}, err
	}
	var sess Session
	ok, err := readJSON(s.sessionPath(id), &sess)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrNotFound
	}
	return sess, nil
}

// SessionLive is process liveness of the recorded pid.
func (s *Store) SessionLive(id string) (bool, error) {
	sess, err := s.LookupSession(id)
	if err != nil {
		return false, err
	}
	return Alive(sess.Proc), nil
}
