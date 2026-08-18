// Package store provides durable, crash-safe persistence for sandbox records.
// It is the single source of persistence in nexus3, which has no central daemon;
// any CLI invocation reads and writes directly, and multiple may run concurrently.
//
// # Platform support
// This package requires Linux or macOS. Windows is not supported.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Lock is a per-sandbox advisory file lock backed by flock(2).
//
// # Crash safety guarantee
// flock(2) locks are released automatically by the kernel when the holding
// process dies — including on SIGKILL — so a lock cannot be leaked by a crash.
// This is the property that makes flock the correct primitive for multi-process
// CLI invocations with no central daemon: a dead holder never permanently blocks
// a new invocation.
//
// # Lease equivalence
// An exclusive Lock held alongside a sandbox record IS the in-flight operation
// lease. There is no separate lease API: the lock is absent in the resting case
// and present only during an active operation. The kernel releases it if the
// holder dies, which is precisely "a lease whose owner is dead must be
// detectable" — the next caller's Exclusive acquires successfully.
//
// # Lock file identity
// The lock file is created once and is NEVER renamed, replaced, or removed
// during normal operation. Only Delete removes it (along with the entire sandbox
// directory). This invariant is essential: if the lock file were replaced,
// flocking the new inode would provide zero exclusion against processes that
// opened the old inode before the replacement.
type Lock struct {
	f *os.File
}

// OpenLock opens (or creates) the lock file at path and returns a Lock ready
// for acquisition. The file is created with mode 0600 if it does not exist.
// OpenLock does not acquire the lock; call Exclusive.
func OpenLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("store: open lock file %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Exclusive acquires an exclusive (write) lock, blocking until the lock is
// available or ctx is cancelled. EINTR is retried transparently.
func (l *Lock) Exclusive(ctx context.Context) error {
	return l.acquire(ctx, syscall.LOCK_EX)
}

// TryExclusive acquires an exclusive (write) lock using non-blocking attempts
// (LOCK_EX|LOCK_NB) with a 5 ms backoff between retries. Unlike Exclusive,
// the kernel syscall never blocks: each attempt returns immediately with
// EWOULDBLOCK if the lock is held, and the caller checks ctx.Done between
// tries. This gives the context deadline real force (RISK-SD2-1): the caller
// surfaces a timeout error within one backoff interval of the deadline rather
// than being parked in the kernel until a signal happens to fire.
//
// Use TryExclusive for the per-volume advisory lock (checkRWAttach,
// detachVolumeLocked). Use Exclusive for per-sandbox record locks where
// blocking semantics are intentional — do not change those callers.
func (l *Lock) TryExclusive(ctx context.Context) error {
	const retryInterval = 5 * time.Millisecond
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("store: lock: %w", err)
	}
	for {
		err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("store: flock LOCK_NB: %w", err)
		}
		// Lock is held; back off and re-check the deadline.
		select {
		case <-ctx.Done():
			return fmt.Errorf("store: lock: %w", ctx.Err())
		case <-time.After(retryInterval):
		}
	}
}

// Unlock releases a previously acquired lock. The underlying file descriptor
// remains open; call Close when the Lock is no longer needed.
func (l *Lock) Unlock() error {
	if err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("store: flock LOCK_UN: %w", err)
	}
	return nil
}

// Close closes the underlying file descriptor. If a lock is held, it is
// released by the OS as part of the close. After Close the Lock must not
// be used again.
func (l *Lock) Close() error {
	return l.f.Close()
}

// acquire calls Flock with how, retrying on EINTR and respecting ctx.
func (l *Lock) acquire(ctx context.Context, how int) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("store: lock: %w", err)
	}
	for {
		err := syscall.Flock(int(l.f.Fd()), how)
		if err == nil {
			return nil
		}
		if errors.Is(err, syscall.EINTR) {
			if ctx.Err() != nil {
				return fmt.Errorf("store: lock: %w", ctx.Err())
			}
			continue
		}
		return fmt.Errorf("store: flock: %w", err)
	}
}
