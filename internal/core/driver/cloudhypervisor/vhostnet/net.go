//go:build linux

package vhostnet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

const (
	rxQueue = 0
	txQueue = 1

	defaultBacklog = 64
	maxFDsPerMsg   = maxRegions
)

// Config tunes a Device. The zero value is valid.
type Config struct {
	// TXBacklog bounds frames the guest has sent that the reader has not yet consumed.
	TXBacklog int
}

// Stats are cumulative counters, safe to read while the device runs.
type Stats struct {
	TXFrames    uint64
	RXFrames    uint64
	Malformed   uint64
	RXTooLarge  uint64
	MemRestarts uint64
}

// Device is a vhost-user-net backend on one master connection. Read yields one
// Ethernet frame the guest transmitted; Write delivers one frame to the guest.
type Device struct {
	conn *net.UnixConn
	txq  chan []byte

	done    chan struct{}
	doneErr atomic.Pointer[error]
	once    sync.Once
	wg      sync.WaitGroup
	rxMu    sync.Mutex
	rxWake  chan struct{}

	q [numQueues]*queue

	features   uint64
	protoFeats uint64
	mem        *memTable

	txFrames, rxFrames, malformed, rxTooLarge, memRestarts atomic.Uint64
}

var _ io.ReadWriteCloser = (*Device)(nil)

// Serve runs the vhost-user slave protocol on conn until the master disconnects
// or Close is called. It takes ownership of conn.
func Serve(conn *net.UnixConn, cfg Config) (*Device, error) {
	if !hostIsLittleEndian() {
		return nil, errors.New("vhostnet: big-endian hosts unsupported")
	}
	backlog := cfg.TXBacklog
	if backlog <= 0 {
		backlog = defaultBacklog
	}
	d := &Device{
		conn:   conn,
		txq:    make(chan []byte, backlog),
		done:   make(chan struct{}),
		rxWake: make(chan struct{}, 1),
		mem:    &memTable{},
	}
	for i := range d.q {
		d.q[i] = newQueue(i)
	}
	d.wg.Add(1)
	go d.control()
	return d, nil
}

func (d *Device) Stats() Stats {
	return Stats{d.txFrames.Load(), d.rxFrames.Load(), d.malformed.Load(), d.rxTooLarge.Load(), d.memRestarts.Load()}
}

// Read returns one guest-transmitted frame. It returns io.EOF once the master
// has disconnected and pending frames are drained.
func (d *Device) Read(p []byte) (int, error) {
	select {
	case f := <-d.txq:
		return deliver(p, f)
	default:
	}
	select {
	case f := <-d.txq:
		return deliver(p, f)
	case <-d.done:
		select {
		case f := <-d.txq:
			return deliver(p, f)
		default:
			return 0, d.finalErr()
		}
	}
}

func deliver(p, f []byte) (int, error) {
	if len(p) < len(f) {
		return copy(p, f), io.ErrShortBuffer
	}
	return copy(p, f), nil
}

func (d *Device) finalErr() error {
	if e := d.doneErr.Load(); e != nil && *e != nil {
		return *e
	}
	return io.EOF
}

// Write delivers one frame to the guest, blocking until the guest has posted a
// receive buffer.
func (d *Device) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	d.rxMu.Lock()
	defer d.rxMu.Unlock()
	q := d.q[rxQueue]
	for {
		select {
		case <-d.done:
			return 0, io.ErrClosedPipe
		default:
		}
		ok, err := q.pushRX(d, p)
		if err != nil {
			return 0, err
		}
		if ok {
			d.rxFrames.Add(1)
			return len(p), nil
		}
		select {
		case <-q.kicked:
		case <-d.rxWake:
		case <-d.done:
			return 0, io.ErrClosedPipe
		}
	}
}

// Close disconnects from the master and releases every resource.
func (d *Device) Close() error {
	d.markDone(nil)
	d.wg.Wait()
	return nil
}

func (d *Device) markDone(err error) {
	d.once.Do(func() {
		d.doneErr.Store(&err)
		close(d.done)
		_ = d.conn.Close()
	})
}

func (q *queue) pushRX(d *Device, frame []byte) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.running {
		head, ok, err := q.peekLocked()
		if err != nil {
			d.markDone(err)
			return false, err
		}
		if !ok {
			return false, nil
		}
		segs, total, err := q.walkLocked(head, true)
		if err != nil {
			d.malformed.Add(1)
			q.lastAvail++
			q.completeLocked(uint32(head), 0)
			q.notifyLocked()
			continue
		}
		need := uint64(netHdrSize + len(frame))
		if total < need {
			d.rxTooLarge.Add(1)
			return false, errFrameLarge
		}
		var hdr [netHdrSize]byte
		binary.LittleEndian.PutUint16(hdr[10:], 1)
		scatter(segs, hdr[:], frame)
		q.lastAvail++
		q.completeLocked(uint32(head), uint32(need))
		q.notifyLocked()
		return true, nil
	}
	return false, nil
}

func scatter(segs []seg, parts ...[]byte) {
	si, so := 0, 0
	for _, p := range parts {
		for len(p) > 0 {
			for si < len(segs) && so == len(segs[si]) {
				si, so = si+1, 0
			}
			n := copy(segs[si][so:], p)
			so += n
			p = p[n:]
		}
	}
}

func (q *queue) runTX(d *Device, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		if !q.drainTX(d, stop) {
			return
		}
		select {
		case <-q.kicked:
		case <-stop:
			return
		case <-d.done:
			return
		}
	}
}

func (q *queue) drainTX(d *Device, stop <-chan struct{}) bool {
	pending := 0
	flush := func() {
		if pending > 0 {
			q.mu.Lock()
			if q.running {
				q.notifyLocked()
			}
			q.mu.Unlock()
			pending = 0
		}
	}
	defer flush()
	for {
		q.mu.Lock()
		if !q.running {
			q.mu.Unlock()
			return false
		}
		head, ok, err := q.peekLocked()
		if err != nil {
			q.mu.Unlock()
			d.markDone(err)
			return false
		}
		if !ok {
			q.mu.Unlock()
			return true
		}
		segs, total, err := q.walkLocked(head, false)
		var frame []byte
		if err == nil && total >= netHdrSize {
			frame = gather(segs, total)[netHdrSize:]
		} else {
			d.malformed.Add(1)
		}
		if len(frame) == 0 {
			q.lastAvail++
			q.completeLocked(uint32(head), 0)
			pending++
			q.mu.Unlock()
			continue
		}
		q.mu.Unlock()

		select {
		case d.txq <- frame:
		case <-stop:
			return false
		case <-d.done:
			return false
		}
		d.txFrames.Add(1)

		q.mu.Lock()
		if !q.running {
			q.mu.Unlock()
			return false
		}
		q.lastAvail++
		q.completeLocked(uint32(head), 0)
		pending++
		q.mu.Unlock()
		if pending >= notifyBatch {
			flush()
		}
	}
}

func (d *Device) control() {
	defer d.wg.Done()
	err := d.serve()
	d.markDone(err)
	d.cleanup()
}

func (d *Device) cleanup() {
	d.stopAll()
	for _, q := range d.q {
		q.mu.Lock()
		closeFile(&q.kick)
		closeFile(&q.call)
		q.mu.Unlock()
	}
	d.mem.unmapAll()
}

func closeFile(f **os.File) {
	if *f != nil {
		_ = (*f).Close()
		*f = nil
	}
}

func (d *Device) stopAll() {
	for _, q := range d.q {
		q.stopRing()
	}
}

func (d *Device) startAll() error {
	for _, q := range d.q {
		q.mu.Lock()
		if q.running || !q.ready() {
			q.mu.Unlock()
			continue
		}
		if err := q.startLocked(d.mem); err != nil {
			q.mu.Unlock()
			return fmt.Errorf("start queue %d: %w", q.idx, err)
		}
		stop, done := q.stop, q.done
		q.mu.Unlock()
		if q.idx == txQueue {
			go q.runTX(d, stop, done)
			q.poke()
		} else {
			close(done)
			select {
			case d.rxWake <- struct{}{}:
			default:
			}
		}
	}
	return nil
}

func (d *Device) serve() error {
	var hdrBuf [hdrSize]byte
	for {
		fds, err := d.readFull(hdrBuf[:], nil)
		if err != nil {
			closeFDs(fds)
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		h, err := parseHeader(hdrBuf[:])
		if err != nil {
			closeFDs(fds)
			return err
		}
		body := make([]byte, h.Size)
		more, err := d.readFull(body, fds)
		fds = more
		if err != nil {
			closeFDs(fds)
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		msg, err := decodeMessage(h, body, len(fds))
		if err != nil {
			closeFDs(fds)
			return err
		}
		herr := d.handle(h, msg, fds)
		if herr != nil {
			if d.protoFeats&pfReplyAck != 0 && h.needReply() && !errors.Is(herr, errProto) {
				if err := d.reply(h, 1); err != nil {
					return err
				}
				continue
			}
			return herr
		}
	}
}

// readFull fills buf from the socket, collecting SCM_RIGHTS fds into fds.
func (d *Device) readFull(buf []byte, fds []int) ([]int, error) {
	oob := make([]byte, unix.CmsgSpace(4*maxFDsPerMsg))
	for got := 0; got < len(buf); {
		n, oobn, flags, _, err := d.conn.ReadMsgUnix(buf[got:], oob)
		if oobn > 0 {
			msgs, perr := unix.ParseSocketControlMessage(oob[:oobn])
			if perr != nil {
				return fds, perr
			}
			for i := range msgs {
				rights, perr := unix.ParseUnixRights(&msgs[i])
				if perr == nil {
					fds = append(fds, rights...)
				}
			}
		}
		if flags&unix.MSG_CTRUNC != 0 {
			return fds, fmt.Errorf("%w: truncated ancillary data", errProto)
		}
		got += n
		if err != nil {
			return fds, err
		}
		if n == 0 && oobn == 0 {
			return fds, io.EOF
		}
	}
	return fds, nil
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
}

func (d *Device) reply(h header, v uint64) error {
	buf := appendHeader(nil, header{Request: h.Request, Flags: flagVersion1 | flagReply, Size: 8})
	buf = binary.LittleEndian.AppendUint64(buf, v)
	_, err := d.conn.Write(buf)
	return err
}

func (d *Device) ack(h header) error {
	if d.protoFeats&pfReplyAck != 0 && h.needReply() {
		return d.reply(h, 0)
	}
	return nil
}

func (d *Device) reset() {
	d.stopAll()
	for _, q := range d.q {
		q.mu.Lock()
		closeFile(&q.kick)
		closeFile(&q.call)
		q.haveNum, q.haveAdr, q.enabled, q.base = false, false, false, 0
		q.mu.Unlock()
	}
	d.mem.unmapAll()
	d.mem = &memTable{}
	d.features, d.protoFeats = 0, 0
}
