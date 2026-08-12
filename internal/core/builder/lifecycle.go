package builder

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Lifecycle manages the teardown guarantee for an ephemeral builder VM.
//
// Stop of the VMM is guaranteed on every exit path — success, build error,
// transfer failure, panic, and context cancellation. Guest sync of all
// persistent disks is attempted before the VMM is killed so that cache disks
// do not lose in-flight writes.
//
// Usage pattern inside BuildInVM:
//
//	lc := newLifecycle(stopFn, syncFn)
//	defer lc.panicStop() // covers panic / early return before explicit stop
//
//	// ... boot VM ...
//
//	buildErr := runBuild(...)
//	tearErr  := lc.SyncAndStop(ctx)  // explicit: success AND build-error path
//
// The defer is a no-op if SyncAndStop was already called (idempotent).
type Lifecycle struct {
	stopFn func(ctx context.Context) error // wraps drv.Stop
	syncFn func(ctx context.Context) error // wraps guest "sync" exec; best-effort
	once   sync.Once
}

const guestSyncTimeout = 15 * time.Second

// newLifecycle creates a Lifecycle whose teardown is composed of syncFn then
// stopFn. syncFn errors are surfaced but never prevent stopFn from running.
// stopFn always receives a fresh background context so caller cancellation
// cannot prevent VMM shutdown.
func newLifecycle(
	stopFn func(ctx context.Context) error,
	syncFn func(ctx context.Context) error,
) *Lifecycle {
	return &Lifecycle{stopFn: stopFn, syncFn: syncFn}
}

// SyncAndStop flushes all guest persistent disk writes then stops the VMM.
// It is idempotent: subsequent calls are no-ops and return nil.
//
// The guest sync uses ctx bounded to guestSyncTimeout. The VMM stop always
// uses context.Background() so a cancelled caller context cannot leave an
// orphaned VMM process.
func (lc *Lifecycle) SyncAndStop(ctx context.Context) (retErr error) {
	lc.once.Do(func() {
		// Guest sync — best-effort; a sync failure must not prevent stop.
		syncCtx, cancel := context.WithTimeout(ctx, guestSyncTimeout)
		defer cancel()
		if err := lc.syncFn(syncCtx); err != nil {
			// Record sync error but do not return early.
			retErr = fmt.Errorf("lifecycle: guest sync: %w", err)
		}

		// VMM stop — unconditional; use background context so the caller's
		// cancellation cannot orphan a running VMM process.
		if stopErr := lc.stopFn(context.Background()); stopErr != nil {
			if retErr != nil {
				retErr = fmt.Errorf("%w; stop: %v", retErr, stopErr)
			} else {
				retErr = fmt.Errorf("lifecycle: stop: %w", stopErr)
			}
		}
	})
	return retErr
}

// panicSafeStop is used in the defer inside BuildInVM to cover the panic and
// early-return paths where SyncAndStop has not yet been called explicitly.
// It swallows errors because we are already unwinding; callers that care about
// errors should call SyncAndStop before returning.
func (lc *Lifecycle) panicSafeStop() {
	_ = lc.SyncAndStop(context.Background())
}
