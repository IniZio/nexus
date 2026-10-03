//go:build linux

package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseProcStatCPU(t *testing.T) {
	in := "cpu  100 0 50 800 40 5 5 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10\nintr 1\n"
	busy, total, ok := parseProcStatCPU(in)
	if !ok || total != 1000 || busy != 160 {
		t.Fatalf("busy=%d total=%d ok=%v; want 160/1000/true", busy, total, ok)
	}
	if _, _, ok := parseProcStatCPU("cpu0 1 2 3 4 5\n"); ok {
		t.Fatal("per-cpu line parsed as aggregate")
	}
	if _, _, ok := parseProcStatCPU("cpu  a b c d e\n"); ok {
		t.Fatal("garbage parsed")
	}
}

func TestCPUBusySamplerDelta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stat")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := &cpuBusyState{}
	t0 := time.Unix(1000, 0)

	write("cpu  100 0 100 700 100 0 0 0\n")
	if _, ok := c.sample(path, t0); ok {
		t.Fatal("first poll must be unsupported")
	}
	// +300 busy, +100 idle, +100 iowait => 300/500
	write("cpu  300 0 200 800 200 0 0 0\n")
	f, ok := c.sample(path, t0.Add(5*time.Second))
	if !ok || math.Abs(f-0.6) > 1e-9 {
		t.Fatalf("frac=%v ok=%v; want 0.6", f, ok)
	}
	// within min interval: cached
	write("cpu  900 0 200 800 200 0 0 0\n")
	f, ok = c.sample(path, t0.Add(5*time.Second+10*time.Millisecond))
	if !ok || math.Abs(f-0.6) > 1e-9 {
		t.Fatalf("burst call frac=%v ok=%v; want cached 0.6", f, ok)
	}
	// counters backwards => unsupported
	write("cpu  1 0 1 1 1 0 0 0\n")
	if _, ok := c.sample(path, t0.Add(20*time.Second)); ok {
		t.Fatal("backwards counters must be unsupported")
	}
	if _, ok := c.sample(filepath.Join(t.TempDir(), "missing"), t0.Add(40*time.Second)); ok {
		t.Fatal("missing file must be unsupported")
	}
}
