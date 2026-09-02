package cloudhypervisor

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestCappedConsoleWriter_WritesUpToCap verifies that bytes written to
// cappedConsoleWriter appear in the underlying file as long as the cumulative
// total is within maxConsoleSizeBytes.
//
// Mutation proof: stubbing w.f.Write to write nothing makes data disappear
// from the file and this test fails on the ReadFile assertion.
func TestCappedConsoleWriter_WritesUpToCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "console.log")
	w, err := newCappedConsoleWriter(path)
	if err != nil {
		t.Fatalf("newCappedConsoleWriter: %v", err)
	}
	msg := []byte("hello virtio-console\n")
	n, werr := w.Write(msg)
	if werr != nil {
		t.Fatalf("Write: %v", werr)
	}
	if n != len(msg) {
		t.Fatalf("Write returned %d, want %d", n, len(msg))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(data, msg) {
		t.Errorf("file contains %q, want %q", data, msg)
	}
}

// TestCappedConsoleWriter_TailRetained is the primary mutation-proof test for
// the S1-durable-console tail-retention requirement.
//
// It writes maxConsoleSizeBytes of "old" data followed by a distinct "tail"
// payload, then asserts:
//  1. The tail bytes are present in console.log (the most recent file).
//  2. The total on-disk size across console.log and console.log.1 stays at or
//     below 2×maxConsoleSizeBytes.
//
// This test FAILS against the old design (keep first N, discard rest): with
// that design, the tail write is silently dropped and bytes.Contains returns
// false.
//
// Mutation proof A — stub rotate() to be a no-op (keep old file, discard tail):
//
//	console.log stays full of 0xAA, tail bytes are not present → FAIL
//
// Mutation proof B — never call rotate (always discard once at cap):
//
//	identical failure: tail not present in any file → FAIL
func TestCappedConsoleWriter_TailRetained(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "console.log")
	w, err := newCappedConsoleWriter(path)
	if err != nil {
		t.Fatalf("newCappedConsoleWriter: %v", err)
	}

	// Write exactly maxConsoleSizeBytes of filler — triggers a rotation boundary.
	old := bytes.Repeat([]byte{0xAA}, maxConsoleSizeBytes)
	n, werr := w.Write(old)
	if werr != nil || n != len(old) {
		t.Fatalf("Write old data: n=%d err=%v", n, werr)
	}

	// Write a distinct tail payload that must appear in the most-recent file.
	tail := bytes.Repeat([]byte{0xBB}, 256)
	n, werr = w.Write(tail)
	if werr != nil || n != len(tail) {
		t.Fatalf("Write tail: n=%d err=%v", n, werr)
	}

	// console.log must contain the tail.
	current, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile console.log: %v", readErr)
	}
	if !bytes.Contains(current, tail) {
		t.Errorf("console.log does NOT contain the tail bytes — recent output is lost.\n"+
			"console.log size=%d; first 16 bytes: %x",
			len(current), current[:min(16, len(current))])
	}

	// Total on-disk (both files) must be ≤ 2 × maxConsoleSizeBytes.
	totalSize := int64(len(current))
	if fi, statErr := os.Stat(path + ".1"); statErr == nil {
		totalSize += fi.Size()
	}
	if totalSize > 2*int64(maxConsoleSizeBytes) {
		t.Errorf("total on-disk size %d exceeds 2×cap (%d)", totalSize, 2*int64(maxConsoleSizeBytes))
	}
}

// TestCappedConsoleWriter_BoundedGrowth verifies that writing significantly more
// than maxConsoleSizeBytes keeps total on-disk storage within 2×cap. This
// complements TestCappedConsoleWriter_TailRetained by exercising multiple
// rotation cycles.
func TestCappedConsoleWriter_BoundedGrowth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "console.log")
	w, err := newCappedConsoleWriter(path)
	if err != nil {
		t.Fatalf("newCappedConsoleWriter: %v", err)
	}

	// Write 3× the cap to force at least two rotations.
	chunk := bytes.Repeat([]byte{0xCC}, 4096)
	total := 3 * maxConsoleSizeBytes
	written := 0
	for written < total {
		n, werr := w.Write(chunk)
		if werr != nil || n != len(chunk) {
			t.Fatalf("Write: n=%d err=%v", n, werr)
		}
		written += n
	}

	currentSize := int64(0)
	if fi, statErr := os.Stat(path); statErr == nil {
		currentSize = fi.Size()
	}
	oldSize := int64(0)
	if fi, statErr := os.Stat(path + ".1"); statErr == nil {
		oldSize = fi.Size()
	}
	totalSize := currentSize + oldSize
	if totalSize > 2*int64(maxConsoleSizeBytes) {
		t.Errorf("total on-disk size %d exceeds 2×cap (%d) after 3× writes",
			totalSize, 2*int64(maxConsoleSizeBytes))
	}
}

// TestCappedConsoleWriter_AlwaysReturnsSuccess verifies that Write always
// returns (len(p), nil) even when the file is nil (error or rotation-failed
// state), so the drain goroutine never interprets a write failure as a signal
// to stop and lets the pipe buffer fill.
//
// Mutation proof: returning the real error from w.f.Write would cause
// io.Copy (the exec goroutine) to stop and let the pipe buffer fill — blocking CH.
func TestCappedConsoleWriter_AlwaysReturnsSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "console.log")
	w, err := newCappedConsoleWriter(path)
	if err != nil {
		t.Fatalf("newCappedConsoleWriter: %v", err)
	}

	// Simulate a write-error / rotation-failed state by closing and nil-ing the file.
	w.mu.Lock()
	_ = w.f.Close()
	w.f = nil
	w.mu.Unlock()

	msg := []byte("after error: must still return success")
	n, werr := w.Write(msg)
	if werr != nil {
		t.Errorf("Write after nil-f: got error %v, want nil", werr)
	}
	if n != len(msg) {
		t.Errorf("Write after nil-f: n=%d, want %d", n, len(msg))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
