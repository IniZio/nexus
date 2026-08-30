package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
)

// stubListener is a net.Listener used by tests as a stand-in for the vsock
// listener.  It panics on Accept (unused in hot-swap tests).
type stubListener struct{ fd int }

func (s *stubListener) Accept() (net.Conn, error) { panic("stub: Accept") }
func (s *stubListener) Close() error              { return nil }
func (s *stubListener) Addr() net.Addr            { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// makeSwapFns — test fixture
// ─────────────────────────────────────────────────────────────────────────────

// makeSwapFns returns a swapFns where all seams succeed by default.
// Tests override one seam at a time to prove fail-closed behaviour.
func makeSwapFns(t *testing.T) (swapFns, *[]string) {
	t.Helper()
	calls := &[]string{}

	fns := swapFns{
		statSize: func(path string) (int64, error) {
			*calls = append(*calls, "stat:"+path)
			return 12345, nil // matches expectedBytes in helpers
		},
		fsyncPath: func(path string) error {
			*calls = append(*calls, "fsync:"+path)
			return nil
		},
		renameAtomic: func(src, dst string) error {
			*calls = append(*calls, "rename:"+src+"->"+dst)
			return nil
		},
		dupListenerFd: func(l net.Listener) (int, error) {
			*calls = append(*calls, "dup")
			return 42, nil
		},
		execSelf: func(argv []string, env []string) error {
			*calls = append(*calls, "exec:"+argv[0])
			return nil
		},
	}
	return fns, calls
}

// callsContain reports whether the calls slice contains an entry with the given prefix.
func callsContain(calls []string, prefix string) bool {
	for _, c := range calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// callsCount counts entries equal to s.
func callsCount(calls []string, s string) int {
	n := 0
	for _, c := range calls {
		if c == s {
			n++
		}
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// Happy path
// ─────────────────────────────────────────────────────────────────────────────

func TestPerformSwap_HappyPath(t *testing.T) {
	fns, calls := makeSwapFns(t)

	err := performSwap(fns, "/sbin/.nexus3-agent.upgrade", 12345, "/sbin/nexus3-agent", "/sbin/.nexus3-agent.prev",
		&stubListener{}, &stubListener{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// stat → fsync (staged) → fsync (dir, twice) → rename (backup) → rename (install) → dup × 2 → exec
	if !callsContain(*calls, "stat:") {
		t.Error("stat not called")
	}
	if !callsContain(*calls, "fsync:") {
		t.Error("fsync not called")
	}
	if !callsContain(*calls, "rename:") {
		t.Error("rename not called")
	}
	if callsCount(*calls, "dup") != 2 {
		t.Errorf("expected 2 dup calls, got %d in %v", callsCount(*calls, "dup"), *calls)
	}
	if !callsContain(*calls, "exec:") {
		t.Error("exec not called")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mutation proof 1: size check removed → size mismatch goes undetected.
// ─────────────────────────────────────────────────────────────────────────────

func TestPerformSwap_SizeMismatch(t *testing.T) {
	fns, _ := makeSwapFns(t)
	fns.statSize = func(path string) (int64, error) { return 99999, nil } // != 12345
	var renameCalled, execCalled bool
	fns.renameAtomic = func(src, dst string) error { renameCalled = true; return nil }
	fns.execSelf = func(argv []string, env []string) error { execCalled = true; return nil }

	err := performSwap(fns, "/sbin/.nexus3-agent.upgrade", 12345, "/sbin/nexus3-agent", "/sbin/.nexus3-agent.prev",
		&stubListener{}, &stubListener{})

	if err == nil {
		t.Fatal("expected size mismatch error, got nil")
	}
	if renameCalled {
		t.Error("rename must not be called after size mismatch")
	}
	if execCalled {
		t.Error("exec must not be called after size mismatch")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mutation proof 2: rename failure → exec must not be called.
// ─────────────────────────────────────────────────────────────────────────────

func TestPerformSwap_InstallRenameFails(t *testing.T) {
	fns, calls := makeSwapFns(t)
	// The backup rename (old→.prev) may succeed; the install rename (staged→install) fails.
	renameCount := 0
	fns.renameAtomic = func(src, dst string) error {
		renameCount++
		*calls = append(*calls, "rename:"+src+"->"+dst)
		if renameCount == 2 {
			// Second rename is the install rename.
			return errors.New("rename: read-only filesystem")
		}
		return nil
	}

	err := performSwap(fns, "/sbin/.nexus3-agent.upgrade", 12345, "/sbin/nexus3-agent", "/sbin/.nexus3-agent.prev",
		&stubListener{}, &stubListener{})

	if err == nil {
		t.Fatal("expected rename error, got nil")
	}
	if callsContain(*calls, "exec:") {
		t.Error("exec must not be called after rename failure")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mutation proof 3: dup failure → exec must not be called; ctrl dup fd closed.
// ─────────────────────────────────────────────────────────────────────────────

func TestPerformSwap_DupFails(t *testing.T) {
	fns, calls := makeSwapFns(t)
	dupCount := 0
	fns.dupListenerFd = func(l net.Listener) (int, error) {
		dupCount++
		*calls = append(*calls, "dup")
		if dupCount == 2 {
			return -1, errors.New("dup: EMFILE")
		}
		return 42, nil
	}

	err := performSwap(fns, "/sbin/.nexus3-agent.upgrade", 12345, "/sbin/nexus3-agent", "/sbin/.nexus3-agent.prev",
		&stubListener{}, &stubListener{})

	if err == nil {
		t.Fatal("expected dup error, got nil")
	}
	if callsContain(*calls, "exec:") {
		t.Error("exec must not be called after dup failure")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Rollback: exec failure → install binary is rolled back from backup.
// ─────────────────────────────────────────────────────────────────────────────

func TestPerformSwap_ExecFails_Rollback(t *testing.T) {
	fns, calls := makeSwapFns(t)
	fns.execSelf = func(argv []string, env []string) error {
		*calls = append(*calls, "exec:"+argv[0])
		return errors.New("exec: EACCES")
	}

	err := performSwap(fns, "/sbin/.nexus3-agent.upgrade", 12345, "/sbin/nexus3-agent", "/sbin/.nexus3-agent.prev",
		&stubListener{}, &stubListener{})

	if err == nil {
		t.Fatal("expected exec error, got nil")
	}
	// Rollback: .prev → install path must appear in rename calls after exec.
	var sawRollback bool
	for _, c := range *calls {
		if c == "rename:/sbin/.nexus3-agent.prev->/sbin/nexus3-agent" {
			sawRollback = true
		}
	}
	if !sawRollback {
		t.Errorf("exec failure must trigger rollback rename .prev→install; calls: %v", *calls)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Fsync: staged binary and directory must be fsynced before rename.
// ─────────────────────────────────────────────────────────────────────────────

func TestPerformSwap_FsyncCalledBeforeRename(t *testing.T) {
	// Track call order as a sequence of tagged events.
	// We need to assert: at least one fsync fires BEFORE the install rename
	// (which is the second rename: backup rename fires first, then install rename).
	type event struct{ kind, arg string }
	var seq []event

	fns, _ := makeSwapFns(t)
	fns.fsyncPath = func(path string) error {
		seq = append(seq, event{"fsync", path})
		return nil
	}
	fns.renameAtomic = func(src, dst string) error {
		seq = append(seq, event{"rename", src + "->" + dst})
		return nil
	}
	fns.execSelf = func(argv []string, env []string) error {
		seq = append(seq, event{"exec", argv[0]})
		return nil
	}

	_ = performSwap(fns, "/sbin/.nexus3-agent.upgrade", 12345, "/sbin/nexus3-agent", "/sbin/.nexus3-agent.prev",
		&stubListener{}, &stubListener{})

	// Find index of first fsync and the install rename (staged→install).
	installRenameIdx := -1
	firstFsyncIdx := -1
	for i, e := range seq {
		if e.kind == "fsync" && firstFsyncIdx == -1 {
			firstFsyncIdx = i
		}
		if e.kind == "rename" && e.arg == "/sbin/.nexus3-agent.upgrade->/sbin/nexus3-agent" {
			installRenameIdx = i
		}
	}

	if firstFsyncIdx == -1 {
		t.Fatal("no fsync call found in sequence")
	}
	if installRenameIdx == -1 {
		t.Fatal("install rename not found in sequence")
	}
	if firstFsyncIdx >= installRenameIdx {
		t.Errorf("first fsync (idx=%d) must precede install rename (idx=%d); seq=%v", firstFsyncIdx, installRenameIdx, seq)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Real-syscall test: os.Rename across tmpfs→rootfs is EXDEV.
// This proves the staged path must be on the same device as the install path.
//
// We simulate the constraint: stage on /dev/shm (tmpfs), install target on
// a t.TempDir (which lands on the rootfs).  os.Rename across them fails with
// EXDEV.  Then verify staging on the SAME dir as the target works.
// ─────────────────────────────────────────────────────────────────────────────

func TestRenameAcrossDeviceFails(t *testing.T) {
	if _, err := os.Stat("/dev/shm"); os.IsNotExist(err) {
		t.Skip("/dev/shm not available; skipping cross-device rename test")
	}

	// Create a file on tmpfs (/dev/shm).
	tmpfsFile := filepath.Join("/dev/shm", "nexus3-rename-test-staged")
	if err := os.WriteFile(tmpfsFile, []byte("agent binary"), 0755); err != nil {
		t.Fatalf("write to /dev/shm: %v", err)
	}
	defer os.Remove(tmpfsFile)

	// Install target on the rootfs (t.TempDir uses TMPDIR which may also be /tmp;
	// on Linux without TMPDIR override, TempDir uses os.TempDir which is /tmp
	// — also tmpfs.  Use /var/tmp which is typically on ext4 if available.
	// Fall back to asserting that /dev/shm is a different device from /tmp.)
	// The test's primary purpose is to prove that /dev/shm → any rootfs path fails.
	varTmp := "/var/tmp"
	if _, err := os.Stat(varTmp); err != nil {
		t.Skip("/var/tmp not available; skipping cross-device rename test")
	}
	rootfsTarget := filepath.Join(varTmp, "nexus3-rename-test-target")
	defer os.Remove(rootfsTarget)

	// Attempt cross-device rename.
	err := os.Rename(tmpfsFile, rootfsTarget)
	if err == nil {
		// Some systems have /dev/shm on the same device as /var/tmp (e.g. tmpfs on /).
		// The rename succeeded, meaning this is not a cross-device scenario.
		t.Log("os.Rename /dev/shm→/var/tmp succeeded (same device); cross-device guard is still correct for production where /tmp is a separate tmpfs mount")
		return
	}
	// On proper Linux setups this should be EXDEV.
	t.Logf("os.Rename /dev/shm→/var/tmp failed as expected: %v", err)

	// Verify that staging on the SAME directory as the target works fine.
	sameDeviceStaged := filepath.Join(varTmp, "nexus3-rename-test-staged")
	if err := os.WriteFile(sameDeviceStaged, []byte("agent binary"), 0755); err != nil {
		t.Fatalf("write same-device staged: %v", err)
	}
	defer os.Remove(sameDeviceStaged)

	if err := os.Rename(sameDeviceStaged, rootfsTarget); err != nil {
		t.Errorf("same-device rename must succeed, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AgentInfo control-server unit test
// ─────────────────────────────────────────────────────────────────────────────

func TestControlServer_AgentInfo(t *testing.T) {
	orig := agentBuildTag
	agentBuildTag = "test-20260829-deadbeef"
	defer func() { agentBuildTag = orig }()

	cs := newControlServer(&Agent{sessions: newSessionTable(), copies: newCopyTable()})
	resp, err := cs.AgentInfo(nil, nil)
	if err != nil {
		t.Fatalf("AgentInfo: unexpected error: %v", err)
	}
	if resp.BuildTag != "test-20260829-deadbeef" {
		t.Errorf("BuildTag: got %q, want %q", resp.BuildTag, "test-20260829-deadbeef")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RestartAgent: active sessions without force → error
// ─────────────────────────────────────────────────────────────────────────────

func TestControlServer_RestartAgent_ActiveSessionsNoForce(t *testing.T) {
	a := &Agent{sessions: newSessionTable(), copies: newCopyTable()}
	a.sessions.add(&Session{id: "s1", exitCh: make(chan int32, 1)})

	cs := newControlServer(a)

	oldFns := swapFunctions
	defer func() { swapFunctions = oldFns }()
	swapFunctions = swapFns{
		statSize:      func(path string) (int64, error) { return 100, nil },
		fsyncPath:     func(path string) error { return nil },
		renameAtomic:  func(src, dst string) error { return nil },
		dupListenerFd: func(l net.Listener) (int, error) { return 42, nil },
		execSelf:      func(argv []string, env []string) error { return nil },
	}

	_, err := cs.RestartAgent(nil, &agentpb_RestartAgentRequest{
		StagedPath:    "/sbin/.nexus3-agent.upgrade",
		ExpectedBytes: 100,
		Force:         false,
	})
	if err == nil {
		t.Fatal("expected error for active sessions without force, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RestartAgent: active copies without force → error
// ─────────────────────────────────────────────────────────────────────────────

func TestControlServer_RestartAgent_ActiveCopiesNoForce(t *testing.T) {
	a := &Agent{sessions: newSessionTable(), copies: newCopyTable()}
	a.copies.add(&pendingCopy{transferID: "copy-1"}) // in-flight copy

	cs := newControlServer(a)

	oldFns := swapFunctions
	defer func() { swapFunctions = oldFns }()
	swapFunctions = swapFns{
		statSize:      func(path string) (int64, error) { return 100, nil },
		fsyncPath:     func(path string) error { return nil },
		renameAtomic:  func(src, dst string) error { return nil },
		dupListenerFd: func(l net.Listener) (int, error) { return 42, nil },
		execSelf:      func(argv []string, env []string) error { return nil },
	}

	_, err := cs.RestartAgent(nil, &agentpb_RestartAgentRequest{
		StagedPath:    "/sbin/.nexus3-agent.upgrade",
		ExpectedBytes: 100,
		Force:         false,
	})
	if err == nil {
		t.Fatal("expected error for active copies without force, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RestartAgent: force overrides active sessions
// ─────────────────────────────────────────────────────────────────────────────

func TestControlServer_RestartAgent_ForceOverridesActiveSessions(t *testing.T) {
	a := &Agent{
		sessions: newSessionTable(),
		copies:   newCopyTable(),
		ctrlLis:  &stubListener{},
		dataLis:  &stubListener{},
	}
	a.sessions.add(&Session{id: "s1", exitCh: make(chan int32, 1)})

	cs := newControlServer(a)

	var execCalled bool
	oldFns := swapFunctions
	defer func() { swapFunctions = oldFns }()
	swapFunctions = swapFns{
		statSize:      func(path string) (int64, error) { return 100, nil },
		fsyncPath:     func(path string) error { return nil },
		renameAtomic:  func(src, dst string) error { return nil },
		dupListenerFd: func(l net.Listener) (int, error) { return 42, nil },
		execSelf: func(argv []string, env []string) error {
			execCalled = true
			return nil
		},
	}

	_, err := cs.RestartAgent(nil, &agentpb_RestartAgentRequest{
		StagedPath:    "/sbin/.nexus3-agent.upgrade",
		ExpectedBytes: 100,
		Force:         true,
	})
	if err != nil {
		t.Fatalf("expected no error with force=true, got %v", err)
	}
	if !execCalled {
		t.Error("exec must be called when force=true overrides active sessions")
	}
}

// agentpb_RestartAgentRequest is a type alias to keep test imports clean.
type agentpb_RestartAgentRequest = agentpb.RestartAgentRequest
