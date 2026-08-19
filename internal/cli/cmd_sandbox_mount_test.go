package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// parseMountNamed tests

// TestParseMountNamed_basic tests the happy path: volume:path format.
func TestParseMountNamed_basic(t *testing.T) {
	m, err := parseMountNamed("data:/mnt/data")
	if err != nil {
		t.Fatalf("parseMountNamed: %v", err)
	}
	if m.Name != "data" {
		t.Errorf("Name: got %q, want %q", m.Name, "data")
	}
	if m.GuestPath != "/mnt/data" {
		t.Errorf("GuestPath: got %q, want %q", m.GuestPath, "/mnt/data")
	}
	if m.Kind != volumestore.KindDisk {
		t.Errorf("Kind: got %v, want %v", m.Kind, volumestore.KindDisk)
	}
	if m.ReadOnly {
		t.Errorf("ReadOnly: got true, want false")
	}
	if m.SizeBytes != 0 {
		t.Errorf("SizeBytes: got %d, want 0", m.SizeBytes)
	}
}

// TestParseMountNamed_readOnly tests the :ro option.
func TestParseMountNamed_readOnly(t *testing.T) {
	m, err := parseMountNamed("data:/mnt/data:ro")
	if err != nil {
		t.Fatalf("parseMountNamed: %v", err)
	}
	if !m.ReadOnly {
		t.Errorf("ReadOnly: got false, want true")
	}
	if m.Kind != volumestore.KindDisk {
		t.Errorf("Kind: got %v, want %v", m.Kind, volumestore.KindDisk)
	}
}

// TestParseMountNamed_kindDir tests the kind=dir option.
func TestParseMountNamed_kindDir(t *testing.T) {
	m, err := parseMountNamed("data:/mnt/data:kind=dir")
	if err != nil {
		t.Fatalf("parseMountNamed: %v", err)
	}
	if m.Kind != volumestore.KindDir {
		t.Errorf("Kind: got %v, want %v", m.Kind, volumestore.KindDir)
	}
}

// TestParseMountNamed_size_10g tests the size=10g option.
func TestParseMountNamed_size_10g(t *testing.T) {
	m, err := parseMountNamed("data:/mnt/data:size=10g")
	if err != nil {
		t.Fatalf("parseMountNamed: %v", err)
	}
	expected := int64(10) << 30
	if m.SizeBytes != expected {
		t.Errorf("SizeBytes: got %d, want %d", m.SizeBytes, expected)
	}
}

// TestParseMountNamed_size_5g tests the size=5g option.
func TestParseMountNamed_size_5g(t *testing.T) {
	m, err := parseMountNamed("data:/mnt/data:size=5g")
	if err != nil {
		t.Fatalf("parseMountNamed: %v", err)
	}
	expected := int64(5) << 30
	if m.SizeBytes != expected {
		t.Errorf("SizeBytes: got %d, want %d", m.SizeBytes, expected)
	}
}

// TestParseMountNamed_multiple_options tests combining ro and size.
func TestParseMountNamed_multiple_options(t *testing.T) {
	m, err := parseMountNamed("data:/mnt/data:ro,size=5g")
	if err != nil {
		t.Fatalf("parseMountNamed: %v", err)
	}
	if !m.ReadOnly {
		t.Errorf("ReadOnly: got false, want true")
	}
	expected := int64(5) << 30
	if m.SizeBytes != expected {
		t.Errorf("SizeBytes: got %d, want %d", m.SizeBytes, expected)
	}
}

// TestParseMountNamed_git_terminal rejects .git as a terminal path component.
func TestParseMountNamed_git_terminal(t *testing.T) {
	_, err := parseMountNamed("x:/workspace/.git")
	if err == nil {
		t.Fatal("parseMountNamed: expected error for .git component, got nil")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}
}

// TestParseMountNamed_git_non_terminal rejects .git in a non-terminal position.
func TestParseMountNamed_git_non_terminal(t *testing.T) {
	_, err := parseMountNamed("x:/.git/hooks")
	if err == nil {
		t.Fatal("parseMountNamed: expected error for .git component, got nil")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}
}

// TestParseMountNamed_git_nested rejects .git in a nested path.
func TestParseMountNamed_git_nested(t *testing.T) {
	_, err := parseMountNamed("x:/a/.git/b")
	if err == nil {
		t.Fatal("parseMountNamed: expected error for .git component, got nil")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}
}

// TestParseMountNamed_missing_name rejects specs with no name.
func TestParseMountNamed_missing_name(t *testing.T) {
	_, err := parseMountNamed(":/mnt/data")
	if err == nil {
		t.Fatal("parseMountNamed: expected error for missing name, got nil")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}
}

// TestParseMountNamed_missing_path rejects specs with no path.
func TestParseMountNamed_missing_path(t *testing.T) {
	_, err := parseMountNamed("data:")
	if err == nil {
		t.Fatal("parseMountNamed: expected error for missing path, got nil")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}
}

// TestParseMountNamed_no_colon rejects specs with no colon.
func TestParseMountNamed_no_colon(t *testing.T) {
	_, err := parseMountNamed("nocolon")
	if err == nil {
		t.Fatal("parseMountNamed: expected error for missing colon, got nil")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}
}

// TestParseMountNamed_invalid_size rejects non-numeric size values.
func TestParseMountNamed_invalid_size(t *testing.T) {
	_, err := parseMountNamed("data:/mnt/data:size=xg")
	if err == nil {
		t.Fatal("parseMountNamed: expected error for invalid size, got nil")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}
}

// TestParseMountNamed_unknown_option rejects unrecognized options.
func TestParseMountNamed_unknown_option(t *testing.T) {
	_, err := parseMountNamed("data:/mnt/data:unknown=value")
	if err == nil {
		t.Fatal("parseMountNamed: expected error for unknown option, got nil")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}
}

// hasGitComponent tests

// TestHasGitComponent_terminal tests detection of .git as a terminal component.
func TestHasGitComponent_terminal(t *testing.T) {
	if !hasGitComponent(".git") {
		t.Errorf(".git: got false, want true")
	}
}

// TestHasGitComponent_hooks tests detection of .git in a nested path.
func TestHasGitComponent_hooks(t *testing.T) {
	if !hasGitComponent(".git/hooks") {
		t.Errorf(".git/hooks: got false, want true")
	}
}

// TestHasGitComponent_work_git tests detection of .git in work/.git.
func TestHasGitComponent_work_git(t *testing.T) {
	if !hasGitComponent("work/.git") {
		t.Errorf("work/.git: got false, want true")
	}
}

// TestHasGitComponent_gitignore rejects .gitignore.
func TestHasGitComponent_gitignore(t *testing.T) {
	if hasGitComponent("work/.gitignore") {
		t.Errorf("work/.gitignore: got true, want false")
	}
}

// TestHasGitComponent_workspace rejects plain workspace path.
func TestHasGitComponent_workspace(t *testing.T) {
	if hasGitComponent("/workspace") {
		t.Errorf("/workspace: got true, want false")
	}
}

// TestHasGitComponent_empty rejects empty string.
func TestHasGitComponent_empty(t *testing.T) {
	if hasGitComponent("") {
		t.Errorf("empty string: got true, want false")
	}
}

// TestHasGitComponent_deep_nesting tests detection in deeply nested path.
func TestHasGitComponent_deep_nesting(t *testing.T) {
	if !hasGitComponent("/foo/bar/.git/objects") {
		t.Errorf("/foo/bar/.git/objects: got false, want true")
	}
}

// parseVolumeSize tests

// TestParseVolumeSize_10g tests parsing 10g (gigabytes).
func TestParseVolumeSize_10g(t *testing.T) {
	v, err := parseVolumeSize("10g")
	if err != nil {
		t.Fatalf("parseVolumeSize(10g): %v", err)
	}
	expected := int64(10) << 30
	if v != expected {
		t.Errorf("10g: got %d, want %d", v, expected)
	}
}

// TestParseVolumeSize_512m tests parsing 512m (megabytes).
func TestParseVolumeSize_512m(t *testing.T) {
	v, err := parseVolumeSize("512m")
	if err != nil {
		t.Fatalf("parseVolumeSize(512m): %v", err)
	}
	expected := int64(512) << 20
	if v != expected {
		t.Errorf("512m: got %d, want %d", v, expected)
	}
}

// TestParseVolumeSize_1024k tests parsing 1024k (kilobytes).
func TestParseVolumeSize_1024k(t *testing.T) {
	v, err := parseVolumeSize("1024k")
	if err != nil {
		t.Fatalf("parseVolumeSize(1024k): %v", err)
	}
	expected := int64(1024) << 10
	if v != expected {
		t.Errorf("1024k: got %d, want %d", v, expected)
	}
}

// TestParseVolumeSize_bytes tests parsing plain byte count.
func TestParseVolumeSize_bytes(t *testing.T) {
	v, err := parseVolumeSize("4096")
	if err != nil {
		t.Fatalf("parseVolumeSize(4096): %v", err)
	}
	if v != 4096 {
		t.Errorf("4096: got %d, want 4096", v)
	}
}

// TestParseVolumeSize_empty rejects empty string.
func TestParseVolumeSize_empty(t *testing.T) {
	_, err := parseVolumeSize("")
	if err == nil {
		t.Fatal("parseVolumeSize(\"\"): expected error, got nil")
	}
}

// TestParseVolumeSize_invalid_gig rejects non-numeric gigabyte value.
func TestParseVolumeSize_invalid_gig(t *testing.T) {
	_, err := parseVolumeSize("xg")
	if err == nil {
		t.Fatal("parseVolumeSize(xg): expected error, got nil")
	}
}

// TestParseVolumeSize_invalid_meg rejects non-numeric megabyte value.
func TestParseVolumeSize_invalid_meg(t *testing.T) {
	_, err := parseVolumeSize("abcm")
	if err == nil {
		t.Fatal("parseVolumeSize(abcm): expected error, got nil")
	}
}

// TestParseVolumeSize_uppercase_G tests parsing uppercase G suffix.
func TestParseVolumeSize_uppercase_G(t *testing.T) {
	v, err := parseVolumeSize("10G")
	if err != nil {
		t.Fatalf("parseVolumeSize(10G): %v", err)
	}
	expected := int64(10) << 30
	if v != expected {
		t.Errorf("10G: got %d, want %d", v, expected)
	}
}

// TestParseVolumeSize_uppercase_M tests parsing uppercase M suffix.
func TestParseVolumeSize_uppercase_M(t *testing.T) {
	v, err := parseVolumeSize("512M")
	if err != nil {
		t.Fatalf("parseVolumeSize(512M): %v", err)
	}
	expected := int64(512) << 20
	if v != expected {
		t.Errorf("512M: got %d, want %d", v, expected)
	}
}

// TestParseVolumeSize_uppercase_K tests parsing uppercase K suffix.
func TestParseVolumeSize_uppercase_K(t *testing.T) {
	v, err := parseVolumeSize("1024K")
	if err != nil {
		t.Fatalf("parseVolumeSize(1024K): %v", err)
	}
	expected := int64(1024) << 10
	if v != expected {
		t.Errorf("1024K: got %d, want %d", v, expected)
	}
}

// ── parseMountLive tests ──────────────────────────────────────────────────────

// TestParseMountLive_basic verifies that a well-formed host:guest spec returns
// the resolved absolute host path and the guest path with ReadOnly=false.
func TestParseMountLive_basic(t *testing.T) {
	dir := t.TempDir()
	spec := dir + ":/workspace"
	lm, err := parseMountLive(spec)
	if err != nil {
		t.Fatalf("parseMountLive(%q): unexpected error: %v", spec, err)
	}
	abs, _ := filepath.Abs(dir)
	if lm.HostPath != abs {
		t.Errorf("HostPath: got %q, want %q", lm.HostPath, abs)
	}
	if lm.GuestPath != "/workspace" {
		t.Errorf("GuestPath: got %q, want %q", lm.GuestPath, "/workspace")
	}
	if lm.ReadOnly {
		t.Error("ReadOnly: got true, want false")
	}
}

// TestParseMountLive_readOnly verifies that :ro sets ReadOnly=true.
func TestParseMountLive_readOnly(t *testing.T) {
	dir := t.TempDir()
	spec := dir + ":/workspace:ro"
	lm, err := parseMountLive(spec)
	if err != nil {
		t.Fatalf("parseMountLive(%q): unexpected error: %v", spec, err)
	}
	if !lm.ReadOnly {
		t.Error("ReadOnly: got false, want true")
	}
}

// TestParseMountLive_GitComponentAccepted_D_PD_99 proves that a guest path
// containing a .git component is ACCEPTED by parseMountLive (D-PD-99).
//
// This is a deliberate divergence from parseMountNamed, which hard-refuses .git
// components (design line 63, SD2-6-MOUNT). Live mounts exist precisely to
// share a real worktree — including its .git directory — into the guest.
func TestParseMountLive_GitComponentAccepted_D_PD_99(t *testing.T) {
	// Host side: create a temp dir with a nested .git subdirectory to simulate
	// a real worktree root being mounted.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll .git: %v", err)
	}

	// Guest path with .git as a non-terminal component — must not be refused.
	spec := dir + ":/workspace/repo/.git"
	lm, err := parseMountLive(spec)
	if err != nil {
		t.Fatalf("parseMountLive with .git guest path: unexpected error: %v — D-PD-99 requires this to be accepted", err)
	}
	if lm.GuestPath != "/workspace/repo/.git" {
		t.Errorf("GuestPath: got %q, want %q", lm.GuestPath, "/workspace/repo/.git")
	}
}

// TestParseMountLive_HostNotDirectory verifies that a regular file is rejected
// (virtiofs shares directories, not files).
func TestParseMountLive_HostNotDirectory(t *testing.T) {
	f, err := os.CreateTemp("", "nexus3-test-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	spec := f.Name() + ":/workspace"
	_, gotErr := parseMountLive(spec)
	if gotErr == nil {
		t.Fatalf("parseMountLive(%q): expected error for regular file, got nil", spec)
	}
	var ue *UsageError
	if !errors.As(gotErr, &ue) {
		t.Fatalf("expected UsageError, got %T: %v", gotErr, gotErr)
	}
}

// TestParseMountLive_HostNotExist verifies that a non-existent host path returns
// a UsageError.
func TestParseMountLive_HostNotExist(t *testing.T) {
	spec := "/this/path/does/not/exist:/workspace"
	_, gotErr := parseMountLive(spec)
	if gotErr == nil {
		t.Fatalf("parseMountLive(%q): expected error, got nil", spec)
	}
	var ue *UsageError
	if !errors.As(gotErr, &ue) {
		t.Fatalf("expected UsageError, got %T: %v", gotErr, gotErr)
	}
}

// TestParseMountLive_MalformedSpec verifies that a spec missing the guest path
// returns a UsageError with a "want <host-path>:<guest-path>[:ro]" form message.
func TestParseMountLive_MalformedSpec(t *testing.T) {
	for _, spec := range []string{"", "onlyone", ":"} {
		_, gotErr := parseMountLive(spec)
		if gotErr == nil {
			t.Errorf("parseMountLive(%q): expected error, got nil", spec)
			continue
		}
		var ue *UsageError
		if !errors.As(gotErr, &ue) {
			t.Errorf("parseMountLive(%q): expected UsageError, got %T: %v", spec, gotErr, gotErr)
		}
	}
}

// TestLiveMountsToGuestMounts_TagAgreement proves that the virtiofs tag
// emitted in the guest cmdline argument (via liveMountsToGuestMounts) matches
// the tag the driver derives at Start time using VirtiofsTag(idx).
//
// This is the exact silent-mismatch failure VirtiofsTag exists to prevent:
// if either side diverges from the shared helper the tag strings differ and
// every live mount fails at boot with no actionable error.
//
// The expected tag is derived here as a concrete literal (fmt.Sprintf("nx3fs%d",
// i)) rather than by calling VirtiofsTag — so the test FAILS if VirtiofsTag
// or liveMountsToGuestMounts changes the format, instead of silently passing
// because both sides of the comparison call the same helper.
func TestLiveMountsToGuestMounts_TagAgreement(t *testing.T) {
	mounts := []domain.LiveMount{
		{HostPath: "/host/a", GuestPath: "/guest/a", ReadOnly: false},
		{HostPath: "/host/b", GuestPath: "/guest/b", ReadOnly: true},
		{HostPath: "/host/c", GuestPath: "/guest/c", ReadOnly: false},
	}

	guestMounts := liveMountsToGuestMounts(mounts)
	if len(guestMounts) != len(mounts) {
		t.Fatalf("len: got %d, want %d", len(guestMounts), len(mounts))
	}

	for i, gm := range guestMounts {
		// Derive expected tag as a concrete literal — NOT by calling VirtiofsTag.
		// If VirtiofsTag changes its format, liveMountsToGuestMounts will produce
		// a different string and this assertion will catch the divergence.
		wantTag := fmt.Sprintf("nx3fs%d", i)
		if gm.Device != wantTag {
			t.Errorf("mount[%d].Device = %q; want %q (VirtiofsTag format = \"nx3fs<idx>\")", i, gm.Device, wantTag)
		}
		if gm.FSType != "virtiofs" {
			t.Errorf("mount[%d].FSType = %q; want %q", i, gm.FSType, "virtiofs")
		}
		if gm.Target != mounts[i].GuestPath {
			t.Errorf("mount[%d].Target = %q; want %q", i, gm.Target, mounts[i].GuestPath)
		}
		if gm.ReadOnly != mounts[i].ReadOnly {
			t.Errorf("mount[%d].ReadOnly = %v; want %v", i, gm.ReadOnly, mounts[i].ReadOnly)
		}
		if gm.IsWorkspace {
			t.Errorf("mount[%d].IsWorkspace = true; live mounts must not claim workspace role", i)
		}
	}
}

// ── no-boot path mount guard ──────────────────────────────────────────────────

// TestSandboxCreate_MountOnNoBootPath_UsageError proves that --mount on the
// no-boot create path (no --image/--rootfs/--file) is rejected with a UsageError
// that names the flag and instructs the user to use a boot flag.
//
// Regression guard for the silent-drop footgun: previously the no-boot branch
// returned nil after store.Create, dropping every --mount spec with RC=0.
func TestSandboxCreate_MountOnNoBootPath_UsageError(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(true)

	err := runSandboxCreate(context.Background(),
		[]string{"proj/box", "--mount", "/nonexistent-host:/guest"},
		out, svc)
	if err == nil {
		t.Fatal("expected UsageError, got nil — --mount was silently dropped on no-boot path")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(ue.Msg, "--mount") {
		t.Errorf("error should name --mount; got %q", ue.Msg)
	}
	if !strings.Contains(ue.Msg, "--image") {
		t.Errorf("error should name --image as a remedy; got %q", ue.Msg)
	}
}

// TestSandboxCreate_MountNamedOnNoBootPath_UsageError proves the same guard for
// --mount-named: store-only create must reject it rather than drop it silently.
func TestSandboxCreate_MountNamedOnNoBootPath_UsageError(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(true)

	err := runSandboxCreate(context.Background(),
		[]string{"proj/box", "--mount-named", "myvol:/guest"},
		out, svc)
	if err == nil {
		t.Fatal("expected UsageError, got nil — --mount-named was silently dropped on no-boot path")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(ue.Msg, "--mount-named") {
		t.Errorf("error should name --mount-named; got %q", ue.Msg)
	}
	if !strings.Contains(ue.Msg, "--image") {
		t.Errorf("error should name --image as a remedy; got %q", ue.Msg)
	}
}
