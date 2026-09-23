package main

import (
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
