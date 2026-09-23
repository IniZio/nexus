package main

import (
	"bytes"
	"io"
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
