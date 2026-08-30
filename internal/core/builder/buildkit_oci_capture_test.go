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
	"archive/tar"
	"bytes"
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

// ── D-DC-31: OCI-config-merge tests ──────────────────────────────────────────
//
// The following tests verify that captureBootSpec uses the effective OCI image
// config (which includes values inherited from the FROM base image) when it is
// available, and falls back to captureBootSpecFromContainerfile otherwise.

// buildOCITar constructs a minimal OCI image layout tar containing the
// provided image config JSON. Layers are omitted; only the mandatory
// index.json, manifest blob, and config blob are included.
func buildOCITar(t *testing.T, configJSON []byte) []byte {
	t.Helper()

	// 64-char hex digests (fake but valid hex length for sha256 paths).
	configDigest := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	manifestDigest := "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe0"

	manifestObj := map[string]interface{}{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]interface{}{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"size":      len(configJSON),
			"digest":    "sha256:" + configDigest,
		},
		"layers": []interface{}{},
	}
	manifestJSON, err := json.Marshal(manifestObj)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	indexObj := map[string]interface{}{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]interface{}{
			{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"size":      len(manifestJSON),
				"digest":    "sha256:" + manifestDigest,
			},
		},
	}
	indexJSON, err := json.Marshal(indexObj)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	entries := []struct {
		name string
		data []byte
	}{
		{"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)},
		{"index.json", indexJSON},
		{"blobs/sha256/" + manifestDigest, manifestJSON},
		{"blobs/sha256/" + configDigest, configJSON},
	}
	for _, e := range entries {
		hdr := &tar.Header{
			Name: e.name,
			Mode: 0644,
			Size: int64(len(e.data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar write header %s: %v", e.name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatalf("tar write %s: %v", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

// buildOCIConfigJSON constructs an OCI image config JSON blob with the given
// Entrypoint, Cmd, WorkingDir, and Env.
func buildOCIConfigJSON(t *testing.T, entrypoint, cmd []string, workingDir string, env []string) []byte {
	t.Helper()
	raw := map[string]interface{}{
		"config": map[string]interface{}{
			"Entrypoint": entrypoint,
			"Cmd":        cmd,
			"WorkingDir": workingDir,
			"Env":        env,
		},
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal OCI config JSON: %v", err)
	}
	return b
}

// TestCaptureBootSpec_InheritedFromBase is the primary D-DC-31 regression test.
// It simulates a Containerfile WITHOUT an ENTRYPOINT directive, paired with a
// fixture OCI image config that carries an entrypoint inherited from the base image.
//
// The test proves that captureBootSpec produces a boot task from the OCI config
// even though the Containerfile alone would yield no boot task. This is the
// exact gap that D-DC-31 fixes: `FROM nginx` (no ENTRYPOINT line) must produce
// a boot task for nginx's declared entrypoint.
//
// Mutation proof: removing the `if ociCfg != nil` branch in captureBootSpec
// causes this test to fail (no boot.json written → readBootJSON fatal).
func TestCaptureBootSpec_InheritedFromBase(t *testing.T) {
	outDir := t.TempDir()

	// Containerfile declares NO ENTRYPOINT or CMD — it would produce no boot task
	// if parsed alone (verified by TestCaptureBootSpec_NoEntrypointOrCmd).
	cf := []byte(`FROM docker.io/library/nginx:1.27
RUN echo "configured"
WORKDIR /app
`)

	// The effective OCI image config for the built image carries the inherited
	// ENTRYPOINT from nginx's base image.
	ociCfg := &bootspec.OCIImageConfig{
		Entrypoint: []string{"/docker-entrypoint.sh"},
		Cmd:        []string{"nginx", "-g", "daemon off;"},
		WorkingDir: "/app",
		Env:        []string{"INHERITED_ENV=from_base"},
	}

	builder.CaptureBootSpec(cf, ociCfg, outDir)

	spec := readBootJSON(t, outDir)
	if len(spec.Tasks) != 1 {
		t.Fatalf("expected 1 task from inherited OCI entrypoint, got %d", len(spec.Tasks))
	}
	task := spec.Tasks[0]

	wantArgv := []string{"/docker-entrypoint.sh", "nginx", "-g", "daemon off;"}
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
	foundEnv := false
	for _, e := range task.Env {
		if e == "INHERITED_ENV=from_base" {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Errorf("INHERITED_ENV=from_base not in Env: %v", task.Env)
	}
	if !task.Background {
		t.Errorf("expected task.Background=true (OCI entrypoint is always background)")
	}
}

// TestCaptureBootSpec_NilOCICfgFallsBackToContainerfile verifies that when
// ociCfg is nil (OCI export unavailable), captureBootSpec falls back to
// captureBootSpecFromContainerfile and still produces a correct boot.json from
// an explicit ENTRYPOINT in the Containerfile.
func TestCaptureBootSpec_NilOCICfgFallsBackToContainerfile(t *testing.T) {
	outDir := t.TempDir()

	cf := []byte(`FROM docker.io/library/alpine:3.19
ENTRYPOINT ["/bin/myapp"]
CMD ["--debug"]
`)

	builder.CaptureBootSpec(cf, nil, outDir) // nil ociCfg → Containerfile fallback

	spec := readBootJSON(t, outDir)
	if len(spec.Tasks) != 1 {
		t.Fatalf("expected 1 task from Containerfile fallback, got %d", len(spec.Tasks))
	}
	wantArgv := []string{"/bin/myapp", "--debug"}
	if len(spec.Tasks[0].Argv) != len(wantArgv) {
		t.Fatalf("argv: got %v, want %v", spec.Tasks[0].Argv, wantArgv)
	}
	for i, a := range wantArgv {
		if spec.Tasks[0].Argv[i] != a {
			t.Errorf("argv[%d]: got %q, want %q", i, spec.Tasks[0].Argv[i], a)
		}
	}
}

// TestCaptureBootSpec_OCINoEntrypointNoBootJSON verifies that when the OCI
// config has no Entrypoint and no Cmd (base image has no boot process and
// Containerfile declares none), no boot.json is written.
func TestCaptureBootSpec_OCINoEntrypointNoBootJSON(t *testing.T) {
	outDir := t.TempDir()

	ociCfg := &bootspec.OCIImageConfig{
		// No Entrypoint, no Cmd.
	}
	cf := []byte(`FROM docker.io/library/ubuntu:24.04
RUN apt-get update
`)

	builder.CaptureBootSpec(cf, ociCfg, outDir)

	if _, err := os.Stat(filepath.Join(outDir, "etc", "nexus3", "boot.json")); !os.IsNotExist(err) {
		t.Errorf("expected no boot.json when OCI config has no entrypoint/cmd, got err=%v", err)
	}
}

// TestParseOCIConfigFromTar verifies the OCI layout tar parser end-to-end:
// a well-formed OCI tar with an entrypoint config is parsed correctly.
func TestParseOCIConfigFromTar(t *testing.T) {
	configJSON := buildOCIConfigJSON(t,
		[]string{"/docker-entrypoint.sh"},
		[]string{"nginx", "-g", "daemon off;"},
		"/usr/share/nginx/html",
		[]string{"NGINX_VERSION=1.27", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
	)
	ociTar := buildOCITar(t, configJSON)

	cfg, found, err := builder.ParseOCIConfigFromTar(bytes.NewReader(ociTar))
	if err != nil {
		t.Fatalf("ParseOCIConfigFromTar: %v", err)
	}
	if !found {
		t.Fatal("ParseOCIConfigFromTar: found=false, expected config to be found")
	}
	if len(cfg.Entrypoint) != 1 || cfg.Entrypoint[0] != "/docker-entrypoint.sh" {
		t.Errorf("Entrypoint: got %v, want [/docker-entrypoint.sh]", cfg.Entrypoint)
	}
	if len(cfg.Cmd) != 3 || cfg.Cmd[0] != "nginx" {
		t.Errorf("Cmd: got %v, want [nginx -g daemon off;]", cfg.Cmd)
	}
	if cfg.WorkingDir != "/usr/share/nginx/html" {
		t.Errorf("WorkingDir: got %q, want /usr/share/nginx/html", cfg.WorkingDir)
	}
	if len(cfg.Env) != 2 {
		t.Errorf("Env: got %v, want 2 entries", cfg.Env)
	}
}

// TestParseOCIConfigFromTar_EmptyReader verifies that an empty reader returns
// found=false without error (non-fatal; caller falls back).
func TestParseOCIConfigFromTar_EmptyReader(t *testing.T) {
	_, found, err := builder.ParseOCIConfigFromTar(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("ParseOCIConfigFromTar empty: unexpected error: %v", err)
	}
	if found {
		t.Error("ParseOCIConfigFromTar empty: found=true, want false")
	}
}
