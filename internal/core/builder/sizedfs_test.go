package builder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	gofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tonistiigi/fsutil"
)

const (
	sizedfsFileSize = 10 * 1024 * 1024 // 10 MiB
	sizedfsFileName = "bigfile"
	sizedfsHalfSize = 4 * 1024 * 1024 // 4 MiB — cut point
)

// makeSizedFSTempDir writes a single 10 MiB zero-filled file and returns the
// dir path and the real inner FS.
func makeSizedFSTempDir(t *testing.T) (string, fsutil.FS) {
	t.Helper()
	dir := t.TempDir()
	data := make([]byte, sizedfsFileSize)
	if err := os.WriteFile(filepath.Join(dir, sizedfsFileName), data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	inner, err := fsutil.NewFS(dir)
	if err != nil {
		t.Fatalf("fsutil.NewFS: %v", err)
	}
	return dir, inner
}

// cutFS delegates Walk to the real FS but truncates every Open to return at
// most sizedfsHalfSize bytes before signalling io.EOF — simulating a
// truncated source file.
type cutFS struct {
	real fsutil.FS
}

func (c *cutFS) Walk(ctx context.Context, target string, fn gofs.WalkDirFunc) error {
	return c.real.Walk(ctx, target, fn)
}

func (c *cutFS) Open(path string) (io.ReadCloser, error) {
	rc, err := c.real.Open(path)
	if err != nil {
		return nil, err
	}
	return &cutReader{ReadCloser: rc, remaining: sizedfsHalfSize}, nil
}

type cutReader struct {
	io.ReadCloser
	remaining int
}

func (r *cutReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= n
	if r.remaining <= 0 && err == nil {
		err = io.EOF
	}
	return n, err
}

// longFS delegates Walk to the real FS but extends every Open by one extra
// zero byte after the real content — simulating a corrupted stat where the
// declared size is smaller than the actual byte stream.
type longFS struct {
	real fsutil.FS
}

func (l *longFS) Walk(ctx context.Context, target string, fn gofs.WalkDirFunc) error {
	return l.real.Walk(ctx, target, fn)
}

func (l *longFS) Open(path string) (io.ReadCloser, error) {
	rc, err := l.real.Open(path)
	if err != nil {
		return nil, err
	}
	return &longReader{ReadCloser: rc}, nil
}

type longReader struct {
	io.ReadCloser
	extraSent bool
}

func (r *longReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err == io.EOF && !r.extraSent && n == 0 {
		// Real content exhausted; deliver one extra byte.
		r.extraSent = true
		if len(p) > 0 {
			p[0] = 0x00
			return 1, nil
		}
	}
	if err == io.EOF && !r.extraSent {
		// Real content finished in this Read; tack on extra byte.
		r.extraSent = true
		if n < len(p) {
			p[n] = 0x00
			n++
		}
		return n, nil // suppress EOF so caller reads again and gets the extra
	}
	return n, err
}

// TestSizeVerifiedFS_TruncatedRead proves that a read truncated at 4 MiB from
// a 10 MiB file returns a non-nil error whose message names the file and
// both the truncated byte count and the full expected size.
// nopCancelCause is a no-op CancelCauseFunc for tests that only verify the
// read-error path and do not need context cancellation wired up.
func nopCancelCause(error) {}

func TestSizeVerifiedFS_TruncatedRead(t *testing.T) {
	_, real := makeSizedFSTempDir(t)
	cut := &cutFS{real: real}
	wrapped := newSizeVerifiedFS(cut, nopCancelCause)

	ctx := context.Background()
	if err := wrapped.Walk(ctx, "", func(path string, d gofs.DirEntry, err error) error {
		return err
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	rc, err := wrapped.Open(sizedfsFileName)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	_, readErr := io.ReadAll(rc)
	if readErr == nil {
		t.Fatal("expected non-nil error for truncated read, got nil")
	}

	msg := readErr.Error()
	t.Logf("truncated-read error: %s", msg)

	if !strings.Contains(msg, sizedfsFileName) {
		t.Errorf("error message does not mention filename %q: %s", sizedfsFileName, msg)
	}
	gotStr := "4194304"
	if !strings.Contains(msg, gotStr) {
		t.Errorf("error message does not mention truncated byte count %s: %s", gotStr, msg)
	}
	expectedStr := "10485760"
	if !strings.Contains(msg, expectedStr) {
		t.Errorf("error message does not mention full expected size %s: %s", expectedStr, msg)
	}
}

// TestSizeVerifiedFS_CleanFile proves that a full read of a correctly-sized
// file succeeds with no error and delivers all bytes.
func TestSizeVerifiedFS_CleanFile(t *testing.T) {
	_, real := makeSizedFSTempDir(t)
	wrapped := newSizeVerifiedFS(real, nopCancelCause)

	ctx := context.Background()
	if err := wrapped.Walk(ctx, "", func(path string, d gofs.DirEntry, err error) error {
		return err
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	rc, err := wrapped.Open(sizedfsFileName)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: unexpected error on clean file: %v", err)
	}
	if len(data) != sizedfsFileSize {
		t.Errorf("ReadAll returned %d bytes, want %d", len(data), sizedfsFileSize)
	}
	if !bytes.Equal(data, make([]byte, sizedfsFileSize)) {
		t.Error("ReadAll content mismatch: expected all-zero bytes")
	}
}

// TestSizeVerifiedFS_OverRead proves that a reader that delivers more bytes
// than the stat-declared size returns a non-nil error indicating an over-read.
func TestSizeVerifiedFS_OverRead(t *testing.T) {
	_, real := makeSizedFSTempDir(t)
	long := &longFS{real: real}
	wrapped := newSizeVerifiedFS(long, nopCancelCause)

	ctx := context.Background()
	if err := wrapped.Walk(ctx, "", func(path string, d gofs.DirEntry, err error) error {
		return err
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	rc, err := wrapped.Open(sizedfsFileName)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	_, readErr := io.ReadAll(rc)
	if readErr == nil {
		t.Fatal("expected non-nil error for over-read, got nil")
	}

	msg := readErr.Error()
	t.Logf("over-read error: %s", msg)

	if !strings.Contains(msg, sizedfsFileName) {
		t.Errorf("error message does not mention filename %q: %s", sizedfsFileName, msg)
	}
	// sizedfs.go uses "over-read" in the error string.
	if !strings.Contains(msg, "over-read") && !strings.Contains(msg, "over") {
		t.Errorf("error message does not indicate over-read: %s", msg)
	}
}

// TestSizeVerifiedFSMutationProof proves the mutation that the guard catches:
// reading through the raw inner FS (bypassing the wrapper) silently succeeds
// even though the underlying reader truncates at 4 MiB. This confirms that
// without the wrapper the truncation is invisible to callers.
func TestSizeVerifiedFSMutationProof(t *testing.T) {
	_, real := makeSizedFSTempDir(t)
	cut := &cutFS{real: real}

	// Open directly through the cut FS — no sizeVerifiedFS wrapper.
	rc, err := cut.Open(sizedfsFileName)
	if err != nil {
		t.Fatalf("cut.Open: %v", err)
	}
	defer rc.Close()

	_, readErr := io.ReadAll(rc)
	// MUTATION: bypassing the guard — a truncated read via the raw FS returns
	// no error (io.ReadAll treats io.EOF as success).
	if readErr != nil {
		t.Fatalf("expected nil error bypassing guard, got: %v", readErr)
	}
	t.Log("MUTATION: bypass guard — got no error on truncated read (expected)")
}

// TestSizeVerifiedFS_CancelCause proves that the cancel+cause plumbing fires
// on the first size violation without a live buildkitd. It verifies:
//   - context.Cause(ctx) returns the descriptive sizedfs error after Read
//   - sfs.Err() returns the same error
//   - the context is Done (cancelled)
func TestSizeVerifiedFS_CancelCause(t *testing.T) {
	_, real := makeSizedFSTempDir(t)
	cut := &cutFS{real: real}

	ctx, cancelCause := context.WithCancelCause(context.Background())
	defer cancelCause(nil)

	sfs := newSizeVerifiedFS(cut, cancelCause)

	walkCtx := context.Background()
	if err := sfs.Walk(walkCtx, "", func(_ string, _ gofs.DirEntry, err error) error {
		return err
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	rc, err := sfs.Open(sizedfsFileName)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	_, readErr := io.ReadAll(rc)
	if readErr == nil {
		t.Fatal("expected non-nil error for truncated read, got nil")
	}
	t.Logf("Read error: %v", readErr)

	// cancelCause must have fired — ctx should be done.
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Fatal("context not cancelled after size violation")
	}

	// context.Cause must return our descriptive error, not context.Canceled.
	cause := context.Cause(ctx)
	if cause == nil {
		t.Fatal("context.Cause returned nil, expected descriptive sizedfs error")
	}
	t.Logf("context.Cause: %v", cause)
	if cause.Error() != readErr.Error() {
		t.Errorf("context.Cause %q != readErr %q", cause.Error(), readErr.Error())
	}

	// sfs.Err() must return the same error.
	fsErr := sfs.Err()
	if fsErr == nil {
		t.Fatal("sfs.Err() returned nil, expected descriptive error")
	}
	if fsErr.Error() != readErr.Error() {
		t.Errorf("sfs.Err() %q != readErr %q", fsErr.Error(), readErr.Error())
	}
}

// errOpenFS delegates Walk to the real FS but returns an error from Open —
// simulating a host file that disappears or becomes unreadable after Walk.
// This is the zero-byte-file fault: fsutil's sendFile skips the copy on an
// Open error and sends the terminating PACKET_DATA, writing a zero-byte file.
type errOpenFS struct {
	real    fsutil.FS
	openErr error // returned for every Open call
}

func (e *errOpenFS) Walk(ctx context.Context, target string, fn gofs.WalkDirFunc) error {
	return e.real.Walk(ctx, target, fn)
}

func (e *errOpenFS) Open(path string) (io.ReadCloser, error) {
	return nil, e.openErr
}

// TestSizeVerifiedFS_OpenError proves that an inner Open failure fires
// cancelCause so the Solve context is torn down immediately instead of letting
// fsutil emit a zero-byte file silently. The unit variant needs no buildkitd.
func TestSizeVerifiedFS_OpenError(t *testing.T) {
	_, real := makeSizedFSTempDir(t)
	openErr := fmt.Errorf("injected open failure")
	errFS := &errOpenFS{real: real, openErr: openErr}

	ctx, cancelCause := context.WithCancelCause(context.Background())
	defer cancelCause(nil)

	sfs := newSizeVerifiedFS(errFS, cancelCause)

	if err := sfs.Walk(context.Background(), "", func(_ string, _ gofs.DirEntry, err error) error {
		return err
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	_, openCallErr := sfs.Open(sizedfsFileName)
	if openCallErr == nil {
		t.Fatal("expected non-nil error from Open, got nil")
	}
	t.Logf("Open error: %v", openCallErr)

	// The error must mention the file path.
	if !strings.Contains(openCallErr.Error(), sizedfsFileName) {
		t.Errorf("error does not mention filename %q: %s", sizedfsFileName, openCallErr)
	}

	// cancelCause must have fired — ctx should be done.
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Fatal("context not cancelled after Open failure")
	}

	// context.Cause must equal sfs.Err().
	cause := context.Cause(ctx)
	if cause == nil {
		t.Fatal("context.Cause returned nil")
	}
	t.Logf("context.Cause: %v", cause)

	fsErr := sfs.Err()
	if fsErr == nil {
		t.Fatal("sfs.Err() returned nil")
	}
	if cause.Error() != fsErr.Error() {
		t.Errorf("context.Cause %q != sfs.Err() %q", cause.Error(), fsErr.Error())
	}
}

// TestSizeVerifiedFS_OpenErrorMutationProof proves what happens WITHOUT the
// guard: an inner Open failure returns an error but nothing cancels the Solve
// context. fsutil's sendFile sees `err != nil`, skips the copy, and sends the
// terminating PACKET_DATA — writing a zero-byte file with no error anywhere.
//
// This test PASSES asserting the unsafe behaviour; TestSizeVerifiedFS_OpenError
// FAILS if the noteErr call is removed from sizeVerifiedFS.Open (context not
// cancelled, sfs.Err() nil).
func TestSizeVerifiedFS_OpenErrorMutationProof(t *testing.T) {
	_, real := makeSizedFSTempDir(t)
	errFS := &errOpenFS{real: real, openErr: fmt.Errorf("injected open failure")}

	// MUTATION: use errFS directly — no wrapper, no noteErr, no cancel.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, callErr := errFS.Open(sizedfsFileName)
	if callErr == nil {
		t.Fatal("expected non-nil error from raw Open, got nil")
	}

	// Without the wrapper the context is never cancelled.
	select {
	case <-ctx.Done():
		t.Error("UNEXPECTED: context was cancelled without the wrapper")
	default:
		// expected: context not cancelled
	}
	t.Logf("MUTATION: raw Open returned %v but context NOT cancelled — Solve completes with zero-byte file", callErr)
}

// TestBuildLocalMounts_AllWrapped is the CI-visible regression guard for the
// buildLocalMounts seam: it asserts that the returned map has exactly the three
// required keys and that every value is a *sizeVerifiedFS whose violation fires
// the shared set.Err() and cancels the context. No buildkitd required.
//
// If a future edit changes buildLocalMounts to pass a raw FS for any entry,
// the "violation-fires-<key>" sub-tests catch it in plain go test.
func TestBuildLocalMounts_AllWrapped(t *testing.T) {
	const (
		keyContext = "context"
		keyDF      = "dockerfile"
		keyAgent   = "nexus3agent"
	)
	wantKeys := []string{keyContext, keyDF, keyAgent}

	// ── shape: exactly 3 keys, all typed *sizeVerifiedFS ─────────────────────
	t.Run("map-shape", func(t *testing.T) {
		_, real := makeSizedFSTempDir(t)
		_, cc := context.WithCancelCause(context.Background())
		set := newSizeVerifiedSet(cc)

		mounts := buildLocalMounts(set, real, real, real)
		if got := len(mounts); got != len(wantKeys) {
			t.Errorf("len(mounts) = %d, want %d", got, len(wantKeys))
		}
		for _, k := range wantKeys {
			v, ok := mounts[k]
			if !ok {
				t.Errorf("missing key %q", k)
				continue
			}
			if _, isSFS := v.(*sizeVerifiedFS); !isSFS {
				t.Errorf("mounts[%q] type = %T, want *sizeVerifiedFS", k, v)
			}
		}
	})

	// ── violation through each mount fires shared set.Err() ──────────────────
	for idx, k := range wantKeys {
		idx, k := idx, k
		t.Run("violation-fires-"+k, func(t *testing.T) {
			ctx, cancelCause := context.WithCancelCause(context.Background())
			defer cancelCause(nil)
			set := newSizeVerifiedSet(cancelCause)

			// Position idx uses cutFS; the other two are real FSes.
			fses := [3]fsutil.FS{}
			for i := range fses {
				_, r := makeSizedFSTempDir(t)
				if i == idx {
					fses[i] = &cutFS{real: r}
				} else {
					fses[i] = r
				}
			}
			mounts := buildLocalMounts(set, fses[0], fses[1], fses[2])

			v := mounts[k]
			if err := v.Walk(context.Background(), "", func(_ string, _ gofs.DirEntry, err error) error {
				return err
			}); err != nil {
				t.Fatalf("Walk: %v", err)
			}
			rc, err := v.Open(sizedfsFileName)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer rc.Close()
			_, _ = io.ReadAll(rc)

			if set.Err() == nil {
				t.Errorf("set.Err() nil after violation through %q — mount not wrapped", k)
			}
			select {
			case <-ctx.Done():
			default:
				t.Errorf("context not cancelled after violation through %q", k)
			}
		})
	}

	// ── mutation: raw nexus3agent bypasses set.Err() ──────────────────────────
	// Asserts the dangerous behaviour that buildLocalMounts prevents. If
	// buildLocalMounts were changed to leave nexus3agent raw, the sub-tests
	// above would t.Errorf("set.Err() nil…") — caught in plain go test.
	t.Run("mutation-raw-nexus3agent", func(t *testing.T) {
		ctx, cancelCause := context.WithCancelCause(context.Background())
		defer cancelCause(nil)
		set := newSizeVerifiedSet(cancelCause)

		_, real0 := makeSizedFSTempDir(t)
		_, real1 := makeSizedFSTempDir(t)
		_, real2 := makeSizedFSTempDir(t)

		// MUTATION: nexus3agent is raw — simulates a regression in buildLocalMounts.
		mounts := map[string]fsutil.FS{
			keyContext: set.Wrap(real0),
			keyDF:      set.Wrap(real1),
			keyAgent:   &cutFS{real: real2}, // deliberately not wrapped
		}

		agentMount := mounts[keyAgent]
		if err := agentMount.Walk(context.Background(), "", func(_ string, _ gofs.DirEntry, err error) error {
			return err
		}); err != nil {
			t.Fatalf("Walk: %v", err)
		}
		rc, err := agentMount.Open(sizedfsFileName)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer rc.Close()
		_, _ = io.ReadAll(rc)

		if set.Err() != nil {
			t.Errorf("MUTATION FAIL: set.Err() = %v, want nil", set.Err())
		}
		select {
		case <-ctx.Done():
			t.Error("MUTATION FAIL: context cancelled by raw nexus3agent violation")
		default:
		}
		t.Log("MUTATION: raw nexus3agent bypassed set — buildLocalMounts wrapping is the only guard")
	})
}

// TestSizeVerifiedSet_SharedCancel proves that a violation in ANY of the three
// wrapped mounts sets the shared Err() and cancels the context, and that an
// unwrapped mount's violation does not reach the set (mutation sub-test).
//
// This test requires no buildkitd: it drives Walk + Open directly.
func TestSizeVerifiedSet_SharedCancel(t *testing.T) {
	// Sub-tests: violation in each of the three mounts must cancel the context
	// and surface via set.Err().
	for _, tc := range []struct {
		name    string
		violate int // which mount index has the cutFS injected
	}{
		{"mount-0-context", 0},
		{"mount-1-dockerfile", 1},
		{"mount-2-agent", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancelCause := context.WithCancelCause(context.Background())
			defer cancelCause(nil)

			set := newSizeVerifiedSet(cancelCause)

			// Three independent temp dirs, each with one 10 MiB file.
			// The mount at index tc.violate uses cutFS (truncates at 4 MiB).
			fses := make([]fsutil.FS, 3)
			for i := range fses {
				_, real := makeSizedFSTempDir(t)
				if i == tc.violate {
					fses[i] = &cutFS{real: real}
				} else {
					fses[i] = real
				}
			}

			wrapped := [3]*sizeVerifiedFS{
				set.Wrap(fses[0]),
				set.Wrap(fses[1]),
				set.Wrap(fses[2]),
			}

			// Walk all three to register declared sizes.
			for _, w := range wrapped {
				if err := w.Walk(context.Background(), "", func(_ string, _ gofs.DirEntry, err error) error {
					return err
				}); err != nil {
					t.Fatalf("Walk: %v", err)
				}
			}

			// Trigger the violation by reading through the violating mount.
			rc, err := wrapped[tc.violate].Open(sizedfsFileName)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer rc.Close()
			_, _ = io.ReadAll(rc)

			// set.Err() must be non-nil — violation reached the shared slot.
			if set.Err() == nil {
				t.Errorf("set.Err() is nil after violation in mount %d, want non-nil", tc.violate)
			} else {
				t.Logf("set.Err(): %v", set.Err())
			}

			// The context must be cancelled.
			select {
			case <-ctx.Done():
				// expected
			default:
				t.Errorf("context not cancelled after violation in mount %d", tc.violate)
			}
		})
	}

	// Mutation: an unwrapped mount's violation does NOT reach the set.
	// This proves that wrapping is necessary — dropping one mount from the set
	// means its violation is invisible to the shared cancel-cause.
	t.Run("mutation-unwrapped-mount", func(t *testing.T) {
		ctx, cancelCause := context.WithCancelCause(context.Background())
		defer cancelCause(nil)

		set := newSizeVerifiedSet(cancelCause)

		// Wrap only mounts 0 and 1 — mount 2 is NOT wrapped (raw cutFS).
		_, real0 := makeSizedFSTempDir(t)
		_, real1 := makeSizedFSTempDir(t)
		_, real2 := makeSizedFSTempDir(t)

		w0 := set.Wrap(real0)
		w1 := set.Wrap(real1)
		rawCut := &cutFS{real: real2} // deliberately not wrapped

		// Walk all three.
		for _, w := range []fsutil.FS{w0, w1, rawCut} {
			if err := w.Walk(context.Background(), "", func(_ string, _ gofs.DirEntry, err error) error {
				return err
			}); err != nil {
				t.Fatalf("Walk: %v", err)
			}
		}

		// Read from the unwrapped cut mount — violation occurs but set is unaware.
		rc, err := rawCut.Open(sizedfsFileName)
		if err != nil {
			t.Fatalf("rawCut.Open: %v", err)
		}
		defer rc.Close()
		_, _ = io.ReadAll(rc) // short read: io.ReadAll treats EOF as success

		// MUTATION: set.Err() must be nil because the violation bypassed the set.
		if set.Err() != nil {
			t.Errorf("MUTATION FAIL: set.Err() = %v, want nil — unwrapped violation must not reach set", set.Err())
		}
		// Context must NOT be cancelled.
		select {
		case <-ctx.Done():
			t.Error("MUTATION FAIL: context was cancelled by unwrapped mount's violation")
		default:
			// expected
		}
		t.Log("MUTATION: unwrapped mount violation did not reach set (expected — drop one from set → violation invisible)")
	})
}
