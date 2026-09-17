package builder

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

func TestSynthesizeDockerfile_RuncShimAfterAgentCopy(t *testing.T) {
	const agentFile = "_nexus-agent-abc"
	const installPath = "/sbin/nexus-agent"
	df := string(synthesizeDockerfile([]byte("FROM scratch\n"), nil, agentFile, installPath, runcShimContextFilename))

	agentLine := "COPY --chmod=0755 --from=nexusagent " + agentFile + " " + installPath
	shimLine := "COPY --chmod=0755 --from=nexusagent nexus-runc /usr/local/sbin/runc"

	agentIdx := strings.Index(df, agentLine)
	shimIdx := strings.Index(df, shimLine)
	if agentIdx < 0 {
		t.Fatalf("agent COPY line missing:\n%s", df)
	}
	if shimIdx < 0 {
		t.Fatalf("runc shim COPY line missing:\n%s", df)
	}
	if shimIdx < agentIdx {
		t.Fatalf("runc shim COPY (at %d) must come after agent COPY (at %d):\n%s", shimIdx, agentIdx, df)
	}
	if strings.Count(df, shimLine) != 1 {
		t.Fatalf("runc shim COPY line must appear exactly once:\n%s", df)
	}
}

func TestStageRuncShim_WritesExecutableScript(t *testing.T) {
	dir := t.TempDir()
	name, err := stageRuncShim(dir)
	if err != nil {
		t.Fatalf("stageRuncShim: %v", err)
	}
	if name != "nexus-runc" {
		t.Fatalf("stageRuncShim returned %q, want %q", name, "nexus-runc")
	}
	path := filepath.Join(dir, name)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged shim: %v", err)
	}
	if len(runcShimScript) == 0 {
		t.Fatal("embedded runcShimScript is empty")
	}
	if !bytes.Equal(got, runcShimScript) {
		t.Fatalf("staged shim bytes differ from embedded script (%d vs %d bytes)", len(got), len(runcShimScript))
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm&0111 != 0111 {
		t.Fatalf("staged shim mode %o is not executable", perm)
	}
}

func TestBuildFingerprint_RuncShimSensitivity(t *testing.T) {
	cf := []byte("FROM ubuntu:22.04\nRUN echo hello\n")
	fp := func() string {
		got, err := BuildFingerprint(cf, "ubuntu:22.04", []byte("agent-v1"), t.TempDir(), cred.ToolRecipe{}, "x64")
		if err != nil {
			t.Fatalf("BuildFingerprint: %v", err)
		}
		return got
	}

	fp1 := fp()
	if again := fp(); again != fp1 {
		t.Fatalf("fingerprint not stable: %s vs %s", fp1, again)
	}

	saved := runcShimScript
	t.Cleanup(func() { runcShimScript = saved })
	runcShimScript = append([]byte{}, runcShimScript...)
	runcShimScript = append(runcShimScript, '\n', '#', 'x')

	fp2 := fp()
	if fp1 == fp2 {
		t.Fatalf("fingerprint unchanged after runc shim edit: %s", fp1)
	}
}
