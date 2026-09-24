package main

import (
	"encoding/binary"
	"sync"
)

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

// maxTaggedPayload is the maximum data length allowed in a tagged ring record.
// Matches wire.MaxDataPayload; larger values indicate a corrupt header.
const maxTaggedPayload = 64 * 1024

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

// simulateEvictLocked returns the oldest offset that would result after
// evicting enough whole records to make room for n additional bytes.
// Does NOT mutate the ring. Returns the current oldest when no eviction is needed.
func (r *Ring) simulateEvictLocked(n int) uint64 {
	if r.used+n <= r.cap {
		return r.oldestLocked()
	}
	oldest := r.oldestLocked()
	head := r.head
	used := r.used
	evicted := 0
	for used+n > r.cap {
		if used < 5 {
			// Ring corrupt / undersized; treat as full eviction.
			return r.tot
		}
		var lb [4]byte
		for i := 0; i < 4; i++ {
			lb[i] = r.buf[(head+1+i)%r.cap]
		}
		recTotalLen := 5 + int(binary.BigEndian.Uint32(lb[:]))
		if recTotalLen > used {
			return r.tot
		}
		head = (head + recTotalLen) % r.cap
		used -= recTotalLen
		evicted += recTotalLen
	}
	return oldest + uint64(evicted)
}

// WriteRecord appends a tagged record [tag(1)][dataLen(4 BE)][data] to the ring.
// Eviction is record-aligned so OldestOffset() always lands on a record boundary.
func (r *Ring) WriteRecord(tag byte, data []byte) {
	if len(data) == 0 {
		return
	}
	var hdr [5]byte
	hdr[0] = tag
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(data)))
	n := 5 + len(data)

	r.mu.Lock()
	for !r.done && len(r.readers) > 0 {
		newOldest := r.simulateEvictLocked(n)
		if newOldest == r.oldestLocked() {
			// No eviction needed; ring has room.
			break
		}
		if r.minCursorLocked() >= newOldest {
			// All attached readers have advanced past the eviction point.
			break
		}
		r.cond.Wait()
	}
	if r.done {
		r.mu.Unlock()
		return
	}

	for r.used+n > r.cap {
		if r.used < 5 {
			r.head = 0
			r.used = 0
			break
		}
		var lb [4]byte
		for i := 0; i < 4; i++ {
			lb[i] = r.buf[(r.head+1+i)%r.cap]
		}
		recTotalLen := 5 + int(binary.BigEndian.Uint32(lb[:]))
		if recTotalLen > r.used {
			r.head = (r.head + r.used) % r.cap
			r.used = 0
			break
		}
		r.head = (r.head + recTotalLen) % r.cap
		r.used -= recTotalLen
	}

	writeToRingLocked(r, hdr[:])
	writeToRingLocked(r, data)
	r.tot += uint64(n)
	r.cond.Broadcast()
	r.mu.Unlock()
}

func writeToRingLocked(r *Ring, p []byte) {
	for len(p) > 0 {
		tail := (r.head + r.used) % r.cap
		toEnd := r.cap - tail
		chunk := len(p)
		if chunk > toEnd {
			chunk = toEnd
		}
		copy(r.buf[tail:tail+chunk], p[:chunk])
		p = p[chunk:]
		r.used += chunk
	}
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

// SnapToRecordBoundary returns the record-start offset for the record that
// contains from. If from is already at a boundary it is returned unchanged.
// If from <= oldest, returns oldest. If from >= tot, returns tot.
// A corrupt header (length > maxTaggedPayload) causes a fallback to oldest.
func (r *Ring) SnapToRecordBoundary(from uint64) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	oldest := r.oldestLocked()
	if from <= oldest {
		return oldest
	}
	if from >= r.tot {
		return r.tot
	}
	pos := oldest
	for pos < from {
		avail := int(r.tot - pos)
		if avail < 5 {
			return r.tot
		}
		base := r.head + int(pos-oldest)
		var lb [4]byte
		for i := 0; i < 4; i++ {
			lb[i] = r.buf[(base+1+i)%r.cap]
		}
		recDataLen := int(binary.BigEndian.Uint32(lb[:]))
		if recDataLen > maxTaggedPayload || 5+recDataLen > avail {
			return oldest
		}
		next := pos + uint64(5+recDataLen)
		if next > from {
			return pos
		}
		pos = next
	}
	return pos
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
