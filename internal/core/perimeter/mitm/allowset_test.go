package mitm

import (
	"sync"
	"testing"
)

// T0-AC1: MutableAllowSet concurrent Add/Has is race-clean.
func TestMutableAllowSet_AddHas(t *testing.T) {
	s := NewMutableAllowSet("api.github.com", "NPM.ORG")

	// Pre-populated hosts are present (case-folded).
	if !s.Has("api.github.com") {
		t.Fatal("expected api.github.com to be present")
	}
	if !s.Has("npm.org") {
		t.Fatal("expected npm.org (lowercased) to be present")
	}
	if !s.Has("NPM.ORG") {
		t.Fatal("expected NPM.ORG (upper) to match via normalisation")
	}
	if s.Has("example.com") {
		t.Fatal("expected example.com to be absent")
	}

	// Add then Has.
	s.Add("example.com")
	if !s.Has("example.com") {
		t.Fatal("expected example.com to be present after Add")
	}
}

func TestMutableAllowSet_Concurrent(t *testing.T) {
	s := NewMutableAllowSet()
	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			host := "host" + string(rune('a'+i%26)) + ".example.com"
			s.Add(host)
			_ = s.Has(host)
		}(i)
	}
	wg.Wait()
}
