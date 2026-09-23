package main

import "sync"

type Ring struct {
	mu   sync.Mutex
	cond *sync.Cond

	buf  []byte
	cap  int
	head int
	used int
	tot  uint64

	done bool

	readers map[uint64]uint64
	nextRID uint64
}

const defaultRingCap = 16 * 1024 * 1024

func newRing(capacity int) *Ring {
	r := &Ring{
		buf:     make([]byte, capacity),
		cap:     capacity,
		readers: make(map[uint64]uint64),
	}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *Ring) AddReader(from uint64) uint64 {
	r.mu.Lock()
	id := r.nextRID
	r.nextRID++
	r.readers[id] = from
	r.mu.Unlock()
	return id
}

func (r *Ring) RemoveReader(id uint64) {
	r.mu.Lock()
	delete(r.readers, id)
	r.cond.Broadcast()
	r.mu.Unlock()
}

func (r *Ring) minCursorLocked() uint64 {
	min := r.tot
	for _, c := range r.readers {
		if c < min {
			min = c
		}
	}
	return min
}

// Write appends p. Blocks when attached readers would lose bytes; evicts freely with no readers.
func (r *Ring) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	r.mu.Lock()
	for !r.done && len(r.readers) > 0 {
		need := r.tot + uint64(len(p))
		if need <= uint64(r.cap) {
			break
		}
		if r.minCursorLocked() >= need-uint64(r.cap) {
			break
		}
		r.cond.Wait()
	}
	if r.done {
		r.mu.Unlock()
		return
	}
	totalLen := uint64(len(p))
	for len(p) > 0 {
		tail := (r.head + r.used) % r.cap
		toEnd := r.cap - tail
		chunk := len(p)
		if chunk > toEnd {
			chunk = toEnd
		}
		copy(r.buf[tail:tail+chunk], p[:chunk])
		p = p[chunk:]
		if r.used < r.cap {
			r.used += chunk
		} else {
			r.head = (r.head + chunk) % r.cap
		}
	}
	r.tot += totalLen
	r.cond.Broadcast()
	r.mu.Unlock()
}

func (r *Ring) Close() {
	r.mu.Lock()
	r.done = true
	r.cond.Broadcast()
	r.mu.Unlock()
}

func (r *Ring) Total() uint64 {
	r.mu.Lock()
	t := r.tot
	r.mu.Unlock()
	return t
}

func (r *Ring) IsDone() bool {
	r.mu.Lock()
	d := r.done
	r.mu.Unlock()
	return d
}

func (r *Ring) OldestOffset() uint64 {
	r.mu.Lock()
	o := r.oldestLocked()
	r.mu.Unlock()
	return o
}

const ringChunk = 64 * 1024

// WaitNext blocks until data past from is available or the ring is closed.
func (r *Ring) WaitNext(from uint64) (data []byte, newOff uint64, done bool) {
	r.mu.Lock()
	for from == r.tot && !r.done {
		r.cond.Wait()
	}
	data, newOff, _ = r.snapshotLocked(from)
	done = r.done
	r.mu.Unlock()
	return
}

// WaitNextCursored advances the reader cursor and reports overrun when bytes were evicted.
func (r *Ring) WaitNextCursored(id uint64, from uint64) (data []byte, newOff uint64, done bool, overrun bool) {
	r.mu.Lock()
	for from == r.tot && !r.done {
		r.cond.Wait()
	}
	data, newOff, overrun = r.snapshotLocked(from)
	if _, ok := r.readers[id]; ok {
		r.readers[id] = newOff
		r.cond.Broadcast()
	}
	done = r.done
	r.mu.Unlock()
	return
}

func (r *Ring) snapshotLocked(from uint64) (data []byte, newOff uint64, overrun bool) {
	oldest := r.oldestLocked()
	if from < oldest {
		overrun = true
		from = oldest
	}
	if from >= r.tot {
		return nil, r.tot, overrun
	}
	n := int(r.tot - from)
	if n > ringChunk {
		n = ringChunk
	}
	startIdx := (r.head + int(from-oldest)) % r.cap
	out := make([]byte, n)
	toEnd := r.cap - startIdx
	if toEnd >= n {
		copy(out, r.buf[startIdx:startIdx+n])
	} else {
		copy(out, r.buf[startIdx:r.cap])
		copy(out[toEnd:], r.buf[:n-toEnd])
	}
	return out, from + uint64(n), overrun
}

func (r *Ring) oldestLocked() uint64 {
	if uint64(r.used) >= r.tot {
		return 0
	}
	return r.tot - uint64(r.used)
}
