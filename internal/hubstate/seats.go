package hubstate

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

var ErrNotFound = errors.New("hubstate: not found")

type Occupant struct {
	SessionID string `json:"session_id"`
	Proc      Proc   `json:"proc"`
}

type Seat struct {
	Name     string    `json:"name"`
	Occupant Occupant  `json:"occupant"`
	Explicit bool      `json:"explicit"`
	Taken    time.Time `json:"taken"`
	// Next is the sub-seat counter; the next sub-seat is "<name>#<Next>".
	// It only grows, so numbers are never reused.
	Next int `json:"next"`
}

type ClaimReq struct {
	Seat      string // repo seat, or repo#name when Explicit
	Explicit  bool
	SessionID string
	Proc      Proc
}

type ClaimResult struct {
	Seat string // seat actually occupied (may be a sub-seat)
	// SubSeat: the requested seat was not occupied; a new sub-seat was allocated.
	SubSeat bool
	// Inherited: an explicit dead-occupant takeover; caller may inherit state.
	Inherited bool
	// Predecessor is the previous occupant of the requested seat, if any
	// (dead on takeover or on a default seat; live when a sub-seat was forced).
	Predecessor     *Occupant
	PredecessorLive bool
}

func (s *Store) seatPath(name string) string {
	return filepath.Join(s.dir("seats"), name+".json")
}

func sameOccupant(o Occupant, r ClaimReq) bool {
	return (r.SessionID != "" && o.SessionID == r.SessionID) || (r.Proc.PID > 0 && o.Proc == r.Proc)
}

// Claim runs the whole occupancy decision under seats.lock (§5 P7).
func (s *Store) Claim(r ClaimReq) (ClaimResult, error) {
	if err := validName(r.Seat); err != nil {
		return ClaimResult{}, err
	}
	unlock, err := s.Lock("seats")
	if err != nil {
		return ClaimResult{}, err
	}
	defer unlock()

	now := time.Now().UTC()
	occ := Occupant{SessionID: r.SessionID, Proc: r.Proc}
	var seat Seat
	ok, err := readJSON(s.seatPath(r.Seat), &seat)
	if err != nil {
		return ClaimResult{}, err
	}
	if !ok {
		seat = Seat{Name: r.Seat, Occupant: occ, Explicit: r.Explicit, Taken: now, Next: 1}
		return ClaimResult{Seat: r.Seat}, writeJSON(s.seatPath(r.Seat), seat)
	}
	if sameOccupant(seat.Occupant, r) {
		seat.Occupant = occ
		return ClaimResult{Seat: r.Seat}, writeJSON(s.seatPath(r.Seat), seat)
	}
	prev := seat.Occupant
	live := Alive(prev.Proc)
	if !live && r.Explicit && seat.Explicit {
		seat.Occupant, seat.Taken = occ, now
		return ClaimResult{Seat: r.Seat, Inherited: true, Predecessor: &prev},
			writeJSON(s.seatPath(r.Seat), seat)
	}
	// Live occupant, or dead occupant on a default seat: new sub-seat, owns nothing.
	n := seat.Next
	if n < 1 {
		n = 1
	}
	seat.Next = n + 1
	if err := writeJSON(s.seatPath(r.Seat), seat); err != nil {
		return ClaimResult{}, err
	}
	sub := Seat{Name: fmt.Sprintf("%s#%d", r.Seat, n), Occupant: occ, Taken: now, Next: 1}
	if err := writeJSON(s.seatPath(sub.Name), sub); err != nil {
		return ClaimResult{}, err
	}
	return ClaimResult{Seat: sub.Name, SubSeat: true, Predecessor: &prev, PredecessorLive: live}, nil
}

// Take is the explicit `seat take`: occupy the seat unconditionally, displacing
// any occupant (live or dead), and mark it explicit. Returns the displaced one.
func (s *Store) Take(name, sessionID string, p Proc) (Occupant, error) {
	if err := validName(name); err != nil {
		return Occupant{}, err
	}
	unlock, err := s.Lock("seats")
	if err != nil {
		return Occupant{}, err
	}
	defer unlock()
	var seat Seat
	ok, err := readJSON(s.seatPath(name), &seat)
	if err != nil {
		return Occupant{}, err
	}
	if !ok {
		return Occupant{}, ErrNotFound
	}
	prev := seat.Occupant
	seat.Occupant = Occupant{SessionID: sessionID, Proc: p}
	seat.Explicit, seat.Taken = true, time.Now().UTC()
	return prev, writeJSON(s.seatPath(name), seat)
}

func (s *Store) GetSeat(name string) (Seat, error) {
	if err := validName(name); err != nil {
		return Seat{}, err
	}
	var seat Seat
	ok, err := readJSON(s.seatPath(name), &seat)
	if err != nil {
		return Seat{}, err
	}
	if !ok {
		return Seat{}, ErrNotFound
	}
	return seat, nil
}

// SeatLive reports whether the seat's occupant is live.
func (s *Store) SeatLive(name string) (bool, error) {
	seat, err := s.GetSeat(name)
	if err != nil {
		return false, err
	}
	return Alive(seat.Occupant.Proc), nil
}
