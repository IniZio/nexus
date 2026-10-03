package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

/**
 * Create-deadline policy for `sandbox create` run by worktree-sandbox.
 *
 * A cold image build (buildkit pull + builder VM + layers) can take far
 * longer than the old fixed 240s bound, yet it is making progress the whole
 * time. The guest builder's stderr is buffered (core/builder guestBuild,
 * sbuilder), not streamed, so the child can be silent on stdout/stderr for
 * the entire build. Liveness is therefore two signals: any child output, and
 * growth/modification of the build-state files under the store root (the
 * buildkit cache disk, images, disks), sampled every herdrCreateProbeEvery.
 * The child is killed only when neither moves for herdrWorktreeCreateIdleTimeout,
 * or when herdrWorktreeCreateTimeout (hard cap) elapses.
 *
 * The same bounds apply to --auto/hook calls: they run the identical create,
 * so they share the cold-build risk; a shorter bound would SIGKILL the build
 * mid-write and recreate the cache-poison loop.
 *
 * The package vars exist so tests can scale the durations down.
 */
const (
	herdrWorktreeCreateIdleTimeout = 180 * time.Second
	herdrWorktreeCreateTimeout     = 15 * time.Minute
	herdrCreateProbeInterval       = 5 * time.Second
)

var (
	herdrCreateIdleLimit  = herdrWorktreeCreateIdleTimeout
	herdrCreateHardCap    = herdrWorktreeCreateTimeout
	herdrCreateProbeEvery = herdrCreateProbeInterval
)

var errHerdrCreateIdle = errors.New("create idle")

/** herdrIdleWatchdog cancels a context when Touch is not called within idle. */
type herdrIdleWatchdog struct {
	idle   time.Duration
	cancel context.CancelCauseFunc
	mu     sync.Mutex
	timer  *time.Timer
}

func newHerdrIdleWatchdog(parent context.Context, idle time.Duration) (context.Context, *herdrIdleWatchdog) {
	ctx, cancel := context.WithCancelCause(parent)
	w := &herdrIdleWatchdog{idle: idle, cancel: cancel}
	w.timer = time.AfterFunc(idle, func() { cancel(errHerdrCreateIdle) })
	return ctx, w
}

func (w *herdrIdleWatchdog) Touch() {
	w.mu.Lock()
	w.timer.Reset(w.idle)
	w.mu.Unlock()
}

func (w *herdrIdleWatchdog) Stop() {
	w.mu.Lock()
	w.timer.Stop()
	w.mu.Unlock()
	w.cancel(nil)
}

/** Wrap returns a writer that Touches the watchdog on every write. */
func (w *herdrIdleWatchdog) Wrap(dst io.Writer) io.Writer {
	return touchWriter{dst: dst, w: w}
}

type touchWriter struct {
	dst io.Writer
	w   *herdrIdleWatchdog
}

func (t touchWriter) Write(p []byte) (int, error) {
	t.w.Touch()
	return t.dst.Write(p)
}

/**
 * herdrCreateActivityProbe returns a cheap fingerprint of build-state files
 * under storeRoot (allocated blocks + mtime, two levels deep). It changes
 * whenever the buildkit cache disk, image store or disks are written.
 */
func herdrCreateActivityProbe(storeRoot string) func() string {
	dirs := []string{"caches", "images", "disks", "build-cache"}
	return func() string {
		var blocks, mtime int64
		for _, d := range dirs {
			base := filepath.Join(storeRoot, d)
			_ = filepath.WalkDir(base, func(p string, de fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if de.IsDir() {
					if rel, rerr := filepath.Rel(base, p); rerr == nil && depth(rel) > 2 {
						return fs.SkipDir
					}
					return nil
				}
				fi, ierr := de.Info()
				if ierr != nil {
					return nil
				}
				if st, ok := fi.Sys().(*syscall.Stat_t); ok {
					blocks += st.Blocks
				}
				if m := fi.ModTime().UnixNano(); m > mtime {
					mtime = m
				}
				return nil
			})
		}
		return fmt.Sprintf("%d/%d", blocks, mtime)
	}
}

func depth(rel string) int {
	if rel == "." {
		return 0
	}
	n := 1
	for _, c := range rel {
		if c == os.PathSeparator {
			n++
		}
	}
	return n
}

/**
 * herdrRunCreateWatched runs the command built by mk under an idle watchdog.
 * Output written to the returned writers and any change in probe() reset the
 * idle timer. On idle expiry the child is killed (default exec kill) and the
 * error names the idle limit.
 */
func herdrRunCreateWatched(
	ctx context.Context,
	idle, probeEvery time.Duration,
	probe func() string,
	mk func(ctx context.Context, wrap func(io.Writer) io.Writer) *exec.Cmd,
) error {
	wctx, wd := newHerdrIdleWatchdog(ctx, idle)
	defer wd.Stop()
	if probe != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			last := probe()
			t := time.NewTicker(probeEvery)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-wctx.Done():
					return
				case <-t.C:
					if cur := probe(); cur != last {
						last = cur
						wd.Touch()
					}
				}
			}
		}()
	}
	cmd := mk(wctx, wd.Wrap)
	err := cmd.Run()
	if err != nil && errors.Is(context.Cause(wctx), errHerdrCreateIdle) {
		return fmt.Errorf("no progress for %s (no output and no build-state disk activity); child killed: %w", idle, err)
	}
	return err
}
