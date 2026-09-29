//go:build linux

package vhostnet

import (
	"encoding/binary"
	"net"
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	ramGPA  = 0x100000
	half    = 1 << 20
	descOff = 0x0000
	availAt = 0x4000
	usedAt  = 0x5000
	bufBase = 0x10000
)

type fakeMaster struct {
	t       *testing.T
	conn    *net.UnixConn
	ram     []byte
	ramFile *os.File
	dev     *Device
	qs      [numQueues]*fakeQueue
}

type fakeQueue struct {
	m         *fakeMaster
	idx       int
	size      int
	descAt    int
	availAt   int
	usedAt    int
	kick      *os.File
	call      *os.File
	availIdx  uint16
	nextDesc  int
	usedSeen  uint16
	callCount int
}

func newMaster(t *testing.T, size int) *fakeMaster {
	t.Helper()
	fd, err := unix.MemfdCreate("guest-ram", unix.MFD_CLOEXEC)
	if err != nil {
		t.Skipf("memfd_create: %v", err)
	}
	f := os.NewFile(uintptr(fd), "guest-ram")
	if err := f.Truncate(2 * half); err != nil {
		t.Fatal(err)
	}
	ram, err := unix.Mmap(fd, 0, 2*half, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	mine := os.NewFile(uintptr(pair[0]), "master")
	theirs := os.NewFile(uintptr(pair[1]), "slave")
	mc, err := net.FileConn(mine)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := net.FileConn(theirs)
	if err != nil {
		t.Fatal(err)
	}
	_ = mine.Close()
	_ = theirs.Close()
	dev, err := Serve(sc.(*net.UnixConn), Config{})
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeMaster{t: t, conn: mc.(*net.UnixConn), ram: ram, ramFile: f, dev: dev}
	t.Cleanup(func() {
		_ = m.conn.Close()
		_ = dev.Close()
		_ = f.Close()
		_ = unix.Munmap(ram)
	})
	for i := range m.qs {
		m.qs[i] = &fakeQueue{m: m, idx: i, size: size, descAt: descOff + i*0x8000, availAt: availAt + i*0x8000, usedAt: usedAt + i*0x8000}
	}
	return m
}

func (m *fakeMaster) uaddr(off int) uint64 { return uint64(uintptr(unsafe.Pointer(&m.ram[off]))) }

func (m *fakeMaster) send(req uint32, needReply bool, body []byte, fds ...int) {
	m.t.Helper()
	flags := uint32(flagVersion1)
	if needReply {
		flags |= flagNeedReply
	}
	buf := appendHeader(nil, header{Request: req, Flags: flags, Size: uint32(len(body))})
	buf = append(buf, body...)
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	if _, _, err := m.conn.WriteMsgUnix(buf, oob, nil); err != nil {
		m.t.Fatalf("send %d: %v", req, err)
	}
}

func (m *fakeMaster) recv() (header, []byte) {
	m.t.Helper()
	_ = m.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hb [hdrSize]byte
	readAll(m.t, m.conn, hb[:])
	h := header{binary.LittleEndian.Uint32(hb[0:]), binary.LittleEndian.Uint32(hb[4:]), binary.LittleEndian.Uint32(hb[8:])}
	body := make([]byte, h.Size)
	readAll(m.t, m.conn, body)
	return h, body
}

func readAll(t *testing.T, c *net.UnixConn, b []byte) {
	t.Helper()
	for got := 0; got < len(b); {
		n, err := c.Read(b[got:])
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		got += n
	}
}

func u64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

func (m *fakeMaster) ackOK(req uint32) {
	m.t.Helper()
	h, b := m.recv()
	if h.Request != req || h.Flags&flagReply == 0 || len(b) != 8 || binary.LittleEndian.Uint64(b) != 0 {
		m.t.Fatalf("bad ack for %d: %+v %x", req, h, b)
	}
}

func (m *fakeMaster) getU64(req uint32) uint64 {
	m.t.Helper()
	m.send(req, false, nil)
	h, b := m.recv()
	if h.Request != req || len(b) != 8 {
		m.t.Fatalf("bad reply to %d: %+v", req, h)
	}
	return binary.LittleEndian.Uint64(b)
}

func regionBody(gpa, size, uaddr, off uint64) []byte {
	var b []byte
	for _, v := range []uint64{gpa, size, uaddr, off} {
		b = binary.LittleEndian.AppendUint64(b, v)
	}
	return b
}

// handshake mimics the CH v53 activation sequence for one queue pair.
func (m *fakeMaster) handshake() {
	m.t.Helper()
	m.send(reqSetOwner, false, nil)
	feats := m.getU64(reqGetFeatures)
	if feats != offeredFeatures {
		m.t.Fatalf("features %#x", feats)
	}
	pf := m.getU64(reqGetProtocolFeatures)
	m.send(reqSetProtocolFeatures, false, u64(pf))
	m.send(reqSetFeatures, true, u64(feats&^featNetMAC))
	m.ackOK(reqSetFeatures)

	body := binary.LittleEndian.AppendUint32(nil, 2)
	body = binary.LittleEndian.AppendUint32(body, 0)
	body = append(body, regionBody(ramGPA, half, m.uaddr(0), 0)...)
	body = append(body, regionBody(ramGPA+half, half, m.uaddr(half), half)...)
	m.send(reqSetMemTable, true, body, int(m.ramFile.Fd()), int(m.ramFile.Fd()))
	m.ackOK(reqSetMemTable)

	for _, q := range m.qs {
		q.setup()
	}
}

func (q *fakeQueue) setup() {
	m := q.m
	m.t.Helper()
	pair := func() *os.File {
		fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
		if err != nil {
			m.t.Fatal(err)
		}
		return os.NewFile(uintptr(fd), "efd")
	}
	q.kick, q.call = pair(), pair()
	m.t.Cleanup(func() { _ = q.kick.Close(); _ = q.call.Close() })
	idx := uint32(q.idx)
	state := func(num uint32) []byte {
		return binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, idx), num)
	}
	m.send(reqSetVringNum, true, state(uint32(q.size)))
	m.ackOK(reqSetVringNum)
	addr := binary.LittleEndian.AppendUint32(nil, idx)
	addr = binary.LittleEndian.AppendUint32(addr, 0)
	for _, v := range []uint64{m.uaddr(q.descAt), m.uaddr(q.usedAt), m.uaddr(q.availAt), 0} {
		addr = binary.LittleEndian.AppendUint64(addr, v)
	}
	m.send(reqSetVringAddr, true, addr)
	m.ackOK(reqSetVringAddr)
	m.send(reqSetVringBase, true, state(0))
	m.ackOK(reqSetVringBase)
	m.send(reqSetVringCall, true, u64(uint64(idx)), int(q.call.Fd()))
	m.ackOK(reqSetVringCall)
	m.send(reqSetVringKick, true, u64(uint64(idx)), int(q.kick.Fd()))
	m.ackOK(reqSetVringKick)
	m.send(reqSetVringEnable, true, state(1))
	m.ackOK(reqSetVringEnable)
}

type gdesc struct {
	gpa   uint64
	len   uint32
	write bool
}

// post publishes one chain on the avail ring and kicks unless suppressed.
func (q *fakeQueue) post(chain []gdesc, kick bool) (head int) {
	ram := q.m.ram
	head = q.nextDesc
	for i, d := range chain {
		slot := (head + i) % q.size
		o := q.descAt + slot*descSize
		binary.LittleEndian.PutUint64(ram[o:], d.gpa)
		binary.LittleEndian.PutUint32(ram[o+8:], d.len)
		var fl uint16
		if d.write {
			fl |= descFWrite
		}
		next := uint16((head + i + 1) % q.size)
		if i < len(chain)-1 {
			fl |= descFNext
		}
		binary.LittleEndian.PutUint16(ram[o+12:], fl)
		binary.LittleEndian.PutUint16(ram[o+14:], next)
	}
	q.nextDesc = (head + len(chain)) % q.size
	binary.LittleEndian.PutUint16(ram[q.availAt+4+2*int(q.availIdx%uint16(q.size)):], uint16(head))
	q.availIdx++
	store16(ram[q.availAt:], 2, q.availIdx)
	if kick {
		q.doKick()
	}
	return head
}

func (q *fakeQueue) doKick() {
	if _, err := q.kick.Write(u64(1)); err != nil {
		q.m.t.Fatalf("kick: %v", err)
	}
}

func (q *fakeQueue) usedIdx() uint16 { return load16(q.m.ram[q.usedAt:], 2) }

func (q *fakeQueue) waitUsed(n uint16) (id, length uint32) {
	q.m.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for q.usedIdx() == q.usedSeen {
		if time.Now().After(deadline) {
			q.m.t.Fatalf("queue %d: no used entry (used=%d seen=%d)", q.idx, q.usedIdx(), q.usedSeen)
		}
		time.Sleep(time.Millisecond)
	}
	o := q.usedAt + 4 + 8*int(q.usedSeen%uint16(q.size))
	id = binary.LittleEndian.Uint32(q.m.ram[o:])
	length = binary.LittleEndian.Uint32(q.m.ram[o+4:])
	q.usedSeen++
	_ = n
	return id, length
}

func (q *fakeQueue) calls() int {
	var b [8]byte
	_ = q.call.SetReadDeadline(time.Now().Add(time.Millisecond))
	n, err := q.call.Read(b[:])
	if err != nil || n != 8 {
		return 0
	}
	return int(binary.LittleEndian.Uint64(b[:]))
}
