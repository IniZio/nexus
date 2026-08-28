package mitm

import (
	"strings"
	"sync"
)

// MutableAllowSet is a concurrency-safe set of allowed hostnames.
// Hostnames are stored lowercase-normalised (matching the frozen map in New).
type MutableAllowSet struct {
	mu  sync.RWMutex
	set map[string]struct{}
}

// NewMutableAllowSet returns a MutableAllowSet pre-populated with the given
// hostnames. Each host is lowercased on insertion, mirroring the normalisation
// applied to Config.AllowedHosts in New.
func NewMutableAllowSet(initial ...string) *MutableAllowSet {
	s := &MutableAllowSet{set: make(map[string]struct{}, len(initial))}
	for _, h := range initial {
		s.set[strings.ToLower(h)] = struct{}{}
	}
	return s
}

// Add inserts host into the set. It is safe for concurrent use.
func (s *MutableAllowSet) Add(host string) {
	h := strings.ToLower(host)
	s.mu.Lock()
	s.set[h] = struct{}{}
	s.mu.Unlock()
}

// Has reports whether host is in the set. It is safe for concurrent use.
func (s *MutableAllowSet) Has(host string) bool {
	h := strings.ToLower(host)
	s.mu.RLock()
	_, ok := s.set[h]
	s.mu.RUnlock()
	return ok
}
