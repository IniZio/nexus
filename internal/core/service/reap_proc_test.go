package service

// Internal tests for the proc scan liveness gate. These live in package
// service (not service_test) because they exercise unexported types and
// helpers (procScanResult, checkPIDCmdline, scanProcForULID).
//
// N-AC2 requires that ANY ambiguity resolves to KEEP. The tests here prove
// that each ambiguous case (unreadable file, vanished PID, truncated cmdline)
// returns procScanAmbiguous rather than procScanDead.
//
// Each test is designed to FAIL against the old procContainsULID(bool)
// implementation: old code returned false (dead) in all three cases.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testULID = "sb-06FZZX7V8XZM12YE7VTR7T8168"

// TestCheckPIDCmdline_Live verifies that a cmdline containing the ULID returns procScanLive.
func TestCheckPIDCmdline_Live(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cmdline")
	// Write cmdline with the ULID embedded, separated by NUL bytes.
	content := "cloud-hypervisor\x00--api-socket\x00" + testULID + ".sock\x00"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	if got := checkPIDCmdline(path, testULID); got != procScanLive {
		t.Errorf("checkPIDCmdline with matching ULID: got %v, want procScanLive", got)
	}
}

// TestCheckPIDCmdline_Dead verifies that a fully-read cmdline without the
// ULID returns procScanDead.
func TestCheckPIDCmdline_Dead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cmdline")
	content := "/usr/bin/bash\x00-c\x00echo hello\x00"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	if got := checkPIDCmdline(path, testULID); got != procScanDead {
		t.Errorf("checkPIDCmdline without ULID: got %v, want procScanDead", got)
	}
}

// TestCheckPIDCmdline_PermissionDenied verifies that an unreadable cmdline
// (EACCES) returns procScanAmbiguous, not procScanDead.
//
// This test FAILS against the old implementation (which returned false = dead
// on any read error via the `continue` path in procContainsULID).
func TestCheckPIDCmdline_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read any file; permission-denied test is not meaningful as root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(path, []byte("some-process"), 0600); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	// Make the file unreadable.
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	defer func() { _ = os.Chmod(path, 0600) }() // restore so TempDir can clean up

	if got := checkPIDCmdline(path, testULID); got != procScanAmbiguous {
		t.Errorf("checkPIDCmdline with 000 perms: got %v, want procScanAmbiguous (N-AC2: ambiguity = keep)", got)
	}
}

// TestCheckPIDCmdline_VanishedPID verifies that a non-existent cmdline path
// (PID vanished between ReadDir and read, like ESRCH) returns procScanAmbiguous.
//
// This test FAILS against the old implementation (which returned false = dead
// via `continue` when readUpTo returned an error).
func TestCheckPIDCmdline_VanishedPID(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "vanished", "cmdline")
	if got := checkPIDCmdline(nonExistent, testULID); got != procScanAmbiguous {
		t.Errorf("checkPIDCmdline with missing path: got %v, want procScanAmbiguous (N-AC2: vanished PID may have been our process)", got)
	}
}

// TestCheckPIDCmdline_Truncated verifies that a cmdline that fills the read
// limit (ULID not found in what was read) returns procScanAmbiguous, because
// the ULID might be beyond the truncation point.
//
// This test FAILS against the old implementation (which read only 4096 bytes
// and returned false = dead when the ULID wasn't found in that window, even
// if the cmdline was cut off mid-argument).
func TestCheckPIDCmdline_Truncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cmdline")

	// Write a cmdline larger than maxCmdlineRead. The ULID does not appear
	// anywhere in the content, simulating a long cmdline where the ULID would
	// be beyond the cutoff.
	longContent := strings.Repeat("x", maxCmdlineRead+1)
	if err := os.WriteFile(path, []byte(longContent), 0600); err != nil {
		t.Fatalf("write big cmdline: %v", err)
	}
	if got := checkPIDCmdline(path, testULID); got != procScanAmbiguous {
		t.Errorf("checkPIDCmdline with truncated read: got %v, want procScanAmbiguous (ULID may be beyond cutoff)", got)
	}
}

// TestScanProcForULID_ReadDirFailure verifies that a scanProcForULID call
// against a non-existent procDir returns procScanAmbiguous (not procScanDead).
//
// This test FAILS against the old implementation (which returned false = dead
// when os.ReadDir("/proc") failed).
func TestScanProcForULID_ReadDirFailure(t *testing.T) {
	nonExistentDir := filepath.Join(t.TempDir(), "no-such-proc")
	if got := scanProcForULID(nonExistentDir, testULID); got != procScanAmbiguous {
		t.Errorf("scanProcForULID with missing procDir: got %v, want procScanAmbiguous", got)
	}
}

// TestScanProcForULID_AmbiguousEntryPropagates verifies that if any PID entry
// is ambiguous, the overall scan is ambiguous — even if no LIVE entry is found.
func TestScanProcForULID_AmbiguousEntryPropagates(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read any file; permission test not meaningful as root")
	}
	procDir := t.TempDir()

	// PID 1: readable, does not contain ULID → dead
	pid1Dir := filepath.Join(procDir, "1")
	if err := os.Mkdir(pid1Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pid1Dir, "cmdline"), []byte("init"), 0600); err != nil {
		t.Fatal(err)
	}

	// PID 2: unreadable → ambiguous
	pid2Dir := filepath.Join(procDir, "2")
	if err := os.Mkdir(pid2Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pid2Dir, "cmdline"), []byte("systemd"), 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filepath.Join(pid2Dir, "cmdline"), 0600) }()

	if got := scanProcForULID(procDir, testULID); got != procScanAmbiguous {
		t.Errorf("scanProcForULID with one ambiguous PID: got %v, want procScanAmbiguous", got)
	}
}

// TestScanProcForULID_LiveFoundDespiteAmbiguous verifies that a LIVE result
// is still returned even when some entries are ambiguous — we don't need a
// complete scan to confirm liveness.
func TestScanProcForULID_LiveFoundDespiteAmbiguous(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read any file; permission test not meaningful as root")
	}
	procDir := t.TempDir()

	// PID 1: unreadable
	pid1Dir := filepath.Join(procDir, "1")
	if err := os.Mkdir(pid1Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pid1Dir, "cmdline"), []byte("something"), 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filepath.Join(pid1Dir, "cmdline"), 0600) }()

	// PID 2: contains ULID → live
	pid2Dir := filepath.Join(procDir, "2")
	if err := os.Mkdir(pid2Dir, 0700); err != nil {
		t.Fatal(err)
	}
	content := "cloud-hypervisor\x00--api-socket\x00" + testULID + ".sock\x00"
	if err := os.WriteFile(filepath.Join(pid2Dir, "cmdline"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	if got := scanProcForULID(procDir, testULID); got != procScanLive {
		t.Errorf("scanProcForULID with live PID present: got %v, want procScanLive", got)
	}
}
