package main

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestRing_AttachedReaderBlocksWriter(t *testing.T) {
	const ringCap = 4096
	r := newRing(ringCap)
	r.Write(make([]byte, ringCap))

	rid := r.AddReader(0)

	writerStarted := make(chan struct{})
	writeDone := make(chan struct{})
	go func() {
		close(writerStarted)
		r.Write(make([]byte, 1))
		close(writeDone)
	}()

	<-writerStarted
	time.Sleep(20 * time.Millisecond)

	select {
	case <-writeDone:
		t.Fatal("writer completed without reader advancing: backpressure not working")
	default:
	}

	r.WaitNextCursored(rid, 0)
	r.RemoveReader(rid)
	r.Close()

	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not unblock after reader advanced")
	}
}

func TestRing_AttachedReaderByteIntegrity(t *testing.T) {
	const ringCap = 64 * 1024
	const total = ringCap * 3
	r := newRing(ringCap)
	rid := r.AddReader(0)
	defer r.RemoveReader(rid)

	go func() {
		buf := make([]byte, 4096)
		for i := range buf {
			buf[i] = byte(i)
		}
		written := 0
		for written < total {
			r.Write(buf)
			written += len(buf)
		}
		r.Close()
	}()

	received := 0
	off := uint64(0)
	for {
		data, newOff, done, overrun := r.WaitNextCursored(rid, off)
		if overrun {
			t.Fatal("overrun on attached reader")
		}
		received += len(data)
		off = newOff
		if done && len(data) == 0 {
			break
		}
	}
	if received != total {
		t.Fatalf("got %d bytes, want %d", received, total)
	}
}

func TestRing_NoReader_OverrunThenAttach(t *testing.T) {
	const ringCap = 64 * 1024
	r := newRing(ringCap)

	buf := make([]byte, ringCap+1024)
	r.Write(buf)
	r.Close()

	rid := r.AddReader(0)
	defer r.RemoveReader(rid)

	_, _, _, overrun := r.WaitNextCursored(rid, 0)
	if !overrun {
		t.Fatal("expected overrun when attaching after ring evicted bytes")
	}
}

func TestRing_CloseUnblocksWriter(t *testing.T) {
	const ringCap = 4096
	r := newRing(ringCap)

	rid := r.AddReader(0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Write(make([]byte, ringCap*2))
	}()

	time.Sleep(20 * time.Millisecond)
	r.RemoveReader(rid)
	r.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not unblock after Close")
	}
}

func TestRing_DetachedWriterNeverBlocks(t *testing.T) {
	const ringCap = 4096
	r := newRing(ringCap)

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Write(make([]byte, ringCap*4))
		r.Close()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("detached Write blocked unexpectedly")
	}
}

func TestRing_WriteRecord_TagsPreservedInOrder(t *testing.T) {
	const ringCap = 64 * 1024
	r := newRing(ringCap)
	rid := r.AddReader(0)
	defer r.RemoveReader(rid)

	go func() {
		r.WriteRecord(1, []byte("stdout-a"))
		r.WriteRecord(2, []byte("stderr-b"))
		r.WriteRecord(1, []byte("stdout-c"))
		r.Close()
	}()

	type record struct {
		tag  byte
		data []byte
	}
	var records []record
	off := uint64(0)
	var carry []byte
	for {
		chunk, newOff, done, overrun := r.WaitNextCursored(rid, off)
		if overrun {
			t.Fatal("unexpected overrun")
		}
		off = newOff
		if len(chunk) > 0 {
			var buf []byte
			if len(carry) > 0 {
				buf = append(carry, chunk...)
				carry = nil
			} else {
				buf = chunk
			}
			for len(buf) >= 5 {
				tag := buf[0]
				plen := int(binary.BigEndian.Uint32(buf[1:5]))
				if len(buf) < 5+plen {
					break
				}
				payload := make([]byte, plen)
				copy(payload, buf[5:5+plen])
				records = append(records, record{tag, payload})
				buf = buf[5+plen:]
			}
			if len(buf) > 0 {
				carry = append(carry[:0], buf...)
			}
		}
		if done && len(chunk) == 0 {
			break
		}
	}

	want := []record{{1, []byte("stdout-a")}, {2, []byte("stderr-b")}, {1, []byte("stdout-c")}}
	if len(records) != len(want) {
		t.Fatalf("got %d records, want %d", len(records), len(want))
	}
	for i, w := range want {
		if records[i].tag != w.tag || string(records[i].data) != string(w.data) {
			t.Fatalf("record[%d]: got tag=%d data=%q, want tag=%d data=%q",
				i, records[i].tag, records[i].data, w.tag, w.data)
		}
	}
}

func TestRing_WriteRecord_BackpressureBlocks(t *testing.T) {
	const ringCap = 128
	r := newRing(ringCap)
	r.WriteRecord(1, make([]byte, ringCap-5))

	rid := r.AddReader(0)

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		r.WriteRecord(1, []byte("x"))
	}()

	time.Sleep(20 * time.Millisecond)
	select {
	case <-writeDone:
		t.Fatal("writer should have been blocked by attached reader")
	default:
	}

	r.WaitNextCursored(rid, 0)
	r.RemoveReader(rid)
	r.Close()

	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not unblock after reader advanced")
	}
}

func TestRing_SnapToRecordBoundary_MidRecord(t *testing.T) {
	r := newRing(64 * 1024)
	r.WriteRecord(1, []byte("hello")) // [1][0 0 0 5][hello] = 10 bytes, offsets 0-9
	r.WriteRecord(2, []byte("world")) // [2][0 0 0 5][world] = 10 bytes, offsets 10-19
	r.Close()

	if got := r.SnapToRecordBoundary(0); got != 0 {
		t.Errorf("offset 0 (boundary): got %d want 0", got)
	}
	if got := r.SnapToRecordBoundary(2); got != 0 {
		t.Errorf("offset 2 (mid first record): got %d want 0 (snap back to record start)", got)
	}
	if got := r.SnapToRecordBoundary(10); got != 10 {
		t.Errorf("offset 10 (boundary): got %d want 10", got)
	}
	if got := r.SnapToRecordBoundary(13); got != 10 {
		t.Errorf("offset 13 (mid second record): got %d want 10 (snap back to record start)", got)
	}
	if got := r.SnapToRecordBoundary(20); got != 20 {
		t.Errorf("offset 20 (end): got %d want 20", got)
	}
}

func TestRing_SnapToRecordBoundary_MutationCheck(t *testing.T) {
	r := newRing(64 * 1024)
	r.WriteRecord(1, []byte("abc"))
	r.WriteRecord(2, []byte("xyz"))
	r.Close()

	snapped := r.SnapToRecordBoundary(4)
	if snapped == 4 {
		t.Error("SnapToRecordBoundary returned mid-record offset unchanged — realignment is broken")
	}
	if snapped != 0 {
		t.Errorf("expected snap to 0 (start of first record containing offset 4), got %d", snapped)
	}
}

func TestRing_WriteRecord_OverrunAlignedToBoundary(t *testing.T) {
	const ringCap = 256
	r := newRing(ringCap)

	r.WriteRecord(1, make([]byte, 60))
	r.WriteRecord(2, make([]byte, 60))
	r.WriteRecord(1, make([]byte, 60))
	r.WriteRecord(2, make([]byte, 60))
	r.Close()

	rid := r.AddReader(0)
	defer r.RemoveReader(rid)

	oldest := r.OldestOffset()
	_, _, _, overrun := r.WaitNextCursored(rid, 0)
	if !overrun {
		return
	}

	if oldest%65 != 0 {
		t.Fatalf("OldestOffset %d not aligned to record size 65", oldest)
	}
}
