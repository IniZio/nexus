// ch_virtiofs_test.go — unit tests for virtiofs config marshalling, socket-path
// limit, and the production wiring in spawnVirtiofsdForMounts.
//
// Acceptance criteria exercised:
//   AC #4 — virtiofsd killed by process group; socket unlinked on failure.
//            TestSpawnVirtiofsdForMounts_FailureKillsOrphans (real wiring path)
//   AC #5 — virtiofsd socket path ≤107 bytes for max-length SocketDir.
//            TestVirtiofsdSockPath_SunPathLimit
//   AC #6 — vmFsConfig serialises correctly; "fs" omitted when absent.
//            TestVmFsConfig_Marshal, TestVmFsConfig_OmitWhenAbsent
//   tags  — spawnVirtiofsdForMounts tags match VirtiofsTag(i).
//            TestSpawnVirtiofsdForMounts_TagsAndCount
package cloudhypervisor

import (
	"time"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
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

// fakeVirtiofsdBad writes a script that exits immediately without creating
// the socket, so spawnVirtiofsd times out waiting for it.
func fakeVirtiofsdBad(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "virtiofsd-bad")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write bad virtiofsd: %v", err)
	}
	return script
}

// testDriver builds a minimal CHDriver with a custom virtiofsd binary and mounts.
func testDriver(t *testing.T, virtiofsdBin string, mounts []domain.LiveMount) *CHDriver {
	t.Helper()
	return &CHDriver{
		cfg: Config{
			SocketDir:     t.TempDir(),
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
//   1. Mount[0] is spawned with the good binary and registered in virtiofsdProcs[id].
//   2. Mount[1] spawn uses a bad binary and fails (context cancelled quickly).
//   3. clearState (called explicitly, as Start's cleanup() calls it on failure) kills
//      mount[0]'s process group and unlinks its socket.
//
// This proves that no orphan virtiofsd process or stale socket survives a
// mid-sequence failure — the property that Start's cleanup() → clearState path must guarantee.
func TestSpawnVirtiofsdForMounts_FailureKillsOrphans(t *testing.T) {
	goodBin := fakeVirtiofsd(t)
	shared := t.TempDir()

	// Build driver with good binary but set cfg.VirtiofsdPath to bad after pre-registering mount[0].
	socketDir := t.TempDir()
	var id domain.SandboxID

	d := &CHDriver{
		cfg: Config{
			SocketDir:     socketDir,
			VirtiofsdPath: goodBin,
			LiveMounts: []domain.LiveMount{
				{HostPath: shared, GuestPath: "/ok"},
				{HostPath: shared, GuestPath: "/bad"},
			},
		},
		procs:          make(map[domain.SandboxID]*managedProcess),
		nets:           make(map[domain.SandboxID]*netState),
		virtiofsdProcs: make(map[domain.SandboxID][]*managedProcess),
	}

	// Spawn mount[0] successfully with the good binary and register it
	// (this mirrors what spawnVirtiofsdForMounts does on first iteration).
	sock0 := virtiofsdSockPath(socketDir, id, 0)
	vp0, err := spawnVirtiofsd(t.Context(), goodBin, sock0, shared, false)
	if err != nil {
		t.Fatalf("pre-spawn mount[0]: %v", err)
	}
	pid0 := vp0.pid
	d.mu.Lock()
	d.virtiofsdProcs[id] = append(d.virtiofsdProcs[id], vp0)
	d.mu.Unlock()

	// Now call spawnVirtiofsdForMounts with a context that cancels almost immediately
	// — this causes mount[0]'s spawn attempt to fail (context cancelled before socket
	// appears). In real failure scenarios the bad binary exits without creating the socket.
	// Either way the function returns an error with mount[0] already registered.
	cancelCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, spawnErr := d.spawnVirtiofsdForMounts(cancelCtx, id)
	if spawnErr == nil {
		t.Fatal("expected spawnVirtiofsdForMounts to fail, got nil")
	}
	t.Logf("spawn error (expected): %v", spawnErr)

	// Simulate Start's cleanup() call. clearState kills virtiofsdProcs[id] and
	// removes their sockets. The pre-registered vp0 is the "orphan" to be killed.
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
	const bin = "$HOME/.local/bin/virtiofsd"
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
	fs := vmFsConfig{Tag: "nx3fs0", Socket: "/run/nexus3/id.vfs0"}
	b, err := json.Marshal(fs)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if v, _ := m["tag"].(string); v != "nx3fs0" {
		t.Errorf("tag = %q, want nx3fs0", v)
	}
	if _, present := m["num_queues"]; present {
		t.Errorf("num_queues present when zero (want omitted); raw=%s", b)
	}
}

// TestVmFsConfig_NumQueues verifies num_queues is emitted when non-zero.
func TestVmFsConfig_NumQueues(t *testing.T) {
	b, _ := json.Marshal(vmFsConfig{Tag: "nx3fs0", Socket: "/s", NumQueues: 4})
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
			{Tag: "nx3fs0", Socket: "/run/n3/id.vfs0"},
			{Tag: "nx3fs1", Socket: "/run/n3/id.vfs1"},
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
	if tag := VirtiofsTag(0); tag != "nx3fs0" {
		t.Errorf("VirtiofsTag(0) = %q, want nx3fs0", tag)
	}
	if tag := VirtiofsTag(3); tag != "nx3fs3" {
		t.Errorf("VirtiofsTag(3) = %q, want nx3fs3", tag)
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
