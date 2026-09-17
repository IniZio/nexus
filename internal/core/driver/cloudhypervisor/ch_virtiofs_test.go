// ch_virtiofs_test.go — unit tests for virtiofs config marshalling, socket-path
// limit, and the production wiring in spawnVirtiofsdForMounts.
//
// Acceptance criteria exercised:
//
//	AC #4 — virtiofsd killed by process group; socket unlinked on failure.
//	         TestSpawnVirtiofsdForMounts_FailureKillsOrphans (real wiring path)
//	AC #5 — virtiofsd socket path ≤107 bytes for max-length SocketDir.
//	         TestVirtiofsdSockPath_SunPathLimit
//	AC #6 — vmFsConfig serialises correctly; "fs" omitted when absent.
//	         TestVmFsConfig_Marshal, TestVmFsConfig_OmitWhenAbsent
//	tags  — spawnVirtiofsdForMounts tags match VirtiofsTag(i).
//	         TestSpawnVirtiofsdForMounts_TagsAndCount
package cloudhypervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

// fakeVirtiofsd writes a shell script to a temp dir that, when executed,
// creates the file at --socket-path and then sleeps until killed.
// Simulates a ready virtiofsd without the real binary.
func fakeVirtiofsd(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "virtiofsd")
	src := `#!/bin/sh
sock=""
while [ $# -gt 0 ]; do
  case "$1" in
    --socket-path) sock="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$sock" ] && touch "$sock"
sleep 300
`
	if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
		t.Fatalf("write fake virtiofsd: %v", err)
	}
	return script
}

// testDriver builds a minimal CHDriver with a custom virtiofsd binary and mounts.
func testDriver(t *testing.T, virtiofsdBin string, mounts []domain.LiveMount) *CHDriver {
	t.Helper()
	return &CHDriver{
		cfg: Config{
			SocketDir:     testSocketDir(t),
			VirtiofsdPath: virtiofsdBin,
			LiveMounts:    mounts,
		},
		procs:          make(map[domain.SandboxID]*managedProcess),
		nets:           make(map[domain.SandboxID]*netState),
		virtiofsdProcs: make(map[domain.SandboxID][]*managedProcess),
	}
}

// --- wiring tests ---

// TestSpawnVirtiofsdForMounts_TagsAndCount verifies that spawnVirtiofsdForMounts
// returns exactly len(LiveMounts) vmFsConfig entries, each tagged VirtiofsTag(i),
// and registers the same count in virtiofsdProcs[id].
func TestSpawnVirtiofsdForMounts_TagsAndCount(t *testing.T) {
	bin := fakeVirtiofsd(t)
	shared := t.TempDir()
	mounts := []domain.LiveMount{
		{HostPath: shared, GuestPath: "/w0"},
		{HostPath: shared, GuestPath: "/w1", ReadOnly: true},
	}
	d := testDriver(t, bin, mounts)
	var id domain.SandboxID

	fsCfgs, err := d.spawnVirtiofsdForMounts(t.Context(), id)
	if err != nil {
		t.Fatalf("spawnVirtiofsdForMounts: %v", err)
	}
	t.Cleanup(func() { d.clearState(id) })

	if len(fsCfgs) != len(mounts) {
		t.Fatalf("got %d fsCfgs, want %d", len(fsCfgs), len(mounts))
	}
	for i, cfg := range fsCfgs {
		want := VirtiofsTag(i)
		if cfg.Tag != want {
			t.Errorf("fsCfgs[%d].Tag = %q, want %q", i, cfg.Tag, want)
		}
		if cfg.Socket == "" {
			t.Errorf("fsCfgs[%d].Socket is empty", i)
		}
	}

	d.mu.Lock()
	nProcs := len(d.virtiofsdProcs[id])
	d.mu.Unlock()
	if nProcs != len(mounts) {
		t.Errorf("virtiofsdProcs[id] has %d entries, want %d", nProcs, len(mounts))
	}
}

// TestSpawnVirtiofsdForMounts_FailureKillsOrphans proves AC #4 via the real
// production wiring path:
//  1. Mount[0] spawn succeeds (seam calls real spawnVirtiofsd with the good binary).
//  2. Mount[1] spawn fails synchronously via the injected seam (no timing dependency).
//  3. clearState (called explicitly, as Start's cleanup() calls it on failure) kills
//     mount[0]'s process group and unlinks its socket.
//
// This proves that no orphan virtiofsd process or stale socket survives a
// mid-sequence failure — the property that Start's cleanup() → clearState path must guarantee.
func TestSpawnVirtiofsdForMounts_FailureKillsOrphans(t *testing.T) {
	goodBin := fakeVirtiofsd(t)
	shared := t.TempDir()

	socketDir := testSocketDir(t)
	var id domain.SandboxID

	// pid0 is captured by the seam closure once mount[0] spawns successfully.
	var pid0 int

	// callCount tracks how many times the seam has been invoked.
	// Call 0 (mount[0]): delegate to the real spawnVirtiofsd so a live process
	//   exists in virtiofsdProcs[id] — it becomes the "orphan" under test.
	// Call 1 (mount[1]): return a deterministic error synchronously, with no
	//   timing dependency, so the test never flakes under load.
	var callCount int
	seam := func(ctx context.Context, binaryPath, socketPath, sharedDir string, readOnly bool) (*managedProcess, error) {
		n := callCount
		callCount++
		if n == 0 {
			vp, err := spawnVirtiofsd(ctx, goodBin, socketPath, sharedDir, readOnly)
			if err != nil {
				return nil, err
			}
			pid0 = vp.pid
			return vp, nil
		}
		return nil, fmt.Errorf("injected failure for mount[%d]", n)
	}

	d := &CHDriver{
		cfg: Config{
			SocketDir:     socketDir,
			VirtiofsdPath: goodBin,
			LiveMounts: []domain.LiveMount{
				{HostPath: shared, GuestPath: "/ok"},
				{HostPath: shared, GuestPath: "/bad"},
			},
		},
		procs:            make(map[domain.SandboxID]*managedProcess),
		nets:             make(map[domain.SandboxID]*netState),
		virtiofsdProcs:   make(map[domain.SandboxID][]*managedProcess),
		spawnVirtiofsdFn: seam,
	}

	_, spawnErr := d.spawnVirtiofsdForMounts(t.Context(), id)
	if spawnErr == nil {
		t.Fatal("expected spawnVirtiofsdForMounts to fail, got nil")
	}
	t.Logf("spawn error (expected): %v", spawnErr)

	if pid0 == 0 {
		t.Fatal("seam for mount[0] was never called — test wiring is broken")
	}

	// Simulate Start's cleanup() call. clearState kills virtiofsdProcs[id] and
	// removes their sockets. The registered vp0 is the "orphan" to be killed.
	sock0 := virtiofsdSockPath(socketDir, id, 0)
	d.clearState(id)

	// Assert: vp0 must be dead.
	if alive, msg := procAlive(pid0); alive {
		t.Errorf("virtiofsd[0] (pid %d) still alive after clearState: %s", pid0, msg)
	}

	// Assert: sock0 must be removed.
	if _, err := os.Stat(sock0); !os.IsNotExist(err) {
		t.Errorf("virtiofsd socket %q still exists after clearState", sock0)
	}
}

// procAlive returns (true, status) when the process is running (not a zombie),
// (false, "") when gone or zombie-reaped.
func procAlive(pid int) (bool, string) {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	b, err := os.ReadFile(statusPath)
	if err != nil {
		return false, "" // process gone
	}
	s := string(b)
	if strings.Contains(s, "State:\tZ") {
		return false, "zombie (already signalled, reaping in progress)"
	}
	if strings.Contains(s, "State:") {
		return true, s
	}
	return false, ""
}

// TestSpawnVirtiofsdForMounts_NoMounts verifies zero mounts returns (nil, nil).
func TestSpawnVirtiofsdForMounts_NoMounts(t *testing.T) {
	d := testDriver(t, "", nil)
	var id domain.SandboxID
	fsCfgs, err := d.spawnVirtiofsdForMounts(t.Context(), id)
	if err != nil || fsCfgs != nil {
		t.Errorf("want (nil,nil), got (%v,%v)", fsCfgs, err)
	}
}

// TestSpawnVirtiofsdForMounts_MissingBinary verifies non-empty LiveMounts with
// empty VirtiofsdPath returns an actionable error naming VirtiofsdPath.
func TestSpawnVirtiofsdForMounts_MissingBinary(t *testing.T) {
	shared := t.TempDir()
	d := testDriver(t, "", []domain.LiveMount{{HostPath: shared, GuestPath: "/w"}})
	var id domain.SandboxID
	_, err := d.spawnVirtiofsdForMounts(t.Context(), id)
	if err == nil {
		t.Fatal("expected error for empty VirtiofsdPath")
	}
	if !strings.Contains(err.Error(), "VirtiofsdPath") {
		t.Errorf("error %q does not name VirtiofsdPath", err)
	}
}

// TestSpawnVirtiofsdForMounts_RealBinary exercises against the real virtiofsd
// when available. Skipped in CI without the binary.
func TestSpawnVirtiofsdForMounts_RealBinary(t *testing.T) {
	bin := filepath.Join(os.Getenv("HOME"), ".local/bin/virtiofsd")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("virtiofsd not at %s", bin)
	}
	shared := t.TempDir()
	mounts := []domain.LiveMount{
		{HostPath: shared, GuestPath: "/w0"},
		{HostPath: shared, GuestPath: "/w1", ReadOnly: true},
	}
	d := testDriver(t, bin, mounts)
	var id domain.SandboxID
	fsCfgs, err := d.spawnVirtiofsdForMounts(t.Context(), id)
	if err != nil {
		t.Fatalf("spawnVirtiofsdForMounts: %v", err)
	}
	if len(fsCfgs) != 2 {
		t.Fatalf("got %d fsCfgs, want 2", len(fsCfgs))
	}
	for i, cfg := range fsCfgs {
		want := VirtiofsTag(i)
		if cfg.Tag != want {
			t.Errorf("[%d] tag %q != %q", i, cfg.Tag, want)
		}
		if _, err := os.Stat(cfg.Socket); err != nil {
			t.Errorf("[%d] socket missing: %v", i, err)
		}
	}
	d.clearState(id)
	for i, cfg := range fsCfgs {
		if _, err := os.Stat(cfg.Socket); !os.IsNotExist(err) {
			t.Errorf("[%d] socket %q still exists after clearState", i, cfg.Socket)
		}
	}
}

// --- JSON marshalling tests (AC #6) ---

// TestVmFsConfig_Marshal verifies vmFsConfig serialises to CH's FsConfig shape.
func TestVmFsConfig_Marshal(t *testing.T) {
	fs := vmFsConfig{Tag: "nxfs0", Socket: "/run/nexus/id.vfs0"}
	b, err := json.Marshal(fs)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if v, _ := m["tag"].(string); v != "nxfs0" {
		t.Errorf("tag = %q, want nxfs0", v)
	}
	if _, present := m["num_queues"]; present {
		t.Errorf("num_queues present when zero (want omitted); raw=%s", b)
	}
}

// TestVmFsConfig_NumQueues verifies num_queues is emitted when non-zero.
func TestVmFsConfig_NumQueues(t *testing.T) {
	b, _ := json.Marshal(vmFsConfig{Tag: "nxfs0", Socket: "/s", NumQueues: 4})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if v, _ := m["num_queues"].(float64); int(v) != 4 {
		t.Errorf("num_queues = %v, want 4", m["num_queues"])
	}
}

// TestVmFsConfig_OmitWhenAbsent verifies vmConfigWithNet omits "fs" key when Fs is nil.
func TestVmFsConfig_OmitWhenAbsent(t *testing.T) {
	cfg := vmConfigWithNet{
		vmConfig: vmConfig{Payload: vmPayloadConfig{Kernel: "/boot/vmlinux"}},
		Net:      []vmNetConfig{{Tap: "tap0", Mac: "52:54:00:aa:bb:cc", NumQueues: 2}},
	}
	b, _ := json.Marshal(cfg)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, present := m["fs"]; present {
		t.Errorf("fs key present when Fs nil (want omitted); raw=%s", b)
	}
}

// TestVmFsConfig_PresentWhenSet verifies vmConfigWithNet includes "fs" when populated.
func TestVmFsConfig_PresentWhenSet(t *testing.T) {
	cfg := vmConfigWithNet{
		vmConfig: vmConfig{Payload: vmPayloadConfig{Kernel: "/boot/vmlinux"}},
		Fs: []vmFsConfig{
			{Tag: "nxfs0", Socket: "/run/nexus/id.vfs0"},
			{Tag: "nxfs1", Socket: "/run/nexus/id.vfs1"},
		},
	}
	b, _ := json.Marshal(cfg)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	fsList, _ := m["fs"].([]any)
	if len(fsList) != 2 {
		t.Errorf("fs has %d entries, want 2; raw=%s", len(fsList), b)
	}
}

// TestVirtiofsTag_SingleSourceOfTruth verifies the tag format. Brittle by design:
// a format change here surfaces callers that hard-coded the old string.
func TestVirtiofsTag_SingleSourceOfTruth(t *testing.T) {
	if tag := VirtiofsTag(0); tag != "nxfs0" {
		t.Errorf("VirtiofsTag(0) = %q, want nxfs0", tag)
	}
	if tag := VirtiofsTag(3); tag != "nxfs3" {
		t.Errorf("VirtiofsTag(3) = %q, want nxfs3", tag)
	}
}

// TestVirtiofsdSockPath_SunPathLimit verifies the path fits in 107 bytes for
// the longest SocketDir that passes New()'s validation (107−35 = 72 chars).
func TestVirtiofsdSockPath_SunPathLimit(t *testing.T) {
	socketDir := strings.Repeat("d", maxSocketPathLen-35) // 72 chars
	var id domain.SandboxID
	path := virtiofsdSockPath(socketDir, id, 9)
	if len(path) > maxSocketPathLen {
		t.Errorf("path len=%d > %d: %q", len(path), maxSocketPathLen, path)
	}
	t.Logf("path len=%d (limit=%d)", len(path), maxSocketPathLen)
}
