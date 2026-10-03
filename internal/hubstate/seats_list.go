package hubstate

import (
	"os"
	"sort"
	"strings"
)

// ListSeats returns every seat record, sorted by name.
func (s *Store) ListSeats() ([]Seat, error) {
	ents, err := os.ReadDir(s.dir("seats"))
	if err != nil {
		return nil, err
	}
	var out []Seat
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, ".tmp-") {
			continue
		}
		var seat Seat
		if ok, err := readJSON(s.seatPath(strings.TrimSuffix(n, ".json")), &seat); err != nil {
			return nil, err
		} else if ok {
			out = append(out, seat)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
