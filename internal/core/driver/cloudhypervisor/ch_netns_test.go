package cloudhypervisor

// ch_netns_test.go — tests for the netns-runtime mechanism (ch_netns.go).
//
// TestMain: re-exec dispatch — when NEXUS3_NETNS_RUN=1, runs RunNetnsChild()
// so the test binary itself acts as the re-exec image for StartNetnsRuntime.
//
// Unit tests (always compiled, no build-tag guards):
//   TestNetnsSocketpairFiles         — socketpair creation + fd split
//   TestNetnsChildAttr               — SysProcAttr Cloneflags/maps/setgroups
//   TestNetnsSocketpairCloseOrdering — parent closes pumpFile, child end ok
//
// The VM-booting netns-runtime tests that used to live here now sit in
// ch_netns_runtime_integration_test.go behind `//go:build integration`
// (D-HSH-20). They were untagged and merely runtime-skipped on an empty
// testdata/, which is gitignored — so `make test` was green or red for the
// same commit depending on whether scripts/fetch-boot-artifacts.sh had been
// run locally. Reach them with `make test-integration`.
//
// TestMain stays untagged on purpose: untagged files are compiled into the
// integration build as well, so the test binary remains its own
// NEXUS3_NETNS_RUN re-exec image for both runs.

import (
	"os"
	"syscall"
	"testing"
)

// TestMain checks the netns re-exec sentinel before running tests.
// When NEXUS3_NETNS_RUN=1 this process is the child inside the user+net ns;
// call RunNetnsChild() and exit — do not run any test functions.
//
// S1: wire this sentinel dispatch into cmd/nexus3/main.go
func TestMain(m *testing.M) {
	if os.Getenv(NetnsRunEnv) == "1" {
		RunNetnsChild()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// ── unit tests ────────────────────────────────────────────────────────────────

// TestNetnsSocketpairFiles verifies that netnsSocketpairFiles returns two
// distinct, valid *os.File descriptors (non-negative, non-identical fds).
func TestNetnsSocketpairFiles(t *testing.T) {
	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	t.Cleanup(func() {
		_ = perimFile.Close()
		_ = pumpFile.Close()
	})

	if perimFile == nil || pumpFile == nil {
		t.Fatal("expected both files to be non-nil")
	}
	perimFd := int(perimFile.Fd())
	pumpFd := int(pumpFile.Fd())
	if perimFd < 0 {
		t.Errorf("perimFile fd = %d, want >= 0", perimFd)
	}
	if pumpFd < 0 {
		t.Errorf("pumpFile fd = %d, want >= 0", pumpFd)
	}
	if perimFd == pumpFd {
		t.Errorf("perimFile and pumpFile have the same fd %d; want distinct fds", perimFd)
	}

	// Verify the socketpair is connected: write on pumpFile, read on perimFile.
	msg := []byte("netns-test")
	if _, err := syscall.Write(int(pumpFile.Fd()), msg); err != nil {
		t.Fatalf("write pumpFile: %v", err)
	}
	buf := make([]byte, 64)
	n, err := syscall.Read(int(perimFile.Fd()), buf)
	if err != nil {
		t.Fatalf("read perimFile: %v", err)
	}
	if string(buf[:n]) != string(msg) {
		t.Errorf("round-trip: got %q want %q", buf[:n], msg)
	}
}

// TestNetnsChildAttr verifies the SysProcAttr produced by netnsChildAttr.
//
//   - Cloneflags must include CLONE_NEWUSER and CLONE_NEWNET.
//   - UidMappings must have exactly one entry mapping ContainerID=0 → host uid.
//   - GidMappings must have exactly one entry mapping ContainerID=0 → host gid.
//   - GidMappingsEnableSetgroups must be false (setgroups=deny preserves kvm gid).
func TestNetnsChildAttr(t *testing.T) {
	attr := netnsChildAttr()

	if attr.Cloneflags&syscall.CLONE_NEWUSER == 0 {
		t.Error("Cloneflags missing CLONE_NEWUSER")
	}
	if attr.Cloneflags&syscall.CLONE_NEWNET == 0 {
		t.Error("Cloneflags missing CLONE_NEWNET")
	}
	if attr.GidMappingsEnableSetgroups {
		t.Error("GidMappingsEnableSetgroups must be false (setgroups=deny to preserve kvm gid)")
	}

	if len(attr.UidMappings) != 1 {
		t.Fatalf("UidMappings len = %d, want 1", len(attr.UidMappings))
	}
	if attr.UidMappings[0].ContainerID != 0 {
		t.Errorf("UidMappings[0].ContainerID = %d, want 0", attr.UidMappings[0].ContainerID)
	}
	if attr.UidMappings[0].HostID != os.Getuid() {
		t.Errorf("UidMappings[0].HostID = %d, want %d (host uid)", attr.UidMappings[0].HostID, os.Getuid())
	}
	if attr.UidMappings[0].Size != 1 {
		t.Errorf("UidMappings[0].Size = %d, want 1", attr.UidMappings[0].Size)
	}

	if len(attr.GidMappings) != 1 {
		t.Fatalf("GidMappings len = %d, want 1", len(attr.GidMappings))
	}
	if attr.GidMappings[0].ContainerID != 0 {
		t.Errorf("GidMappings[0].ContainerID = %d, want 0", attr.GidMappings[0].ContainerID)
	}
	if attr.GidMappings[0].HostID != os.Getgid() {
		t.Errorf("GidMappings[0].HostID = %d, want %d (host gid)", attr.GidMappings[0].HostID, os.Getgid())
	}
	if attr.GidMappings[0].Size != 1 {
		t.Errorf("GidMappings[0].Size = %d, want 1", attr.GidMappings[0].Size)
	}
}

// TestNetnsSocketpairCloseOrdering verifies the fd-close contract:
// after the parent closes its copy of pumpFile, the parent end (perimFile)
// remains readable and the pumpFile fd is invalid.
func TestNetnsSocketpairCloseOrdering(t *testing.T) {
	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	t.Cleanup(func() { _ = perimFile.Close() })

	// Write a frame on pumpFile before closing it.
	payload := []byte("close-ordering-test")
	if _, err := syscall.Write(int(pumpFile.Fd()), payload); err != nil {
		pumpFile.Close()
		t.Fatalf("write pumpFile: %v", err)
	}

	// Parent closes pumpFile — this is the post-spawn ordering step.
	pumpFile.Close()

	// perimFile (parent end) must still be readable; the datagram is buffered.
	buf := make([]byte, 64)
	n, err := syscall.Read(int(perimFile.Fd()), buf)
	if err != nil {
		t.Fatalf("read perimFile after pumpFile.Close: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Errorf("read back %q; want %q", buf[:n], payload)
	}
}
