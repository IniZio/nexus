package builder_test

// Tests for the Containerfile-parse → boot.json capture path.
//
// # Mechanism (post-rework)
//
// captureBootSpecFromContainerfile parses the raw .nexus/Containerfile bytes
// using buildkit's own parser+instructions packages and extracts ENTRYPOINT,
// CMD, WORKDIR, and ENV from the final build stage. This is deterministic and
// buildkitd-version-independent — it does NOT rely on gateway Result.Metadata,
// exporter response, or any live buildkitd process.
//
// The previous approach read res.Metadata[exptypes.ExporterImageConfigKey] from
// the gateway Build callback. moby/buildkit v0.19 (the pinned in-guest version)
// does not populate that key for ExporterLocal builds, so boot.json was silently
// never written in-guest. A live VM-boot e2e confirmed the failure.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/bootspec"
)

// readBootJSON reads and unmarshals the boot.json written under outDir.
func readBootJSON(t *testing.T, outDir string) bootspec.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(outDir, "etc", "nexus3", "boot.json"))
	if err != nil {
		t.Fatalf("read boot.json: %v", err)
	}
	var spec bootspec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("unmarshal boot.json: %v", err)
	}
	return spec
}

// TestCaptureBootSpec_ExecForm verifies exec-form ENTRYPOINT+CMD with WORKDIR and ENV.
//
// ENTRYPOINT ["/usr/bin/dockerd"] CMD ["--storage-driver=overlay2"]
// → Argv=["/usr/bin/dockerd","--storage-driver=overlay2"]
func TestCaptureBootSpec_ExecForm(t *testing.T) {
	outDir := t.TempDir()

	cf := []byte(`FROM docker.io/library/debian:bookworm-slim
WORKDIR /var/lib/docker
ENV FOO=bar
ENTRYPOINT ["/usr/bin/dockerd"]
CMD ["--storage-driver=overlay2"]
`)
	builder.CaptureBootSpecFromContainerfile(cf, outDir)

	spec := readBootJSON(t, outDir)
	if len(spec.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(spec.Tasks))
	}
	task := spec.Tasks[0]

	wantArgv := []string{"/usr/bin/dockerd", "--storage-driver=overlay2"}
	if len(task.Argv) != len(wantArgv) {
		t.Fatalf("argv: got %v, want %v", task.Argv, wantArgv)
	}
	for i, a := range wantArgv {
		if task.Argv[i] != a {
			t.Errorf("argv[%d]: got %q, want %q", i, task.Argv[i], a)
		}
	}
	if task.Cwd != "/var/lib/docker" {
		t.Errorf("Cwd: got %q, want /var/lib/docker", task.Cwd)
	}
	found := false
	for _, e := range task.Env {
		if e == "FOO=bar" {
			found = true
		}
	}
	if !found {
		t.Errorf("FOO=bar not in Env: %v", task.Env)
	}
	if !task.Background {
		t.Errorf("expected task.Background=true")
	}
}

// TestCaptureBootSpec_ShellFormCmd verifies shell-form CMD (no ENTRYPOINT).
//
// CMD dockerd --storage-driver=overlay2
// → Argv=["/bin/sh","-c","dockerd --storage-driver=overlay2"]
func TestCaptureBootSpec_ShellFormCmd(t *testing.T) {
	outDir := t.TempDir()

	cf := []byte(`FROM docker.io/library/debian:bookworm-slim
CMD dockerd --storage-driver=overlay2
`)
	builder.CaptureBootSpecFromContainerfile(cf, outDir)

	spec := readBootJSON(t, outDir)
	if len(spec.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(spec.Tasks))
	}
	wantArgv := []string{"/bin/sh", "-c", "dockerd --storage-driver=overlay2"}
	if len(spec.Tasks[0].Argv) != len(wantArgv) {
		t.Fatalf("argv: got %v, want %v", spec.Tasks[0].Argv, wantArgv)
	}
	for i, a := range wantArgv {
		if spec.Tasks[0].Argv[i] != a {
			t.Errorf("argv[%d]: got %q, want %q", i, spec.Tasks[0].Argv[i], a)
		}
	}
}

// TestCaptureBootSpec_EntrypointOnly verifies exec-form ENTRYPOINT with no CMD.
func TestCaptureBootSpec_EntrypointOnly(t *testing.T) {
	outDir := t.TempDir()

	cf := []byte(`FROM docker.io/library/alpine:3.19
ENTRYPOINT ["/docker-entrypoint.sh"]
`)
	builder.CaptureBootSpecFromContainerfile(cf, outDir)

	spec := readBootJSON(t, outDir)
	if len(spec.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(spec.Tasks))
	}
	if spec.Tasks[0].Argv[0] != "/docker-entrypoint.sh" {
		t.Errorf("argv[0]: got %q, want /docker-entrypoint.sh", spec.Tasks[0].Argv[0])
	}
}

// TestCaptureBootSpec_NoEntrypointOrCmd verifies that a Containerfile with
// neither ENTRYPOINT nor CMD writes no boot.json.
func TestCaptureBootSpec_NoEntrypointOrCmd(t *testing.T) {
	outDir := t.TempDir()

	cf := []byte(`FROM docker.io/library/ubuntu:24.04
RUN apt-get update
WORKDIR /workspace
`)
	builder.CaptureBootSpecFromContainerfile(cf, outDir)

	if _, err := os.Stat(filepath.Join(outDir, "etc", "nexus3", "boot.json")); !os.IsNotExist(err) {
		t.Errorf("expected no boot.json for Containerfile with no entrypoint/cmd, got err=%v", err)
	}
}

// TestCaptureBootSpec_MultiStageLastWins verifies that in a multi-stage
// Containerfile, only the final stage's ENTRYPOINT/CMD/WORKDIR/ENV are used.
func TestCaptureBootSpec_MultiStageLastWins(t *testing.T) {
	outDir := t.TempDir()

	cf := []byte(`FROM docker.io/library/golang:1.22 AS builder
WORKDIR /build
ENV BUILD_VAR=yes
ENTRYPOINT ["/build/wrong"]

FROM docker.io/library/debian:bookworm-slim
WORKDIR /app
ENV APP_VAR=hello
ENTRYPOINT ["/app/server"]
CMD ["--port=8080"]
`)
	builder.CaptureBootSpecFromContainerfile(cf, outDir)

	spec := readBootJSON(t, outDir)
	if len(spec.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(spec.Tasks))
	}
	task := spec.Tasks[0]

	wantArgv := []string{"/app/server", "--port=8080"}
	if len(task.Argv) != len(wantArgv) {
		t.Fatalf("argv: got %v, want %v", task.Argv, wantArgv)
	}
	for i, a := range wantArgv {
		if task.Argv[i] != a {
			t.Errorf("argv[%d]: got %q, want %q", i, task.Argv[i], a)
		}
	}
	if task.Cwd != "/app" {
		t.Errorf("Cwd: got %q, want /app", task.Cwd)
	}
	// BUILD_VAR from the builder stage must NOT appear.
	for _, e := range task.Env {
		if e == "BUILD_VAR=yes" {
			t.Errorf("BUILD_VAR from builder stage leaked into final stage env: %v", task.Env)
		}
	}
	found := false
	for _, e := range task.Env {
		if e == "APP_VAR=hello" {
			found = true
		}
	}
	if !found {
		t.Errorf("APP_VAR=hello not in Env: %v", task.Env)
	}
}

// TestCaptureBootSpec_ShellFormEntrypointDropsCmd verifies the Docker semantic:
// shell-form ENTRYPOINT causes CMD to be dropped entirely.
//
// ENTRYPOINT foo bar (shell form) AND CMD baz
// → Argv=["/bin/sh","-c","foo bar"]  (CMD "baz" is NOT appended)
func TestCaptureBootSpec_ShellFormEntrypointDropsCmd(t *testing.T) {
	outDir := t.TempDir()

	cf := []byte(`FROM docker.io/library/debian:bookworm-slim
ENTRYPOINT foo bar
CMD baz
`)
	builder.CaptureBootSpecFromContainerfile(cf, outDir)

	spec := readBootJSON(t, outDir)
	if len(spec.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(spec.Tasks))
	}
	wantArgv := []string{"/bin/sh", "-c", "foo bar"}
	task := spec.Tasks[0]
	if len(task.Argv) != len(wantArgv) {
		t.Fatalf("argv: got %v, want %v (CMD must be dropped for shell-form ENTRYPOINT)", task.Argv, wantArgv)
	}
	for i, a := range wantArgv {
		if task.Argv[i] != a {
			t.Errorf("argv[%d]: got %q, want %q", i, task.Argv[i], a)
		}
	}
}

// TestCaptureBootSpec_NilBytes verifies that nil containerfileBytes is a no-op.
func TestCaptureBootSpec_NilBytes(t *testing.T) {
	outDir := t.TempDir()
	builder.CaptureBootSpecFromContainerfile(nil, outDir) // must not panic

	if _, err := os.Stat(filepath.Join(outDir, "etc", "nexus3", "boot.json")); !os.IsNotExist(err) {
		t.Errorf("expected no boot.json for nil bytes, got err=%v", err)
	}
}

// TestCaptureBootSpec_EmptyBytes verifies that empty containerfileBytes is a no-op.
func TestCaptureBootSpec_EmptyBytes(t *testing.T) {
	outDir := t.TempDir()
	builder.CaptureBootSpecFromContainerfile([]byte{}, outDir) // must not panic

	if _, err := os.Stat(filepath.Join(outDir, "etc", "nexus3", "boot.json")); !os.IsNotExist(err) {
		t.Errorf("expected no boot.json for empty bytes, got err=%v", err)
	}
}

// TestBuildkitClient_LiveGatewayCapture is an integration test that runs a real
// build against a live buildkitd and verifies that boot.json is written from
// Containerfile parsing (the new mechanism — no gateway metadata required).
//
// Skip condition: the daemon socket /tmp/nexus3-spike-bk.sock is absent or
// unreachable (CI / offline envs). Run manually with buildkitd alive.
func TestBuildkitClient_LiveContainerfileCapture(t *testing.T) {
	const sock = "unix:///tmp/nexus3-spike-bk.sock"
	if _, err := os.Stat("/tmp/nexus3-spike-bk.sock"); os.IsNotExist(err) {
		t.Skip("buildkitd socket not present — skipping live integration test")
	}

	const containerfile = `FROM docker.io/library/alpine:3.19
WORKDIR /srv
ENV FOO=bar
ENTRYPOINT ["/bin/echo"]
CMD ["hello","world"]
`

	bk, err := builder.NewBuildkitClient(sock)
	if err != nil {
		t.Fatalf("NewBuildkitClient: %v", err)
	}

	wsDir := t.TempDir()

	agentSrc := "/bin/true"
	if _, err := os.Stat(agentSrc); err != nil {
		t.Skipf("agent stand-in %s not found: %v", agentSrc, err)
	}

	outDir := t.TempDir()

	req := builder.SolveRequest{
		BaseRef:            "docker.io/library/alpine:3.19",
		ContainerfileBytes: []byte(containerfile),
		AgentPath:          agentSrc,
		AgentInstallPath:   "/sbin/nexus3-agent",
		WorkspaceDir:       wsDir,
	}

	t.Logf("Solving against %s …", sock)
	if err := bk.Solve(t.Context(), req, outDir); err != nil {
		t.Fatalf("Solve: %v", err)
	}

	bootJSONPath := filepath.Join(outDir, "etc", "nexus3", "boot.json")
	data, err := os.ReadFile(bootJSONPath)
	if err != nil {
		t.Fatalf("boot.json not written — Containerfile parse produced no config: %v", err)
	}

	var spec bootspec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("unmarshal boot.json: %v", err)
	}

	t.Logf("boot.json contents:\n%s", data)

	if len(spec.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(spec.Tasks))
	}
	task := spec.Tasks[0]

	wantArgv := []string{"/bin/echo", "hello", "world"}
	if len(task.Argv) != len(wantArgv) {
		t.Fatalf("argv: got %v, want %v", task.Argv, wantArgv)
	}
	for i, a := range wantArgv {
		if task.Argv[i] != a {
			t.Errorf("argv[%d]: got %q, want %q", i, task.Argv[i], a)
		}
	}
	if task.Cwd != "/srv" {
		t.Errorf("Cwd: got %q, want /srv", task.Cwd)
	}
	foundFOO := false
	for _, e := range task.Env {
		if e == "FOO=bar" {
			foundFOO = true
		}
	}
	if !foundFOO {
		t.Errorf("FOO=bar not in Env: %v", task.Env)
	}
	if !task.Background {
		t.Errorf("expected task.Background=true")
	}
}
