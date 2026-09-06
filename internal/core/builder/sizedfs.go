package builder

import (
	"context"
	"fmt"
	"io"
	gofs "io/fs"
	"sync"

	"github.com/tonistiigi/fsutil"
)

// sizeVerifiedFS wraps an fsutil.FS and detects truncated file reads before
// they leave our binary. The fsutil sender calls Walk (conveying each file's
// Stat) before it calls Open for the same path, so we can record the declared
// size in Walk and enforce it in Open.
//
// On the first violation, noteErrFn is fired with a descriptive error so
// the context passed to bk.Solve tears down immediately. Without this,
// sendFile (send.go:136-147) exits without emitting the terminating
// PACKET_DATA, the daemon receiver holds that file's pipe open, and the Solve
// call blocks until the client deadline — making the corruption look like a
// flaky timeout rather than a detected fault (D-8).
//
// noteErrFn and errFn are either standalone closures (newSizeVerifiedFS) or
// methods on a shared sizeVerifiedSet (sizeVerifiedSet.Wrap). Either way the
// caller calls Err() to retrieve the first violation and only the external
// cancel-cause + error-slot wiring differs.
type sizeVerifiedFS struct {
	inner      fsutil.FS
	noteErrFn  func(error)  // fires cancel + records first err
	errFn      func() error // returns the first recorded err
	mu         sync.Mutex
	expected   map[string]int64 // path → declared byte count from Walk
}

// newSizeVerifiedFS returns an FS that rejects truncated or over-length reads.
// cancelCause must be the func returned by context.WithCancelCause on the
// context that will be passed to bk.Solve; the first violation fires it so
// Solve returns within seconds rather than at its deadline.
//
// It is a thin wrapper: newSizeVerifiedSet(cancelCause).Wrap(inner).
// All error plumbing lives in sizeVerifiedSet — one path, no parallel state.
func newSizeVerifiedFS(inner fsutil.FS, cancelCause context.CancelCauseFunc) *sizeVerifiedFS {
	return newSizeVerifiedSet(cancelCause).Wrap(inner)
}

// Err returns the first size-violation error recorded by this FS, or nil.
// Call it after bk.Solve returns to recover the descriptive error in preference
// to the context-cancellation error buildkit will have reported.
func (s *sizeVerifiedFS) Err() error { return s.errFn() }

// noteErr records the first violation. For standalone FSes the call fires
// cancelCause; for set members it delegates to the set's shared slot.
func (s *sizeVerifiedFS) noteErr(err error) { s.noteErrFn(err) }

// sizeVerifiedSet groups multiple sizeVerifiedFS instances under a single
// shared cancel-cause and error slot. Any violation in any member immediately
// cancels the Solve context and is retrievable via Err.
//
// Use newSizeVerifiedSet + Wrap when all local mounts (context, dockerfile,
// nexus3agent) must share the same cancellation: the first violation in any
// mount tears down the whole Solve and surfaces a descriptive error naming the
// violating file path.
type sizeVerifiedSet struct {
	cancelCause context.CancelCauseFunc
	mu          sync.Mutex
	firstErr    error
}

// newSizeVerifiedSet returns a set whose Wrap method vends sizeVerifiedFS
// instances sharing the same cancel-cause and error slot.
func newSizeVerifiedSet(cancelCause context.CancelCauseFunc) *sizeVerifiedSet {
	return &sizeVerifiedSet{cancelCause: cancelCause}
}

// noteErr is the shared error recorder: records the first violation and fires
// cancelCause so the Solve context is torn down immediately.
func (s *sizeVerifiedSet) noteErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstErr == nil {
		s.firstErr = err
		s.cancelCause(err)
	}
}

// Err returns the first violation error from any member, or nil.
func (s *sizeVerifiedSet) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstErr
}

// Wrap adds inner to the set and returns a sizeVerifiedFS that delegates
// violations to this set's shared cancel-cause and error slot.
func (s *sizeVerifiedSet) Wrap(inner fsutil.FS) *sizeVerifiedFS {
	sfs := &sizeVerifiedFS{
		inner:     inner,
		noteErrFn: s.noteErr,
		errFn:     s.Err,
		expected:  make(map[string]int64),
	}
	return sfs
}

// Walk delegates to the inner FS and records the declared size of every
// regular file so Open can enforce it. Directory and symlink entries are
// passed through without recording (Open is never called for them).
func (s *sizeVerifiedFS) Walk(ctx context.Context, target string, fn gofs.WalkDirFunc) error {
	return s.inner.Walk(ctx, target, func(path string, d gofs.DirEntry, err error) error {
		if err == nil && d != nil && !d.IsDir() && d.Type()&gofs.ModeSymlink == 0 {
			if fi, infoErr := d.Info(); infoErr == nil {
				s.mu.Lock()
				s.expected[path] = fi.Size()
				s.mu.Unlock()
			}
		}
		return fn(path, d, err)
	})
}

// Open delegates to the inner FS and wraps the reader with a byte counter.
// Both error paths here cancel the Solve context immediately via noteErr:
//
//   - Path not in Walk: mismatch between Walk and Open lists; we cannot know
//     the declared size, so we cannot guard the read.
//   - Inner Open failure: fsutil's sendFile (send.go:136-147) checks
//     `if err == nil` around the copy loop; an Open error silently skips the
//     copy and still emits the terminating empty PACKET_DATA, causing the
//     receiver to write a zero-byte file with no error returned. Cancelling
//     the Solve context tears down the build before that file is committed.
//
// Size-mismatch errors from the reader are handled by sizeVerifiedReader.Read.
func (s *sizeVerifiedFS) Open(path string) (io.ReadCloser, error) {
	s.mu.Lock()
	size, known := s.expected[path]
	s.mu.Unlock()
	if !known {
		e := fmt.Errorf("sizedfs: Open(%q): path not registered by Walk — walk/open mismatch", path)
		s.noteErr(e)
		return nil, e
	}
	rc, err := s.inner.Open(path)
	if err != nil {
		// Inner Open failure: fsutil would emit a zero-byte file silently.
		e := fmt.Errorf("sizedfs: Open(%q): inner open failed (fsutil would emit a zero-byte file): %w", path, err)
		s.noteErr(e)
		return nil, e
	}
	return &sizeVerifiedReader{
		ReadCloser: rc,
		path:       path,
		expected:   size,
		noteErr:    s.noteErr,
	}, nil
}

// sizeVerifiedReader counts bytes as they are read. On EOF short of expected,
// or on reads past expected, it calls noteErr (which fires cancelCause on the
// Solve context) and also returns a descriptive non-EOF error.
type sizeVerifiedReader struct {
	io.ReadCloser
	path     string
	expected int64
	read     int64
	noteErr  func(error)
}

func (r *sizeVerifiedReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.read += int64(n)
	if r.read > r.expected {
		e := fmt.Errorf("sizedfs: %q: read %d bytes but declared size is %d (over-read)", r.path, r.read, r.expected)
		r.noteErr(e)
		return n, e
	}
	if err == io.EOF && r.read < r.expected {
		e := fmt.Errorf("sizedfs: %q: got %d bytes, expected %d (truncated)", r.path, r.read, r.expected)
		r.noteErr(e)
		return n, e
	}
	return n, err
}
