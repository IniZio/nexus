// cachedisk_lease.go — D-HSH-07: the builder cache-disk slot lease is owned by
// the supervisor, not by the CLI.
//
// # The ownership mismatch this closes
//
// A cache-disk slot is guarded by an flock on a sidecar <image>.lock file, and
// cloud-hypervisor takes its own exclusive WRITE lock on the image itself for
// as long as the VM is attached to it. Those two locks must expire together.
// Before this file existed the flock was released by a `defer` in the CLI
// process, so it was CLI-scoped while the CH write lock was VM-scoped: any VM
// that outlived its CLI — which happens whenever a spawn times out and
// terminateSupervisor escalates to SIGKILL, orphaning the VM — left the slot
// reading FREE while the image was still locked, and the next build failed to
// boot with an opaque "Error locking disk images" from CH. (Recorded as the
// residual of motive nexus3-builder-supervisor-spawn-race: "the lease was
// CLI-scoped while the image lock is VM-scoped".)
//
// # Who holds it now
//
//   - BOOT (RunDetached): the CLI selects a free slot, then hands the OPEN
//     lock descriptor to this process through ExtraFiles. flock ownership
//     follows the open file description, so the inherited descriptor holds the
//     identical lock and there is no instant in which the slot reads free. The
//     CLI then drops its own copy and the lease lives exactly as long as this
//     supervisor. The slot is persisted on the sandbox record before boot.
//   - ADOPT (RunAdopt) and RE-ACQUIRE (RunReacquire): there is no descriptor to
//     inherit, so the slot is read back off the record and re-acquired BY PATH.
//     Same slot, new owner.
//
// # The lease must die with the supervisor, not with the VM
//
// D-HSH-07 ratified the SUPERVISOR as the lease owner, so the descriptor must
// NOT travel one hop further into the netns child. It would travel there by
// default: an inherited descriptor arrives with FD_CLOEXEC cleared, and
// os/exec neither closes nor re-marks the descriptors a process already holds,
// so the lease would survive the netns child's execve into cloud-hypervisor.
// builder.AdoptCacheDiskLeaseFD therefore sets FD_CLOEXEC on adoption.
//
// With that in place the actual behaviour is:
//
//   - A supervisor SIGKILL closes these descriptors and the slot reads FREE,
//     even though the netns child — and therefore cloud-hypervisor's write
//     lock on the IMAGE — survives. `nexus3 recover` spawns a replacement
//     supervisor which re-acquires the slot by path here, and it succeeds.
//   - On a planned supervisor-upgrade the outgoing supervisor's
//     `defer ReleaseCacheDiskLeases` frees the slot while the VM is
//     deliberately kept alive, and the incoming supervisor takes it by path.
//
// Residual: between the outgoing supervisor's death and the incoming one's
// acquisition the slot reads free while CH still holds the image write lock.
// A third builder that selects the slot in that window fails to boot with CH's
// "Error locking disk images". The window is the recover/upgrade turnaround,
// not the VM's whole life, which is what the CLI-scoped lease used to be.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
)

// cacheDiskAdoptLeaseTimeout bounds how long an adopting supervisor waits for
// the outgoing supervisor to drop its cache-disk lease. The outgoing side
// releases on its own shutdown, which runs immediately after it reads a
// positive handoff Ack, so this is headroom rather than the expected wait — it
// mirrors adoptWaitOldExitTimeout for the same reason.
const cacheDiskAdoptLeaseTimeout = 15 * time.Second

// acquireCacheDiskLeases takes ownership of every cache-disk slot in slots.
//
// leaseFDs, when it has an entry for a slot, is a descriptor inherited from
// the process that selected that slot and already holding its flock; that
// descriptor is adopted rather than re-locked. Slots without an inherited
// descriptor are acquired by path, retrying up to wait for a previous owner
// to let go.
//
// It is all-or-nothing: any failure releases what was taken and returns an
// error, so a supervisor never proceeds believing it owns a slot it does not.
func acquireCacheDiskLeases(ctx context.Context, slots []string, leaseFDs []int, wait time.Duration) ([]*builder.CacheDiskLease, error) {
	if len(slots) == 0 {
		return nil, nil
	}
	held := make([]*builder.CacheDiskLease, 0, len(slots))
	for i, slot := range slots {
		var (
			lease *builder.CacheDiskLease
			err   error
		)
		if i < len(leaseFDs) && leaseFDs[i] > 0 {
			lease, err = builder.AdoptCacheDiskLeaseFD(leaseFDs[i], slot)
		} else {
			lease, err = builder.AcquireCacheDiskSlotWait(ctx, slot, wait)
		}
		if err != nil {
			builder.ReleaseCacheDiskLeases(held)
			return nil, fmt.Errorf("cache-disk slot %s: %w", slot, err)
		}
		held = append(held, lease)
	}
	slog.Info("supervisor.cachedisk_leases_held", "slots", slots, "inheritedFDs", leaseFDs)
	return held, nil
}
