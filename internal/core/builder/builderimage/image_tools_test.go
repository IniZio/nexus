//go:build linux

package builderimage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/builder/builderimage"
	"github.com/IniZio/nexus/internal/core/builder/toolcache"
)

// fakeToolBytes is a small stub binary for staging tests.
var fakeToolBytes = []byte("#!/bin/sh\necho gh-fake\n")

// TestBuilderToolsCachePath_NilToolsMatchesBase asserts that nil tools yields
// the same cache path as the baseline (tools-less) helper.
func TestBuilderToolsCachePath_NilToolsMatchesBase(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	agent := []byte("fake-agent-bytes")
	ociDigest := "sha256:deadbeefdeadbeef"

	base := builderimage.BuilderImageCachePathForTest(dataDir, ociDigest, agent)
	withNil := builderimage.BuilderImageCachePathWithToolsForTest(dataDir, ociDigest, agent, nil)

	if base != withNil {
		t.Errorf("nil tools path differs from base:\n  base=%s\n  with=%s", base, withNil)
	}
}

// TestBuilderToolsCachePath_NonEmptyToolsDiffers asserts that non-empty tools
// produce a different cache path that still ends with -agent<16hex>.ext4.
func TestBuilderToolsCachePath_NonEmptyToolsDiffers(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	agent := []byte("fake-agent-bytes")
	ociDigest := "sha256:deadbeefdeadbeef"

	tools := []toolcache.Fetched{
		{Name: "gh", Version: "2.50.0", SHA256: "abc123", BinPath: "/tmp/gh"},
	}
	base := builderimage.BuilderImageCachePathForTest(dataDir, ociDigest, agent)
	withTools := builderimage.BuilderImageCachePathWithToolsForTest(dataDir, ociDigest, agent, tools)

	if base == withTools {
		t.Error("expected tools path to differ from base, but they are equal")
	}
	// Must keep -agent<16hex>.ext4 suffix so parseBuilderTemplateName still parses it.
	name := filepath.Base(withTools)
	if !strings.HasSuffix(name, ".ext4") {
		t.Errorf("path does not end in .ext4: %s", name)
	}
	// Locate -agent<hex> suffix (16 hex chars before .ext4).
	stem := strings.TrimSuffix(name, ".ext4")
	const agentSep = "-agent"
	idx := strings.LastIndex(stem, agentSep)
	if idx < 0 {
		t.Fatalf("no -agent separator in stem: %s", stem)
	}
	agentTag := stem[idx+len(agentSep):]
	if len(agentTag) != 16 {
		t.Errorf("agent tag length = %d, want 16: %q", len(agentTag), agentTag)
	}
	// The -tools<digest> segment must appear between -tc and -agent.
	if !strings.Contains(stem[:idx], "-tools") {
		t.Errorf("no -tools<digest> segment before -agent in: %s", stem)
	}
}

// TestStageBuilderTools_TreeExists verifies that stageBuilderTools places a
// tool binary at stagingDir/opt/nexus-sandbox-tools/root/usr/local/bin/gh.
// GuestBinPath and LinkPath must be absolute guest paths as required by StageTree.
func TestStageBuilderTools_TreeExists(t *testing.T) {
	t.Parallel()

	// Write a stub binary to a temp file so BinPath is valid.
	binFile := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(binFile, fakeToolBytes, 0o755); err != nil {
		t.Fatal(err)
	}

	stagingDir := t.TempDir()
	tools := []toolcache.Fetched{
		{
			Name:         "gh",
			Version:      "2.50.0",
			SHA256:       "abc",
			BinPath:      binFile,
			GuestBinPath: "/usr/local/bin/gh",
			LinkPath:     "/usr/bin/gh",
		},
	}
	if err := builderimage.StageBuilderToolsForTest(stagingDir, tools); err != nil {
		t.Fatalf("StageBuilderTools: %v", err)
	}

	wantPath := filepath.Join(stagingDir, toolcache.BuilderTreeDir, "usr", "local", "bin", "gh")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("gh binary not found at expected path %s: %v", wantPath, err)
	}
}
