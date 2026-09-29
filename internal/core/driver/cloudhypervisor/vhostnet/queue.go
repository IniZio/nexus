//go:build linux

package vhostnet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"
)

const (
	descFNext     = 1
	descFWrite    = 2
	descFIndirect = 4

	availFNoInterrupt = 1

	descSize     = 16
	netHdrSize   = 12
	maxChainLen  = 1 << 20
	notifyBatch  = 64
	maxQueueSize = 32768
)

var (
	errRing       = errors.New("vhostnet: corrupt virtqueue")
	errBadChain   = errors.New("vhostnet: malformed descriptor chain")
	errFrameLarge = errors.New("vhostnet: frame exceeds receive buffer")
)

type seg = []byte

type queue struct {
	idx    int
	kicked chan struct{}

	mu      sync.Mutex
	size    uint16
	haveNum bool
	descU   uint64
	availU  uint64
	usedU   uint64
	haveAdr bool
	base    uint16
	enabled bool
	kick    *os.File
	call    *os.File

	running   bool
	stop      chan struct{}
	done      chan struct{}
	desc      []byte
	avail     []byte
	used      []byte
	mem       *memTable
	lastAvail uint16
	usedIdx   uint16
	scratch   [][]byte
	chain     []seg
}

func newQueue(idx int) *queue {
	return &queue{idx: idx, kicked: make(chan struct{}, 1)}
}

func (q *queue) poke() {
	select {
	case q.kicked <- struct{}{}:
	default:
	}
}

func (q *queue) ready() bool {
	return q.haveNum && q.haveAdr && q.enabled && q.kick != nil
}

// startLocked maps the ring structures against mem and marks the queue live.
func (q *queue) startLocked(mem *memTable) error {
	n := uint64(q.size)
	desc, err := mem.userSlice(q.descU, n*descSize)
	if err != nil {
		return fmt.Errorf("desc table: %w", err)
	}
	avail, err := mem.userSlice(q.availU, 4+2*n)
	if err != nil {
		return fmt.Errorf("avail ring: %w", err)
	}
	used, err := mem.userSlice(q.usedU, 4+8*n)
	if err != nil {
		return fmt.Errorf("used ring: %w", err)
	}
	if uintptr(unsafe.Pointer(&avail[0]))&1 != 0 || uintptr(unsafe.Pointer(&used[0]))&1 != 0 {
		return fmt.Errorf("%w: misaligned ring", errRing)
	}
	q.desc, q.avail, q.used, q.mem = desc, avail, used, mem
	q.lastAvail = q.base
	q.usedIdx = load16(used, 2)
	q.running = true
	q.stop = make(chan struct{})
	q.done = make(chan struct{})
	return nil
}

// stop halts ring processing and waits for the runner to exit. After it
// returns nothing touches guest memory until the next start.
func (q *queue) stopRing() {
	q.mu.Lock()
	if !q.running {
		q.mu.Unlock()
		return
	}
	q.running = false
	close(q.stop)
	done := q.done
	q.base = q.lastAvail
	q.mu.Unlock()
	<-done
}

func (q *queue) notifyLocked() {
	if q.call == nil || load16(q.avail, 0)&availFNoInterrupt != 0 {
		return
	}
	var one [8]byte
	binary.NativeEndian.PutUint64(one[:], 1)
	_, _ = q.call.Write(one[:])
}

func (q *queue) completeLocked(head, length uint32) {
	off := 4 + 8*int(q.usedIdx&(q.size-1))
	binary.LittleEndian.PutUint32(q.used[off:], head)
	binary.LittleEndian.PutUint32(q.used[off+4:], length)
	q.usedIdx++
	store16(q.used, 2, q.usedIdx)
}

// peekLocked returns the head of the next available chain without consuming it.
func (q *queue) peekLocked() (uint16, bool, error) {
	pending := load16(q.avail, 2) - q.lastAvail
	if pending == 0 {
		return 0, false, nil
	}
	if pending > q.size {
		return 0, false, fmt.Errorf("%w: %d buffers pending on a %d entry ring", errRing, pending, q.size)
	}
	head := binary.LittleEndian.Uint16(q.avail[4+2*int(q.lastAvail&(q.size-1)):])
	if head >= q.size {
		return 0, false, fmt.Errorf("%w: head %d beyond ring size %d", errRing, head, q.size)
	}
	return head, true, nil
}

// walkLocked resolves the descriptor chain at head into host slices. Every
// descriptor must have the direction requested by write.
func (q *queue) walkLocked(head uint16, write bool) ([]seg, uint64, error) {
	q.chain = q.chain[:0]
	var total uint64
	i := head
	for steps := 0; ; steps++ {
		if steps >= int(q.size) {
			return nil, 0, fmt.Errorf("%w: descriptor loop", errBadChain)
		}
		if i >= q.size {
			return nil, 0, fmt.Errorf("%w: next %d beyond ring", errBadChain, i)
		}
		d := q.desc[int(i)*descSize:]
		addr := binary.LittleEndian.Uint64(d)
		ln := binary.LittleEndian.Uint32(d[8:])
		fl := binary.LittleEndian.Uint16(d[12:])
		next := binary.LittleEndian.Uint16(d[14:])
		if fl&descFIndirect != 0 {
			return nil, 0, fmt.Errorf("%w: indirect descriptor not negotiated", errBadChain)
		}
		if (fl&descFWrite != 0) != write {
			return nil, 0, fmt.Errorf("%w: wrong descriptor direction", errBadChain)
		}
		total += uint64(ln)
		if total > maxChainLen {
			return nil, 0, fmt.Errorf("%w: chain longer than %d bytes", errBadChain, maxChainLen)
		}
		var err error
		if q.scratch, err = q.mem.spans(q.scratch[:0], addr, uint64(ln)); err != nil {
			return nil, 0, fmt.Errorf("%w: %v", errBadChain, err)
		}
		q.chain = append(q.chain, q.scratch...)
		if fl&descFNext == 0 {
			return q.chain, total, nil
		}
		i = next
	}
}

func gather(segs []seg, total uint64) []byte {
	out := make([]byte, 0, total)
	for _, s := range segs {
		out = append(out, s...)
	}
	return out
}
