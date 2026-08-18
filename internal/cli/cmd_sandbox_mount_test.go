package cli

import (
	"errors"
	"testing"

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
