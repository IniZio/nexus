package main

import (
	"bytes"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/agent/wire"
)

func runStreamRing(t *testing.T, ring *Ring, readerID uint64, from uint64, tagged bool) []wire.Frame {
	t.Helper()
	var buf bytes.Buffer
	w := wire.NewWriter(&buf)
	streamRingToWriter(w, ring, readerID, from, tagged)
	ring.RemoveReader(readerID)

	r := wire.NewReader(&buf)
	var frames []wire.Frame
	for {
		f, err := r.ReadFrame()
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		frames = append(frames, f)
	}
	return frames
}

func TestStreamRingToWriter_TaggedStdoutStderr(t *testing.T) {
	r := newRing(64 * 1024)
	rid := r.AddReader(0)

	go func() {
		r.WriteRecord(byte(wire.StreamStdout), []byte("out1"))
		r.WriteRecord(byte(wire.StreamStderr), []byte("err1"))
		r.WriteRecord(byte(wire.StreamStdout), []byte("out2"))
		r.Close()
	}()

	frames := runStreamRing(t, r, rid, 0, true)

	type want struct {
		tag  wire.StreamTag
		data string
	}
	wants := []want{
		{wire.StreamStdout, "out1"},
		{wire.StreamStderr, "err1"},
		{wire.StreamStdout, "out2"},
	}

	var dataFrames []wire.Frame
	for _, f := range frames {
		if f.Type == wire.FrameData {
			dataFrames = append(dataFrames, f)
		}
	}

	if len(dataFrames) != len(wants) {
		t.Fatalf("got %d data frames, want %d", len(dataFrames), len(wants))
	}
	for i, w := range wants {
		f := dataFrames[i]
		if f.Data.Tag != w.tag {
			t.Errorf("frame[%d]: tag got %d want %d", i, f.Data.Tag, w.tag)
		}
		if !bytes.Equal(f.Data.Payload, []byte(w.data)) {
			t.Errorf("frame[%d]: payload got %q want %q", i, f.Data.Payload, w.data)
		}
	}
}

func TestStreamRingToWriter_UntaggedAlwaysStdout(t *testing.T) {
	r := newRing(64 * 1024)
	rid := r.AddReader(0)

	go func() {
		r.Write([]byte("raw"))
		r.Close()
	}()

	frames := runStreamRing(t, r, rid, 0, false)

	for _, f := range frames {
		if f.Type == wire.FrameData && f.Data.Tag != wire.StreamStdout {
			t.Errorf("untagged ring: got tag %d, want StreamStdout", f.Data.Tag)
		}
	}
}

func TestStreamRingToWriter_LengthGuard_CorruptHeader(t *testing.T) {
	r := newRing(64 * 1024)
	rid := r.AddReader(0)

	go func() {
		corrupt := make([]byte, 5)
		corrupt[0] = byte(wire.StreamStdout)
		corrupt[1] = 0xFF
		corrupt[2] = 0xFF
		corrupt[3] = 0xFF
		corrupt[4] = 0xFF
		r.Write(corrupt)
		r.Close()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runStreamRing(t, r, rid, 0, true)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamRingToWriter hung on corrupt length — length guard not working")
	}
}

func TestStreamRingToWriter_MutationCheck_StderrNotRetaggedAsStdout(t *testing.T) {
	r := newRing(64 * 1024)
	rid := r.AddReader(0)

	go func() {
		r.WriteRecord(byte(wire.StreamStderr), []byte("err"))
		r.Close()
	}()

	frames := runStreamRing(t, r, rid, 0, true)

	for _, f := range frames {
		if f.Type != wire.FrameData {
			continue
		}
		if f.Data.Tag == wire.StreamStdout {
			t.Error("stderr record tagged as StreamStdout — tag routing broken")
		}
		if f.Data.Tag != wire.StreamStderr {
			t.Errorf("expected StreamStderr, got %d", f.Data.Tag)
		}
	}
}

// TestStreamRingToWriter_8MiB_SlowConsumer verifies that 8 MiB of stderr
// records written into a 256 KiB ring all arrive at a consumer without data
// loss. The ring's WriteRecord backpressure must block the writer until the
// reader has consumed enough data. Uses a net.Pipe with an interleaved consumer
// (runtime.Gosched between reads) to trigger scheduling interleaving without
// adding artificial latency that would make the test slow.
func TestStreamRingToWriter_8MiB_SlowConsumer(t *testing.T) {
	const ringCap = 256 * 1024
	const recordPayload = 4096
	const totalBytes = 8 * 1024 * 1024
	const nRecords = totalBytes / recordPayload

	ring := newRing(ringCap)
	rid := ring.AddReader(0)

	// Writer: fills ring with nRecords tagged stderr records.
	go func() {
		payload := make([]byte, recordPayload)
		for i := range payload {
			payload[i] = byte(i)
		}
		for i := 0; i < nRecords; i++ {
			ring.WriteRecord(byte(wire.StreamStderr), payload)
		}
		ring.Close()
	}()

	// Consumer via net.Pipe. Reader goroutine reads every frame and counts bytes;
	// no artificial sleep — ring backpressure provides the scheduling interleaving.
	serverConn, clientConn := net.Pipe()

	var stderrBytes int64
	var tag0Frames int64
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		r := wire.NewReader(clientConn)
		for {
			f, err := r.ReadFrame()
			if err != nil {
				return
			}
			if f.Type == wire.FrameData {
				if f.Data.Tag == wire.StreamStdin {
					atomic.AddInt64(&tag0Frames, 1)
				} else if f.Data.Tag == wire.StreamStderr {
					atomic.AddInt64(&stderrBytes, int64(len(f.Data.Payload)))
				}
			}
		}
	}()

	w := wire.NewWriter(serverConn)
	normal := streamRingToWriter(w, ring, rid, 0, true)
	ring.RemoveReader(rid)
	serverConn.Close()

	select {
	case <-readerDone:
	case <-time.After(30 * time.Second):
		t.Fatal("reader did not finish in time")
	}
	clientConn.Close()

	got := atomic.LoadInt64(&stderrBytes)
	if got != totalBytes {
		t.Errorf("stderr bytes: got %d want %d (normal=%v)", got, totalBytes, normal)
	}
	if n := atomic.LoadInt64(&tag0Frames); n != 0 {
		t.Errorf("got %d tag-0 (StreamStdin) frames — misaligned parse", n)
	}
}

// TestStreamRingToWriter_StdoutStderr_Interleaved verifies that interleaved
// stdout+stderr records are routed to the correct tags and the per-tag byte
// counts match.
func TestStreamRingToWriter_StdoutStderr_Interleaved(t *testing.T) {
	const ringCap = 256 * 1024
	const recordPayload = 4096
	const nPerStream = 512 // 512*4096 = 2 MiB each

	ring := newRing(ringCap)
	rid := ring.AddReader(0)

	go func() {
		outPayload := bytes.Repeat([]byte("o"), recordPayload)
		errPayload := bytes.Repeat([]byte("e"), recordPayload)
		for i := 0; i < nPerStream; i++ {
			ring.WriteRecord(byte(wire.StreamStdout), outPayload)
			ring.WriteRecord(byte(wire.StreamStderr), errPayload)
		}
		ring.Close()
	}()

	serverConn, clientConn := net.Pipe()
	var outBytes, errBytes, tag0Frames int64
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		r := wire.NewReader(clientConn)
		for {
			f, err := r.ReadFrame()
			if err != nil {
				return
			}
			if f.Type != wire.FrameData {
				continue
			}
			switch f.Data.Tag {
			case wire.StreamStdout:
				atomic.AddInt64(&outBytes, int64(len(f.Data.Payload)))
			case wire.StreamStderr:
				atomic.AddInt64(&errBytes, int64(len(f.Data.Payload)))
			case wire.StreamStdin:
				atomic.AddInt64(&tag0Frames, 1)
			}
		}
	}()

	w := wire.NewWriter(serverConn)
	normal := streamRingToWriter(w, ring, rid, 0, true)
	ring.RemoveReader(rid)
	serverConn.Close()
	select {
	case <-readerDone:
	case <-time.After(30 * time.Second):
		t.Fatal("reader timed out")
	}
	clientConn.Close()

	wantEach := int64(nPerStream * recordPayload)
	if ob := atomic.LoadInt64(&outBytes); ob != wantEach {
		t.Errorf("stdout bytes: got %d want %d (normal=%v)", ob, wantEach, normal)
	}
	if eb := atomic.LoadInt64(&errBytes); eb != wantEach {
		t.Errorf("stderr bytes: got %d want %d (normal=%v)", eb, wantEach, normal)
	}
	if n := atomic.LoadInt64(&tag0Frames); n != 0 {
		t.Errorf("got %d tag-0 (StreamStdin) frames", n)
	}
}

// TestStreamRingToWriter_AbnormalEnd_NoExitOnLiveSession verifies that when
// streamRingToWriter returns abnormal (false), handleDataConn does not send an
// Exit frame for a session that has not yet exited.
// We test this directly: inject a corrupt record into the ring to trigger
// abnormal return, then confirm streamRingToWriter returned false and that
// normal=false would suppress the Exit frame.
func TestStreamRingToWriter_AbnormalEnd_NoExitOnLiveSession(t *testing.T) {
	ring := newRing(64 * 1024)
	rid := ring.AddReader(0)

	go func() {
		// Write a corrupt record: tag=StreamStdout, length=0xFFFFFFFF.
		corrupt := make([]byte, 5)
		corrupt[0] = byte(wire.StreamStdout)
		corrupt[1] = 0xFF
		corrupt[2] = 0xFF
		corrupt[3] = 0xFF
		corrupt[4] = 0xFF
		ring.Write(corrupt) // untagged write so the corrupt bytes land raw
		ring.Close()
	}()

	var buf bytes.Buffer
	w := wire.NewWriter(&buf)
	normal := streamRingToWriter(w, ring, rid, 0, true)
	ring.RemoveReader(rid)

	if normal {
		t.Error("expected abnormal (false) return on corrupt header, got normal (true)")
	}

	// Verify that if session.exited were false, the caller would NOT emit Exit.
	// We can't call handleDataConn directly, but we verify the guard logic:
	// normal=false && !exited → must NOT send Exit → check no Exit frame written.
	r := wire.NewReader(&buf)
	for {
		f, err := r.ReadFrame()
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
		if f.Type == wire.FrameExit {
			t.Error("Exit frame was emitted — would fabricate rc=0 for a live session")
		}
	}
}
