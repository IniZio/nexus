//go:build linux

package vhostnet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"
)

func readFrame(t *testing.T, d *Device) []byte {
	t.Helper()
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		buf := make([]byte, 70000)
		n, err := d.Read(buf)
		ch <- res{buf[:n], err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Read: %v", r.err)
		}
		return r.b
	case <-time.After(5 * time.Second):
		t.Fatal("Read timed out")
		return nil
	}
}

func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i*7)
	}
	return b
}

func (m *fakeMaster) put(off int, b []byte) { copy(m.ram[off:], b) }

func TestTXScatterGatherReassembly(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	tx := m.qs[txQueue]

	frame := pattern(1400, 3)
	hdr := make([]byte, netHdrSize)
	m.put(bufBase, hdr)
	m.put(bufBase+0x100, frame[:5])
	m.put(bufBase+0x2000, frame[5:600])
	straddle := half - 16
	m.put(straddle, frame[600:616])
	m.put(half, frame[616:])
	head := tx.post([]gdesc{
		{ramGPA + bufBase, netHdrSize, false},
		{ramGPA + bufBase + 0x100, 5, false},
		{ramGPA + bufBase + 0x2000, 595, false},
		{ramGPA + uint64(straddle), uint32(16 + len(frame) - 616), false},
	}, true)

	got := readFrame(t, m.dev)
	if !bytes.Equal(got, frame) {
		t.Fatalf("frame mismatch: got %d bytes", len(got))
	}
	id, ln := tx.waitUsed(1)
	if int(id) != head || ln != 0 {
		t.Fatalf("used = (%d,%d), want (%d,0)", id, ln, head)
	}
	if tx.calls() == 0 {
		t.Fatal("no call notification for TX completion")
	}
	if s := m.dev.Stats(); s.TXFrames != 1 || s.Malformed != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestRXFillUsedRingAndCall(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	rx := m.qs[rxQueue]

	rx.post([]gdesc{{ramGPA + bufBase, 200, true}, {ramGPA + bufBase + 0x1000, 2048, true}}, true)
	frame := pattern(900, 9)
	n, err := m.dev.Write(frame)
	if err != nil || n != len(frame) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	id, ln := rx.waitUsed(1)
	if id != 0 || int(ln) != netHdrSize+len(frame) {
		t.Fatalf("used = (%d,%d)", id, ln)
	}
	hdr := m.ram[bufBase : bufBase+netHdrSize]
	if !bytes.Equal(hdr[:10], make([]byte, 10)) || binary.LittleEndian.Uint16(hdr[10:]) != 1 {
		t.Fatalf("virtio_net_hdr = %x", hdr)
	}
	out := append(append([]byte{}, m.ram[bufBase+netHdrSize:bufBase+200]...), m.ram[bufBase+0x1000:bufBase+0x1000+len(frame)-(200-netHdrSize)]...)
	if !bytes.Equal(out, frame) {
		t.Fatal("RX payload mismatch")
	}
	if rx.calls() == 0 {
		t.Fatal("no call notification for RX completion")
	}
}

func TestRXWriteBlocksUntilBufferPosted(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	rx := m.qs[rxQueue]
	done := make(chan error, 1)
	go func() { _, err := m.dev.Write(pattern(100, 1)); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("Write returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	rx.post([]gdesc{{ramGPA + bufBase, 2048, true}}, true)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rx.waitUsed(1)
}

func TestRXFrameTooLargeLeavesBufferUntouched(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	rx := m.qs[rxQueue]
	rx.post([]gdesc{{ramGPA + bufBase, 64, true}}, true)
	if _, err := m.dev.Write(pattern(500, 1)); !errors.Is(err, errFrameLarge) {
		t.Fatalf("err = %v", err)
	}
	if rx.usedIdx() != 0 {
		t.Fatal("buffer consumed")
	}
	if _, err := m.dev.Write(pattern(20, 1)); err != nil {
		t.Fatalf("small frame after: %v", err)
	}
}

func TestRingWrapAtQueueSize256(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	tx, rx := m.qs[txQueue], m.qs[rxQueue]
	const rounds = 600
	for i := range rounds {
		frame := pattern(60+i%50, byte(i))
		m.put(bufBase, make([]byte, netHdrSize))
		m.put(bufBase+netHdrSize, frame)
		tx.post([]gdesc{{ramGPA + bufBase, uint32(netHdrSize + len(frame)), false}}, true)
		if got := readFrame(t, m.dev); !bytes.Equal(got, frame) {
			t.Fatalf("round %d: TX mismatch", i)
		}
		tx.waitUsed(1)

		rx.post([]gdesc{{ramGPA + bufBase + 0x8000, 2048, true}}, true)
		if _, err := m.dev.Write(frame); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		_, ln := rx.waitUsed(1)
		if int(ln) != netHdrSize+len(frame) {
			t.Fatalf("round %d: rx len %d", i, ln)
		}
		if !bytes.Equal(m.ram[bufBase+0x8000+netHdrSize:bufBase+0x8000+netHdrSize+len(frame)], frame) {
			t.Fatalf("round %d: RX mismatch", i)
		}
	}
	if tx.usedIdx() != uint16(rounds) || tx.availIdx != uint16(rounds) {
		t.Fatalf("indices tx used=%d avail=%d", tx.usedIdx(), tx.availIdx)
	}
}

func TestAvailNoInterruptSuppressesCall(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	tx := m.qs[txQueue]
	post := func(seed byte) {
		m.put(bufBase, make([]byte, netHdrSize))
		m.put(bufBase+netHdrSize, pattern(64, seed))
		tx.post([]gdesc{{ramGPA + bufBase, netHdrSize + 64, false}}, true)
		readFrame(t, m.dev)
		tx.waitUsed(1)
	}
	post(1)
	if tx.calls() == 0 {
		t.Fatal("expected call with suppression off")
	}
	binary.LittleEndian.PutUint16(m.ram[tx.availAt:], availFNoInterrupt)
	post(2)
	time.Sleep(20 * time.Millisecond)
	if c := tx.calls(); c != 0 {
		t.Fatalf("call fired %d times despite VRING_AVAIL_F_NO_INTERRUPT", c)
	}
	binary.LittleEndian.PutUint16(m.ram[tx.availAt:], 0)
	post(3)
	if tx.calls() == 0 {
		t.Fatal("expected call after suppression cleared")
	}
}

func TestMalformedDescriptorsRejectedWithoutPanic(t *testing.T) {
	cases := map[string]func(m *fakeMaster, tx *fakeQueue){
		"addr outside regions": func(m *fakeMaster, tx *fakeQueue) {
			tx.post([]gdesc{{0xdead0000000, 128, false}}, true)
		},
		"addr+len overflow": func(m *fakeMaster, tx *fakeQueue) {
			tx.post([]gdesc{{^uint64(0) - 10, 128, false}}, true)
		},
		"len past region end": func(m *fakeMaster, tx *fakeQueue) {
			tx.post([]gdesc{{ramGPA + 2*half - 8, 4096, false}}, true)
		},
		"descriptor loop": func(m *fakeMaster, tx *fakeQueue) {
			head := tx.post([]gdesc{{ramGPA + bufBase, 32, false}, {ramGPA + bufBase, 32, false}}, false)
			o := tx.descAt + ((head+1)%tx.size)*descSize
			binary.LittleEndian.PutUint16(m.ram[o+12:], descFNext)
			binary.LittleEndian.PutUint16(m.ram[o+14:], uint16(head))
			tx.doKick()
		},
		"next index beyond ring": func(m *fakeMaster, tx *fakeQueue) {
			head := tx.post([]gdesc{{ramGPA + bufBase, 32, false}}, false)
			o := tx.descAt + head*descSize
			binary.LittleEndian.PutUint16(m.ram[o+12:], descFNext)
			binary.LittleEndian.PutUint16(m.ram[o+14:], 9999)
			tx.doKick()
		},
		"indirect not negotiated": func(m *fakeMaster, tx *fakeQueue) {
			head := tx.post([]gdesc{{ramGPA + bufBase, 32, false}}, false)
			binary.LittleEndian.PutUint16(m.ram[tx.descAt+head*descSize+12:], descFIndirect)
			tx.doKick()
		},
		"device-writable on TX": func(m *fakeMaster, tx *fakeQueue) {
			tx.post([]gdesc{{ramGPA + bufBase, 64, true}}, true)
		},
		"shorter than net header": func(m *fakeMaster, tx *fakeQueue) {
			tx.post([]gdesc{{ramGPA + bufBase, 4, false}}, true)
		},
		"chain over size cap": func(m *fakeMaster, tx *fakeQueue) {
			tx.post([]gdesc{{ramGPA, 1 << 20, false}, {ramGPA + half, 1 << 19, false}}, true)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMaster(t, 256)
			m.handshake()
			tx := m.qs[txQueue]
			mutate(m, tx)
			id, ln := tx.waitUsed(1)
			if ln != 0 || id != 0 {
				t.Fatalf("used = (%d,%d)", id, ln)
			}
			if s := m.dev.Stats(); s.Malformed != 1 || s.TXFrames != 0 {
				t.Fatalf("stats %+v", s)
			}
			frame := pattern(80, 5)
			m.put(bufBase+0x4000, make([]byte, netHdrSize))
			m.put(bufBase+0x4000+netHdrSize, frame)
			tx.post([]gdesc{{ramGPA + bufBase + 0x4000, uint32(netHdrSize + len(frame)), false}}, true)
			if got := readFrame(t, m.dev); !bytes.Equal(got, frame) {
				t.Fatal("device wedged after malformed chain")
			}
		})
	}
}

func TestCorruptAvailIndexKillsDeviceCleanly(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	tx := m.qs[txQueue]
	store16(m.ram[tx.availAt:], 2, 5000)
	tx.doKick()
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 2000)
	for {
		_, err := m.dev.Read(buf)
		if errors.Is(err, errRing) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Read err = %v, want errRing", err)
		}
	}
}

func TestGetVringBaseReportsConsumedIndex(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	tx := m.qs[txQueue]
	for i := range 3 {
		m.put(bufBase, make([]byte, netHdrSize))
		m.put(bufBase+netHdrSize, pattern(50, byte(i)))
		tx.post([]gdesc{{ramGPA + bufBase, netHdrSize + 50, false}}, true)
		readFrame(t, m.dev)
		tx.waitUsed(1)
	}
	m.send(reqSetVringEnable, true, binary.LittleEndian.AppendUint64(nil, uint64(txQueue)))
	m.ackOK(reqSetVringEnable)
	m.send(reqGetVringBase, false, binary.LittleEndian.AppendUint64(nil, uint64(txQueue)))
	h, b := m.recv()
	if h.Request != reqGetVringBase || len(b) != 8 || binary.LittleEndian.Uint32(b[4:]) != 3 {
		t.Fatalf("GET_VRING_BASE reply %+v %x", h, b)
	}
}

func TestDisabledQueueDoesNotProcessUntilReenabled(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	tx := m.qs[txQueue]
	m.send(reqSetVringEnable, true, binary.LittleEndian.AppendUint64(nil, uint64(txQueue)))
	m.ackOK(reqSetVringEnable)
	m.put(bufBase, make([]byte, netHdrSize))
	m.put(bufBase+netHdrSize, pattern(50, 1))
	tx.post([]gdesc{{ramGPA + bufBase, netHdrSize + 50, false}}, true)
	time.Sleep(30 * time.Millisecond)
	if tx.usedIdx() != 0 {
		t.Fatal("disabled queue consumed a buffer")
	}
	enable := binary.LittleEndian.AppendUint32(nil, txQueue)
	enable = binary.LittleEndian.AppendUint32(enable, 1)
	m.send(reqSetVringEnable, true, enable)
	m.ackOK(reqSetVringEnable)
	readFrame(t, m.dev)
	tx.waitUsed(1)
}

func waitGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutine leak: %d > %d\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMasterDisconnectReturnsEOFWithoutLeak(t *testing.T) {
	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()
	func() {
		m := newMaster(t, 256)
		m.handshake()
		tx := m.qs[txQueue]
		m.put(bufBase, make([]byte, netHdrSize))
		m.put(bufBase+netHdrSize, pattern(50, 1))
		tx.post([]gdesc{{ramGPA + bufBase, netHdrSize + 50, false}}, true)
		blocked := make(chan error, 1)
		go func() { _, err := m.dev.Write(pattern(10, 1)); blocked <- err }()
		time.Sleep(20 * time.Millisecond)
		_ = m.conn.Close()

		buf := make([]byte, 2000)
		n, err := m.dev.Read(buf)
		if err != nil || n != 50 {
			t.Fatalf("queued frame lost: %d, %v", n, err)
		}
		if _, err := m.dev.Read(buf); err != io.EOF {
			t.Fatalf("Read after disconnect = %v, want io.EOF", err)
		}
		if err := <-blocked; !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("blocked Write = %v", err)
		}
		if _, err := m.dev.Write(buf[:10]); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Write after disconnect = %v", err)
		}
		if err := m.dev.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	waitGoroutines(t, base)
}

func TestCloseBeforeHandshakeDoesNotLeak(t *testing.T) {
	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()
	func() {
		m := newMaster(t, 256)
		if err := m.dev.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := m.dev.Read(make([]byte, 10)); err != io.EOF {
			t.Fatalf("Read = %v", err)
		}
	}()
	waitGoroutines(t, base)
}

func TestHotAddedRegionKeepsRingsRunning(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	m.send(reqSetProtocolFeatures, false, u64(offeredProtocolFeatures))
	tx := m.qs[txQueue]
	body := binary.LittleEndian.AppendUint64(nil, 0)
	body = append(body, regionBody(ramGPA+4*half, half, m.uaddr(half), half)...)
	m.send(reqAddMemReg, true, body, int(m.ramFile.Fd()))
	m.ackOK(reqAddMemReg)

	m.put(bufBase, make([]byte, netHdrSize))
	m.put(half+0x100, pattern(70, 4))
	tx.post([]gdesc{{ramGPA + bufBase, netHdrSize, false}, {ramGPA + 4*half + 0x100, 70, false}}, true)
	if got := readFrame(t, m.dev); !bytes.Equal(got, pattern(70, 4)) {
		t.Fatal("frame from hot-added region mismatch")
	}
	rem := binary.LittleEndian.AppendUint64(nil, 0)
	rem = append(rem, regionBody(ramGPA+4*half, half, m.uaddr(half), half)...)
	m.send(reqRemMemReg, true, rem)
	m.ackOK(reqRemMemReg)
	tx.waitUsed(1)
	tx.post([]gdesc{{ramGPA + bufBase, netHdrSize, false}, {ramGPA + 4*half + 0x100, 70, false}}, true)
	tx.waitUsed(1)
	if s := m.dev.Stats(); s.Malformed != 1 {
		t.Fatalf("access to removed region not rejected: %+v", s)
	}
}

func TestProtocolViolationClosesConnection(t *testing.T) {
	m := newMaster(t, 256)
	m.send(9999, false, nil)
	buf := make([]byte, 10)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := m.dev.Read(buf); err != nil {
			if !errors.Is(err, errProto) {
				t.Fatalf("err = %v, want protocol violation", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
	}
}

// A restored CH sends the guest's avail idx as the vring base, skipping RX
// buffers that were posted but never consumed. The queue must resume at the
// used ring instead, or RX stalls forever after restore.
func TestRestoreBaseAheadOfUsedRingStillFillsPostedBuffers(t *testing.T) {
	m := newMaster(t, 256)
	m.handshake()
	rx := m.qs[rxQueue]
	for i := range 4 {
		rx.post([]gdesc{{ramGPA + uint64(bufBase+i*0x1000), 2048, true}}, i == 3)
	}
	if _, err := m.dev.Write(pattern(100, 1)); err != nil {
		t.Fatal(err)
	}
	if id, _ := rx.waitUsed(1); id != 0 {
		t.Fatalf("first fill used head %d, want 0", id)
	}

	state := binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, uint32(rxQueue)), 4)
	m.send(reqSetVringBase, true, state)
	m.ackOK(reqSetVringBase)
	m.send(reqSetVringEnable, true, binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, uint32(rxQueue)), 1))
	m.ackOK(reqSetVringEnable)

	wrote := make(chan error, 1)
	go func() { _, err := m.dev.Write(pattern(100, 2)); wrote <- err }()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RX write stalled: posted buffers were skipped")
	}
	if id, _ := rx.waitUsed(1); id != 1 {
		t.Fatalf("second fill used head %d, want 1 (base 4 must not skip posted buffers)", id)
	}
}
