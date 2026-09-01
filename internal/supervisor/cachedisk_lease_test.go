//go:build linux

package supervisor

// cachedisk_lease_test.go — proof for D-HSH-07: the builder cache-disk slot
// lease is owned by the supervisor process, whose lifetime matches the VM's,
// and NOT by the CLI that selected the slot.
//
// # Why this uses real subprocesses
//
// The defect being closed is a process-lifetime mismatch, and flock is a
// per-open-file-description, cross-process primitive. A single-process test
// with a stand-in lock cannot observe either property: it cannot show that a
// lease outlives the selecting process, and it cannot show that a second
// process is refused. So these tests drive the production chain end to end —
// builder.SelectCacheDisks (CLI selection) → supervisor.SpawnDetached (the
// real spawn, real ExtraFiles wiring, real argv) → acquireCacheDiskLeases /
// builder.AdoptCacheDiskLeaseFD in the child (the real supervisor-side
// acquisition) — with the test binary re-execed as the "supervisor", exactly
// as watchdog_linux_test.go already does for the parent watchdog.
//
// # Mutation guards
//
// Each test names, in its own doc comment, the production edit that must turn
// it RED.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// cacheDiskHelperAcquiredFile is where the re-execed "supervisor" records the
// slots it actually took, so the parent can assert on them.
const cacheDiskHelperAcquiredFile = "acquired.txt"

// cacheDiskHelperQuitFile, once created by the parent, tells the re-execed
// "supervisor" to release its leases and exit.
const cacheDiskHelperQuitFile = "quit"

// runCacheDiskLeaseHelper is the `__supervisor` subprocess role used by the
// tests in this file. It performs the SAME acquisition the production
// supervisor performs, through the same functions:
//
//   - BOOT shape — the spawning CLI passed --cache-disk-slot together with
//     --cache-disk-lease-fd descriptors that already hold the flock. This is
//     what RunDetached does with cfg.CacheDiskSlots / cfg.CacheDiskLeaseFDs.
//   - ADOPT/RE-ACQUIRE shape — no slot flags and no descriptors, so the slots
//     are read back off the persisted sandbox record and taken by path. This
//     is what serveAdoptedSupervisor does with sb.CacheDiskSlot.
//
// It then writes the pidfile (SpawnDetached's readiness signal) and holds the
// leases until the parent drops a quit file, standing in for the VM's life.
func runCacheDiskLeaseHelper() {
	args := os.Args[1:]
	var (
		slots                           []string
		fds                             []int
		stateDir, storeRoot, sandboxRef string
	)
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--cache-disk-slot":
			slots = append(slots, args[i+1])
		case "--cache-disk-lease-fd":
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				fmt.Fprintln(os.Stderr, "helper: bad --cache-disk-lease-fd:", args[i+1])
				os.Exit(1)
			}
			fds = append(fds, n)
		case "--state-dir":
			stateDir = args[i+1]
		case "--store-root":
			storeRoot = args[i+1]
		case "--sandbox-ref":
			sandboxRef = args[i+1]
		}
	}
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		if stateDir != "" {
			_ = os.WriteFile(filepath.Join(stateDir, supervisorErrFile), []byte(msg), 0o600)
		}
		fmt.Fprintln(os.Stderr, "helper:", msg)
		os.Exit(1)
	}

	ctx := context.Background()
	if len(slots) == 0 && storeRoot != "" && sandboxRef != "" {
		st, err := store.NewFileStore(storeRoot)
		if err != nil {
			fail("open store: %v", err)
		}
		sb, err := st.ResolveByPrefix(ctx, sandboxRef)
		if err != nil {
			fail("resolve %s: %v", sandboxRef, err)
		}
		slots = builder.DecodeCacheDiskSlots(sb.CacheDiskSlot)
		if len(slots) == 0 {
			fail("record %s names no cache-disk slot", sandboxRef)
		}
	}

	leases, err := acquireCacheDiskLeases(ctx, slots, fds, 5*time.Second)
	if err != nil {
		fail("acquire: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, cacheDiskHelperAcquiredFile),
		[]byte(strings.Join(builder.CacheDiskSlotPaths(leases), "\n")), 0o600); err != nil {
		fail("write acquired file: %v", err)
	}
	// Pidfile last: it is SpawnDetached's readiness signal, so writing it
	// before the leases are held would let the parent race ahead of the fact
	// it is about to assert.
	if err := os.WriteFile(PidfilePath(stateDir), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fail("write pidfile: %v", err)
	}
	quit := filepath.Join(stateDir, cacheDiskHelperQuitFile)
	for {
		if _, statErr := os.Stat(quit); statErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	builder.ReleaseCacheDiskLeases(leases)
	os.Exit(0)
}

func skipUnlessCacheDiskUsable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available; cannot exercise the real cache-disk selection path")
	}
	if _, err := os.Stat("/dev/vda"); err == nil {
		t.Skip("skipping heavy mke2fs test in-guest (see builder.skipIfInGuest)")
	}
}

// spawnLeaseHolder spawns the test binary as a detached "supervisor" through
// the production SpawnDetached, and returns its state dir. Cleanup drops the
// quit file and waits for the process to go.
func spawnLeaseHolder(t *testing.T, cfg SpawnConfig) (stateDir string, pid int) {
	t.Helper()
	cfg.Exe = os.Args[0]
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = 30 * time.Second
	}
	pid, watchdogW, err := SpawnDetached(cfg)
	if err != nil {
		if b, readErr := os.ReadFile(filepath.Join(cfg.StateDir, "supervisor.log")); readErr == nil {
			t.Logf("helper log:\n%s", b)
		}
		t.Fatalf("SpawnDetached: %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(cfg.StateDir, cacheDiskHelperQuitFile), []byte("x"), 0o600)
		for i := 0; i < 200 && PidAlive(pid); i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if watchdogW != nil {
			_ = watchdogW.Close()
		}
	})
	return cfg.StateDir, pid
}

// baseHelperSpawnConfig is the minimum SpawnConfig BuildSupervisorArgv needs;
// the helper ignores everything except the flags it scans for.
func baseHelperSpawnConfig(t *testing.T, stateDir string) SpawnConfig {
	t.Helper()
	return SpawnConfig{
		Config: Config{
			SandboxRef: "helper",
			StoreRoot:  t.TempDir(),
			StateDir:   stateDir,
			CHBin:      "/bin/true",
			SocketDir:  stateDir,
			KernelPath: "/dev/null",
			DiskPath:   "/dev/null",
			Ephemeral:  true, // builder's production setting: fd 3 is the watchdog pipe
		},
	}
}

// TestCacheDiskLease_SurvivesTheSelectingProcessAndBlocksASecondBuilder is the
// central proof of D-HSH-07.
//
// It walks a builder VM's real life:
//
//  1. the CLI selects a slot (builder.SelectCacheDisks);
//  2. the supervisor is spawned with the lock descriptor (SpawnDetached);
//  3. the CLI drops its own copy of the lease — the moment that used to free
//     the slot while the VM ran on;
//  4. the slot must still read BUSY, and a second builder's selection must be
//     handed a DIFFERENT slot;
//  5. once the "VM" is genuinely gone the slot must be reclaimable.
//
// Mutation guards — each of these must turn this test RED:
//   - spawn_linux.go: drop the cache-disk descriptors from cmd.ExtraFiles (or
//     stop populating cfg.CacheDiskLeaseFDs) → step 4 finds the slot free.
//   - cachedisk.go: make CacheDiskLease.Release unlink the lock file → the
//     child and a later opener both "hold" the slot.
//   - builder_supervisor_driver.go: stop forwarding CacheDiskLeaseFiles →
//     step 4 finds the slot free (covered via the driver test below).
func TestCacheDiskLease_SurvivesTheSelectingProcessAndBlocksASecondBuilder(t *testing.T) {
	skipUnlessCacheDiskUsable(t)

	ctx := context.Background()
	dataDir := t.TempDir()

	// ── 1. CLI selects a free slot ────────────────────────────────────────
	specs, leases, err := builder.SelectCacheDisks(ctx, dataDir, []string{"buildkit"})
	if err != nil {
		t.Fatalf("SelectCacheDisks: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(specs))
	}
	slot0 := specs[0].ImagePath

	// ── 2. Supervisor is spawned with the held descriptor ─────────────────
	stateDir := t.TempDir()
	cfg := baseHelperSpawnConfig(t, stateDir)
	cfg.CacheDiskSlots = builder.CacheDiskSlotPaths(leases)
	for _, l := range leases {
		cfg.CacheDiskLeaseFiles = append(cfg.CacheDiskLeaseFiles, l.File())
	}
	spawnLeaseHolder(t, cfg)

	acquired, err := os.ReadFile(filepath.Join(stateDir, cacheDiskHelperAcquiredFile))
	if err != nil {
		t.Fatalf("read acquired file: %v", err)
	}
	if got := strings.TrimSpace(string(acquired)); got != slot0 {
		t.Fatalf("supervisor holds slot %q, want the selected slot %q", got, slot0)
	}

	// ── 3. The CLI drops its copy. This is the CLI exiting. ───────────────
	builder.ReleaseCacheDiskLeases(leases)

	// ── 4. The lease must still be held — by the supervisor ───────────────
	if lease, err := builder.AcquireCacheDiskSlot(slot0); err == nil {
		lease.Release()
		t.Fatal("slot read FREE after the selecting process released it — the lease is still CLI-scoped (D-HSH-07 regression)")
	} else if !errors.Is(err, builder.ErrCacheDiskSlotBusy) {
		t.Fatalf("AcquireCacheDiskSlot: want ErrCacheDiskSlotBusy, got %v", err)
	}

	// A second builder must be routed to a different slot, never to the image
	// cloud-hypervisor still holds a write lock on.
	specs2, leases2, err := builder.SelectCacheDisks(ctx, dataDir, []string{"buildkit"})
	if err != nil {
		t.Fatalf("second SelectCacheDisks: %v", err)
	}
	defer builder.ReleaseCacheDiskLeases(leases2)
	if specs2[0].ImagePath == slot0 {
		t.Fatalf("second builder was handed the live slot %q", slot0)
	}

	// ── 5. Reclaimed once the holder is genuinely gone ────────────────────
	_ = os.WriteFile(filepath.Join(stateDir, cacheDiskHelperQuitFile), []byte("x"), 0o600)
	deadline := time.Now().Add(10 * time.Second)
	var reclaimed *builder.CacheDiskLease
	for time.Now().Before(deadline) {
		if l, aErr := builder.AcquireCacheDiskSlot(slot0); aErr == nil {
			reclaimed = l
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if reclaimed == nil {
		t.Fatal("slot was never reclaimed after its holder exited — a dead supervisor must not leak a slot")
	}
	reclaimed.Release()
}

// TestCacheDiskLease_AdoptingSupervisorReacquiresTheSameSlot proves the
// re-acquire-BY-SLOT path: a supervisor that performed no selection, and that
// receives no descriptor because there is no live sender, must take back the
// exact slot named on the sandbox record.
//
// The slot reaches the record through the production writer
// (service.SetCacheDiskSlot) and is read back through the production reader
// (builder.DecodeCacheDiskSlots + acquireCacheDiskLeases), which is the pair
// serveAdoptedSupervisor uses on both the adopt and the crash-recovery path.
//
// Mutation guards:
//   - cachedisk.go: delete the own-pin/registry lookup so AcquireCacheDiskSlot
//     always scans for the lowest free slot → the child takes a different slot.
//   - service/supervisor.go: make SetCacheDiskSlot a no-op → the child fails
//     with "names no cache-disk slot".
func TestCacheDiskLease_AdoptingSupervisorReacquiresTheSameSlot(t *testing.T) {
	skipUnlessCacheDiskUsable(t)

	ctx := context.Background()
	dataDir := t.TempDir()
	storeRoot := t.TempDir()

	// Occupy slot 0 so the "lowest free slot" answer and the "recorded slot"
	// answer are DIFFERENT. Without this the test would pass even if the
	// child ignored the record entirely.
	specs0, leases0, err := builder.SelectCacheDisks(ctx, dataDir, []string{"buildkit"})
	if err != nil {
		t.Fatalf("SelectCacheDisks slot0: %v", err)
	}
	defer builder.ReleaseCacheDiskLeases(leases0)
	specs1, leases1, err := builder.SelectCacheDisks(ctx, dataDir, []string{"buildkit"})
	if err != nil {
		t.Fatalf("SelectCacheDisks slot1: %v", err)
	}
	slot1 := specs1[0].ImagePath
	if slot1 == specs0[0].ImagePath {
		t.Fatalf("second selection returned the same slot %q", slot1)
	}

	// Persist the recorded slot exactly as a booting supervisor does.
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	id := domain.NewSandboxID()
	if err := st.Create(ctx, domain.Sandbox{ID: id, Name: id.String(), Project: "__builder", State: domain.Created}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc := service.New(st, nil, lifecycle.New())
	if err := svc.SetCacheDiskSlot(ctx, id, builder.EncodeCacheDiskSlots([]string{slot1})); err != nil {
		t.Fatalf("SetCacheDiskSlot: %v", err)
	}

	// The previous owner of slot 1 is gone.
	builder.ReleaseCacheDiskLeases(leases1)

	// Spawn an adopting supervisor: no --cache-disk-slot, no descriptor.
	stateDir := t.TempDir()
	cfg := baseHelperSpawnConfig(t, stateDir)
	cfg.StoreRoot = storeRoot
	cfg.SandboxRef = id.String()
	spawnLeaseHolder(t, cfg)

	acquired, err := os.ReadFile(filepath.Join(stateDir, cacheDiskHelperAcquiredFile))
	if err != nil {
		t.Fatalf("read acquired file: %v", err)
	}
	if got := strings.TrimSpace(string(acquired)); got != slot1 {
		t.Fatalf("adopting supervisor took slot %q, want the recorded slot %q", got, slot1)
	}
	// And it really holds it.
	if l, aErr := builder.AcquireCacheDiskSlot(slot1); aErr == nil {
		l.Release()
		t.Fatal("recorded slot reads FREE while the adopting supervisor claims to hold it")
	}
}

// TestServeAdoptedSupervisor_RefusesWhenRecordedSlotIsHeld drives the REAL
// serveAdoptedSupervisor entry point (the shared tail of RunAdopt and
// RunReacquire) and proves it consults the recorded slot before it starts
// serving. With the slot held by another process it must fail there — not
// proceed to run a VM whose cache disk it does not own.
//
// The holder here is a genuinely FOREIGN live process. That distinction is
// load-bearing: before AdoptCacheDiskLeaseFD set FD_CLOEXEC, the recorded slot
// could also read held because this sandbox's OWN surviving netns child had
// inherited the lease, so the refusal proven here was also the refusal
// production hit against its own VM. It no longer is — see
// TestCacheDiskLease_DiesWithTheSupervisorNotWithTheNetnsGrandchild — and this
// test now covers only the case the refusal is actually for.
//
// Mutation guard: remove the acquireCacheDiskLeases block from
// serveAdoptedSupervisor → this test fails (it panics on the nil service that
// the refusal is supposed to stop it from ever reaching).
func TestServeAdoptedSupervisor_RefusesWhenRecordedSlotIsHeld(t *testing.T) {
	stateDir := t.TempDir()
	holderDir := t.TempDir()
	slot := filepath.Join(t.TempDir(), "buildkit.ext4")

	// Another process holds the slot. No image is needed: the lease is on the
	// sidecar lock file.
	cfg := baseHelperSpawnConfig(t, holderDir)
	cfg.CacheDiskSlots = []string{slot}
	spawnLeaseHolder(t, cfg)

	err := serveAdoptedSupervisor(context.Background(), serveAdoptedInput{
		cfg:       Config{StateDir: stateDir},
		sb:        domain.Sandbox{ID: domain.NewSandboxID(), CacheDiskSlot: slot},
		logPrefix: "supervisor.test",
	})
	if err == nil {
		t.Fatal("serveAdoptedSupervisor returned nil with the recorded slot held elsewhere")
	}
	if !strings.Contains(err.Error(), "cache-disk slot") {
		t.Fatalf("want a cache-disk slot refusal, got %v", err)
	}
}

// TestBuildSupervisorArgv_ForwardsCacheDiskLease guards the argv contract the
// spawned supervisor parses. Mutation guard: delete either loop from
// BuildSupervisorArgv → RED.
func TestBuildSupervisorArgv_ForwardsCacheDiskLease(t *testing.T) {
	args := BuildSupervisorArgv(SpawnConfig{Config: Config{
		CacheDiskSlots:    []string{"/caches/buildkit.ext4"},
		CacheDiskLeaseFDs: []int{4},
	}})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--cache-disk-slot /caches/buildkit.ext4", "--cache-disk-lease-fd 4"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q\ngot: %s", want, joined)
		}
	}
}
