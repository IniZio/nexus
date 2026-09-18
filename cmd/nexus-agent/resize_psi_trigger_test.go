//go:build linux

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus/internal/core/resize"
)

func setupStreamFixtures(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4096000 kB\nMemAvailable: 2048000 kB\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, filepath.Join(dir, "absent"), filepath.Join(dir, "absent"))
	setStatfsFunc(t, func(_ string, _ *unix.Statfs_t) error { return nil })
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))
}

func injectFakePSI(t *testing.T) chan string {
	t.Helper()
	fakeCh := make(chan string, 8)
	orig := psiWatcherFunc
	t.Cleanup(func() { psiWatcherFunc = orig })
	psiWatcherFunc = func(_ context.Context, _ *os.File) <-chan string { return fakeCh }
	return fakeCh
}

func nextFrame(t *testing.T, dec *resize.StreamDecoder, timeout time.Duration) (resize.Sample, bool) {
	t.Helper()
	ch := make(chan resize.Sample, 1)
	go func() {
		s, err := dec.Next()
		if err == nil {
			ch <- s
		}
	}()
	select {
	case s := <-ch:
		return s, true
	case <-time.After(timeout):
		return resize.Sample{}, false
	}
}

func TestStreamFirstFrameIsHeartbeat(t *testing.T) {
	setupStreamFixtures(t)
	injectFakePSI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go serveStream(ctx, nil, server, nil)

	s, ok := nextFrame(t, resize.NewStreamDecoder(client), 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for first frame")
	}
	if s.Trigger != resize.TriggerHeartbeat {
		t.Errorf("first frame trigger = %q, want %q", s.Trigger, resize.TriggerHeartbeat)
	}
}

func TestStreamPSITriggerDelivered(t *testing.T) {
	setupStreamFixtures(t)
	fakeCh := injectFakePSI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go serveStream(ctx, nil, server, nil)
	dec := resize.NewStreamDecoder(client)

	if _, ok := nextFrame(t, dec, 2*time.Second); !ok {
		t.Fatal("timed out on first frame")
	}
	fakeCh <- resize.TriggerPSIMemory
	s, ok := nextFrame(t, dec, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for triggered frame")
	}
	if s.Trigger != resize.TriggerPSIMemory {
		t.Errorf("triggered frame trigger = %q, want %q", s.Trigger, resize.TriggerPSIMemory)
	}
}

func TestStreamCoalescing(t *testing.T) {
	setupStreamFixtures(t)
	fakeCh := injectFakePSI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go serveStream(ctx, nil, server, nil)
	dec := resize.NewStreamDecoder(client)

	if _, ok := nextFrame(t, dec, 2*time.Second); !ok {
		t.Fatal("timed out on first frame")
	}

	fakeCh <- resize.TriggerPSIMemory
	fakeCh <- resize.TriggerPSICPU

	s, ok := nextFrame(t, dec, 2*time.Second)
	if !ok {
		t.Fatal("timed out on triggered frame")
	}
	if s.Trigger != resize.TriggerPSIMemory {
		t.Errorf("frame trigger = %q, want psi_mem", s.Trigger)
	}

	s2, ok2 := nextFrame(t, dec, 300*time.Millisecond)
	if ok2 && s2.Trigger == resize.TriggerPSICPU {
		t.Error("second trigger was not coalesced: got psi_cpu frame within window")
	}
}

func TestStreamConnCloseNoLeak(t *testing.T) {
	setupStreamFixtures(t)
	injectFakePSI(t)
	ctx, cancel := context.WithCancel(context.Background())
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveStream(ctx, nil, server, nil)
	}()

	dec := resize.NewStreamDecoder(client)
	if _, ok := nextFrame(t, dec, 2*time.Second); !ok {
		t.Fatal("timed out on first frame")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("serveStream goroutine did not exit after ctx cancel")
	}
}

func TestStreamUnknownKind(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })

	go handleResizeConn(nil, server, nil)

	env := struct {
		V    int      `json:"v"`
		Kind string   `json:"kind"`
		P    struct{} `json:"payload"`
	}{V: 1, Kind: "sample.unknown", P: struct{}{}}
	if err := json.NewEncoder(client).Encode(env); err != nil {
		t.Fatalf("encode: %v", err)
	}

	var resp struct {
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Kind != "error" {
		t.Errorf("response kind = %q, want error", resp.Kind)
	}
}

func TestPSITriggerSpecs(t *testing.T) {
	wantLines := map[string]bool{
		"some 100000 500000":  true,
		"full 50000 500000":   true,
		"some 150000 1000000": true,
	}
	for _, s := range psiTriggerSpecs {
		if !wantLines[s.line] {
			t.Errorf("unexpected trigger line %q for resource %q", s.line, s.resource)
		}
	}
	if len(psiTriggerSpecs) != 3 {
		t.Errorf("want 3 trigger specs, got %d", len(psiTriggerSpecs))
	}
}

func TestOpenPSITriggersDegradation(t *testing.T) {
	orig := psiTriggerBasePath
	t.Cleanup(func() { psiTriggerBasePath = orig })
	psiTriggerBasePath = filepath.Join(t.TempDir(), "nonexistent")

	triggers := openPSITriggers(nil)
	if len(triggers) != 0 {
		t.Errorf("expected 0 triggers on missing path, got %d", len(triggers))
	}
}
