package cli

import (
	"testing"
	"time"
)

// herdrColdBuildFloor is the absolute regression guard for
// herdrWorktreeCreateTimeout, grounded in measurements rather than a round
// number picked in isolation.
//
// Measured live on this host on 2026-08-31, through the UNBOUNDED
// `nexus3 create --file` path (no herdrWorktreeCreateTimeout in effect), with
// a cold buildkit LAYER cache (caches/buildkit.ext4 wiped first — a cold
// fingerprint over a warm layer cache is fast and does not reproduce this):
//   - example-app: 120s
//   - nexus3:     152s
//
// herdrWorktreeCreateTimeout must stay comfortably above the worse of the
// two (152s) or the exact self-sustaining cache-poison loop this slice fixes
// (build overruns bound -> SIGKILL -> unclean death -> dirty-marker wipe ->
// next attempt starts cold -> repeat) reopens. This is an ABSOLUTE floor: it
// exists because TestHerdrWorktreeCreateLockTimeout_ExceedsWorstCaseCreate
// only checks a RELATIVE invariant (lock > create) that a shrunk
// herdrWorktreeCreateTimeout still satisfies, so that test alone cannot catch
// a regression back toward the original, too-short 90s value.
const herdrColdBuildFloor = 152 * time.Second

func TestHerdrWorktreeCreateTimeout_AboveMeasuredColdBuildFloor(t *testing.T) {
	if herdrWorktreeCreateTimeout <= herdrColdBuildFloor {
		t.Fatalf("herdrWorktreeCreateTimeout (%v) must exceed the measured cold-build floor (%v; see herdrColdBuildFloor doc comment for the 2026-08-31 example-app/nexus3 measurements) — "+
			"a value at or below this reopens the self-sustaining buildkit cache-poison loop (build overruns bound -> SIGKILL -> unclean death -> dirty-marker wipe -> next attempt starts cold)",
			herdrWorktreeCreateTimeout, herdrColdBuildFloor)
	}
}

// TestHerdrWorktreeCreateLockTimeout_ExceedsWorstCaseCreate guards the
// invariant documented on herdrWorktreeCreateLockTimeout: a second concurrent
// caller waiting for the create-intent lock must never give up before the
// first caller's worst-case total time (herdrWorktreeCreateTimeout).
func TestHerdrWorktreeCreateLockTimeout_ExceedsWorstCaseCreate(t *testing.T) {
	if herdrWorktreeCreateLockTimeout <= herdrWorktreeCreateTimeout {
		t.Fatalf("herdrWorktreeCreateLockTimeout (%v) must exceed herdrWorktreeCreateTimeout (%v)",
			herdrWorktreeCreateLockTimeout, herdrWorktreeCreateTimeout)
	}
}
