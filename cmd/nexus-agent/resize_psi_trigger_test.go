//go:build linux

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	want := map[string][2]string{
		"some 100000 500000":  {"memory", "some 200000 2000000"},
		"full 50000 500000":   {"memory", "full 100000 2000000"},
		"some 150000 1000000": {"cpu", "some 300000 2000000"},
	}
	for _, s := range psiTriggerSpecs {
		w, ok := want[s.fast]
		if !ok {
			t.Errorf("unexpected fast trigger line %q for resource %q", s.fast, s.resource)
			continue
		}
		if s.resource != w[0] || s.slow != w[1] {
			t.Errorf("spec %q: resource/slow = %q/%q, want %q/%q", s.fast, s.resource, s.slow, w[0], w[1])
		}
	}
	if len(psiTriggerSpecs) != 3 {
		t.Errorf("want 3 trigger specs, got %d", len(psiTriggerSpecs))
	}
}

// The kernel's psi_write copies the payload into a fixed buffer and
// unconditionally NUL-terminates it at buf[nbytes-1], dropping the last
// byte written. A line without a trailing newline therefore loses its last
// window digit ("some 100000 500000" -> "some 100000 50000") and is
// rejected with EINVAL.
func TestOpenPSITriggerWritesNewlineTerminatedLine(t *testing.T) {
	orig := psiTriggerBasePath
	t.Cleanup(func() { psiTriggerBasePath = orig })
	psiTriggerBasePath = t.TempDir()
	path := filepath.Join(psiTriggerBasePath, "memory")
	writeTestFile(t, path, "")

	const line = "some 100000 500000"
	fd, err := openPSITrigger("memory", line)
	if err != nil {
		t.Fatalf("openPSITrigger: %v", err)
	}
	unix.Close(fd)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != line+"\n" {
		t.Errorf("bytes written to %s = %q, want %q (newline-terminated)", path, got, line+"\n")
	}
}

func TestOpenPSITriggersDegradationLogsErrnoAndAttempt(t *testing.T) {
	orig := psiTriggerBasePath
	t.Cleanup(func() { psiTriggerBasePath = orig })
	psiTriggerBasePath = t.TempDir()
	if err := os.Mkdir(filepath.Join(psiTriggerBasePath, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(psiTriggerBasePath, "cpu"), 0o755); err != nil {
		t.Fatal(err)
	}
	con, err := os.CreateTemp(t.TempDir(), "console")
	if err != nil {
		t.Fatal(err)
	}
	defer con.Close()

	triggers := openPSITriggers(con)
	if len(triggers) != 0 {
		t.Fatalf("expected 0 triggers, got %d", len(triggers))
	}
	log, err := os.ReadFile(con.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"EISDIR", "fast", "slow", "some 100000 500000", "some 200000 2000000", "heartbeat-only"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("degradation log missing %q:\n%s", want, log)
		}
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
