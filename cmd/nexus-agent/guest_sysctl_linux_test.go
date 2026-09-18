//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeSysctlFile(t *testing.T, root, rel string, val int64) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strconv.FormatInt(val, 10)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSysctlFile(t *testing.T, root, rel string) int64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestApplyGuestSysctls_SetsValues(t *testing.T) {
	root := t.TempDir()
	for _, tgt := range guestInotifyTargets {
		writeSysctlFile(t, root, tgt.rel, 100)
	}

	if err := applyGuestSysctls(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, tgt := range guestInotifyTargets {
		got := readSysctlFile(t, root, tgt.rel)
		if got != tgt.floor {
			t.Errorf("%s: got %d, want %d", tgt.rel, got, tgt.floor)
		}
	}
}

func TestApplyGuestSysctls_LeavesHigherValue(t *testing.T) {
	root := t.TempDir()
	for _, tgt := range guestInotifyTargets {
		writeSysctlFile(t, root, tgt.rel, tgt.floor+1000)
	}

	if err := applyGuestSysctls(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, tgt := range guestInotifyTargets {
		got := readSysctlFile(t, root, tgt.rel)
		if got != tgt.floor+1000 {
			t.Errorf("%s: higher value was lowered: got %d, want %d", tgt.rel, got, tgt.floor+1000)
		}
	}
}

func TestApplyGuestSysctls_ToleratesMissingFile(t *testing.T) {
	root := t.TempDir()
	if err := applyGuestSysctls(root); err != nil {
		t.Fatalf("missing files must be silently skipped, got: %v", err)
	}
}
