package builder

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentLayerCacheKeyIsUniquePerSolve is the regression guard for the
// poisoned-agent-layer defect.
//
// # The defect
//
// buildkitd caches the result snapshot of the final
// `COPY --from=nexus3agent` under a key derived from the copied file's
// contenthash. With a fixed source filename that key is identical on every
// build of the same agent binary, so once a build wrote a CORRUPT (zero-byte)
// agent into that snapshot, every later build cache-hit the poisoned snapshot,
// exported it, and failed the verifyAgentIntegrity canary with
// "/sbin/nexus3-agent is 0 bytes, expected 36329665". Observed live on
// 2026-08-29: cache-disk slot 0 snapshot 61, zero-byte agent written 15:05,
// reproduced on demand in ~7 s with no layer re-executed. The build could not
// recover without deleting the operator's warm cache disk.
//
// # What this test pins
//
// The COPY instruction emitted into the synthetic Dockerfile must name a
// DIFFERENT source path on every Solve, so buildkit is forced to re-execute
// the agent layer instead of serving a persisted one.
//
// Mutation proof: restore a fixed name (e.g. make newAgentContextFilename
// return the constant agentContextFilenamePrefix) and this test fails on the
// "identical across builds" assertion.
func TestAgentLayerCacheKeyIsUniquePerSolve(t *testing.T) {
	containerfile := []byte("FROM ubuntu:24.04\nRUN echo hi\n")
	const installPath = "/sbin/nexus3-agent"

	const builds = 64
	seenNames := make(map[string]int, builds)
	seenDockerfiles := make(map[string]int, builds)

	for i := range builds {
		name, err := newAgentContextFilename()
		if err != nil {
			t.Fatalf("build %d: newAgentContextFilename: %v", i, err)
		}

		if !strings.HasPrefix(name, agentContextFilenamePrefix) {
			t.Fatalf("build %d: agent context filename %q lost the reserved prefix %q",
				i, name, agentContextFilenamePrefix)
		}
		if strings.ContainsAny(name, " \t\n/") {
			t.Fatalf("build %d: agent context filename %q contains a character that would "+
				"break the COPY instruction or escape the context dir", i, name)
		}
		if prev, dup := seenNames[name]; dup {
			t.Fatalf("agent context filename %q reused by builds %d and %d — the agent "+
				"layer's buildkit cache key is stable across builds, so a poisoned "+
				"(zero-byte) agent layer would be served forever", name, prev, i)
		}
		seenNames[name] = i

		df := string(synthesizeDockerfile(containerfile, name, installPath))

		if !strings.HasPrefix(df, string(containerfile)) {
			t.Fatalf("build %d: synthesized Dockerfile does not start with the user's Containerfile:\n%s", i, df)
		}
		copyLine := "COPY --chmod=0755 --from=nexus3agent " + name + " " + installPath
		if !strings.Contains(df, copyLine) {
			t.Fatalf("build %d: synthesized Dockerfile does not COPY the per-build agent file.\nwant line: %s\ngot:\n%s",
				i, copyLine, df)
		}
		if prev, dup := seenDockerfiles[df]; dup {
			t.Fatalf("builds %d and %d produced byte-identical synthetic Dockerfiles — the "+
				"agent COPY step is cacheable across builds and a poisoned agent "+
				"layer cannot be evicted", prev, i)
		}
		seenDockerfiles[df] = i
	}
}

// TestStagedAgentFileIsTheCopySource pins the coupling between the name the
// agent binary is actually WRITTEN under in the nexus3agent context dir and
// the name the COPY instruction reads. If these diverge, every build fails
// with "file not found" and the failure is mistaken for a buildkit fault.
//
// It exercises the real seam (stageAgentContext on a real temp dir), not
// just synthesizeDockerfile: the file named in the COPY line must be the ONE
// file present in the staged dir, byte-identical to the source agent.
//
// Mutation proof: make stageAgentContext write under agentContextFilenamePrefix
// while still returning the nonce name (or vice versa) and this test fails.
func TestStagedAgentFileIsTheCopySource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "agent.bin")
	want := []byte("fake-agent-bytes-0123456789")
	if err := os.WriteFile(src, want, 0755); err != nil {
		t.Fatal(err)
	}
	agentDir := t.TempDir()

	agentFile, err := stageAgentContext(agentDir, src)
	if err != nil {
		t.Fatalf("stageAgentContext: %v", err)
	}
	df := string(synthesizeDockerfile([]byte("FROM scratch\n"), agentFile, "/sbin/nexus3-agent"))
	copyLine := "COPY --chmod=0755 --from=nexus3agent " + agentFile + " /sbin/nexus3-agent"
	if !strings.Contains(df, copyLine) {
		t.Fatalf("COPY line does not reference the staged file.\nwant: %s\ngot:\n%s", copyLine, df)
	}

	entries, err := os.ReadDir(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != agentFile {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("staged agent dir must contain exactly the COPY source %q; got %v", agentFile, names)
	}
	got, err := os.ReadFile(filepath.Join(agentDir, agentFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("staged agent bytes differ from source: got %d bytes, want %d", len(got), len(want))
	}
}
