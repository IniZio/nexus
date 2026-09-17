package main

import "sync"

// Ring is a bounded in-RAM ring buffer with a monotonic byte offset.
// It is the guest-authoritative output store for a session's combined
// stdout+stderr stream. Multiple goroutines may call WaitNext concurrently
// (fan-out readers, one per data-plane connection).
//
// The monotonic total-bytes counter lets hosts replay from an exact byte
// offset across reconnects: the host tracks its cursor, the guest is
// authoritative over the buffer contents.
type Ring struct {
	mu   sync.Mutex
	cond *sync.Cond

	buf  []byte // circular storage
	cap  int
	head int    // index of the oldest byte
	used int    // number of valid bytes currently held
	tot  uint64 // monotonic count: total bytes ever Written

	done bool // no more writes; wake all blocked readers
}

const defaultRingCap = 16 * 1024 * 1024 // 16 MiB per session

func newRing(capacity int) *Ring {
	r := &Ring{buf: make([]byte, capacity), cap: capacity}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// Write appends p to the ring, evicting the oldest bytes when full.
func (r *Ring) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	r.mu.Lock()
	for _, b := range p {
		tail := (r.head + r.used) % r.cap
		r.buf[tail] = b
		if r.used < r.cap {
			r.used++
		} else {
			// Overwrite oldest byte
			r.head = (r.head + 1) % r.cap
		}
	}
	r.tot += uint64(len(p))
	r.cond.Broadcast()
	r.mu.Unlock()
}

// Close marks the ring done (no more writes). All blocked WaitNext calls wake.
func (r *Ring) Close() {
	r.mu.Lock()
	r.done = true
	r.cond.Broadcast()
	r.mu.Unlock()
}

// Total returns the current monotonic byte count.
func (r *Ring) Total() uint64 {
	r.mu.Lock()
	t := r.tot
	r.mu.Unlock()
	return t
}

// IsDone reports whether Close has been called.
func (r *Ring) IsDone() bool {
	r.mu.Lock()
	d := r.done
	r.mu.Unlock()
	return d
}

const ringChunk = 64 * 1024 // 64 KiB – matches wire.MaxDataPayload

// WaitNext blocks until data is available past from, or the ring is closed.
// Returns up to ringChunk bytes, the advanced offset, and whether the ring is
// done. Callers loop, passing newOff each time, until done && len(data)==0.
func (r *Ring) WaitNext(from uint64) (data []byte, newOff uint64, done bool) {
	r.mu.Lock()
	for from == r.tot && !r.done {
		r.cond.Wait()
	}
	data, newOff = r.snapshotLocked(from)
	done = r.done
	r.mu.Unlock()
	return
}

// snapshotLocked copies up to ringChunk bytes starting at from.
// Must be called with r.mu held.
func (r *Ring) snapshotLocked(from uint64) ([]byte, uint64) {
	oldest := r.oldestLocked()
	if from < oldest {
		from = oldest
	}
	if from >= r.tot {
		return nil, r.tot
	}
	n := int(r.tot - from)
	if n > ringChunk {
		n = ringChunk
	}
	start := (r.head + int(from-oldest)) % r.cap
	out := make([]byte, n)
	for i := range out {
		out[i] = r.buf[(start+i)%r.cap]
	}
	return out, from + uint64(n)
}

func (r *Ring) oldestLocked() uint64 {
	if uint64(r.used) >= r.tot {
		return 0
	}
	return r.tot - uint64(r.used)
}
