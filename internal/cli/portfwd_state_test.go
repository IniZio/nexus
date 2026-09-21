//go:build linux

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteForwardsStateAtomic_ConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	s0 := &ForwardsState{UpdatedAt: time.Now()}
	if err := WriteForwardsStateAtomic(dir, s0); err != nil {
		t.Fatal(err)
	}
	var failures int64
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			b, err := os.ReadFile(filepath.Join(dir, ForwardsStateFile))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				continue
			}
			var s ForwardsState
			if json.Unmarshal(b, &s) != nil {
				atomic.AddInt64(&failures, 1)
			}
		}
	}()
	for i := 0; i < 500; i++ {
		s := &ForwardsState{UpdatedAt: time.Now()}
		if err := WriteForwardsStateAtomic(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	if failures > 0 {
		t.Errorf("atomic write: %d torn reads observed", failures)
	}
}

func TestNaiveWriteProducesTornRead(t *testing.T) {
	dir := t.TempDir()
	s0 := &ForwardsState{UpdatedAt: time.Now(), Forwards: []PortForward{{Port: 3000, Status: "live"}}}
	if err := WriteForwardsStateAtomic(dir, s0); err != nil {
		t.Fatal(err)
	}
	afterTruncate := make(chan struct{})
	type readResult struct{ err error }
	resultCh := make(chan readResult, 1)
	go func() {
		<-afterTruncate
		b, _ := os.ReadFile(filepath.Join(dir, ForwardsStateFile))
		var s ForwardsState
		resultCh <- readResult{err: json.Unmarshal(b, &s)}
	}()
	path := filepath.Join(dir, ForwardsStateFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0600)
	if err != nil {
		t.Fatal(err)
	}
	close(afterTruncate)
	res := <-resultCh
	f.Close()
	if res.err == nil {
		t.Fatal("proof harness broken — torn read not observed; empty file parsed without error")
	}
}

func TestRenderRow_LiveWithHostPort(t *testing.T) {
	fwd := PortForward{Port: 3000, HostPort: 41234, Status: PFStatusLive, ConfirmedAt: time.Now()}
	row := renderRow(fwd)
	if !strings.Contains(row, "3000 → 127.0.0.1:41234") {
		t.Errorf("expected guest→host mapping in LIVE row, got: %q", row)
	}
	if strings.Contains(row, "127.0.0.1:3000") {
		t.Errorf("LIVE row with HostPort must not show guest port as URL target, got: %q", row)
	}
}

func TestRenderRow_LiveWithoutHostPort(t *testing.T) {
	fwd := PortForward{Port: 3000, Status: PFStatusLive, ConfirmedAt: time.Now()}
	row := renderRow(fwd)
	if !strings.Contains(row, "127.0.0.1:3000") {
		t.Errorf("LIVE row without HostPort must show guest port, got: %q", row)
	}
}

func TestStaleStateDetected(t *testing.T) {
	dir := t.TempDir()
	s := &ForwardsState{
		WrittenBy: "test",
		UpdatedAt: time.Now().Add(-6 * time.Minute),
		Forwards:  []PortForward{{Port: 3000, Status: PFStatusLive, ConfirmedAt: time.Now()}},
	}
	if err := WriteForwardsStateAtomic(dir, s); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := LoadForwardsState(dir)
	if err != nil || !ok {
		t.Fatalf("LoadForwardsState: ok=%v err=%v", ok, err)
	}
	out := RenderPortsPane(loaded, 0, time.Now())
	if !strings.Contains(out, "WARNING") {
		t.Errorf("expected WARNING in stale render, got: %q", out)
	}
}
