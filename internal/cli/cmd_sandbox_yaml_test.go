package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeYaml writes a nexus3.yaml to dir and returns the file path.
func writeYaml(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "nexus3.yaml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatalf("writeYaml: %v", err)
	}
	return p
}

// writeGitRoot creates a .git marker so config.Load treats dir as a repo root
// and does not walk further up.
func writeGitRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatalf("writeGitRoot: %v", err)
	}
}

// TestApplyProjectConfig_AbsentConfig_IsNoop verifies that a directory with
// no nexus3.yaml leaves every sandboxCreateFlags field unchanged.
//
// Mutation target (d): if applyProjectConfig returns an error when no config
// file is found, this test fails RED — the absent-config path must be a no-op.
func TestApplyProjectConfig_AbsentConfig_IsNoop(t *testing.T) {
	dir := t.TempDir()
	writeGitRoot(t, dir)
	t.Chdir(dir)

	f := sandboxCreateFlags{}
	if err := applyProjectConfig(&f); err != nil {
		t.Fatalf("applyProjectConfig: %v", err)
	}

	// Every field must remain at its zero value — the absent config must not
	// inject any image, memory, vcpus, or mounts.
	if f.imageRef != "" {
		t.Errorf("imageRef = %q, want empty (absent config must not set image)", f.imageRef)
	}
	if f.memoryMiB != 0 {
		t.Errorf("memoryMiB = %d, want 0 (absent config must not set memory)", f.memoryMiB)
	}
	if f.vcpus != 0 {
		t.Errorf("vcpus = %d, want 0 (absent config must not set vcpus)", f.vcpus)
	}
	if f.mountLive != nil {
		t.Errorf("mountLive = %v, want nil (absent config must not set mounts)", f.mountLive)
	}
}

// TestApplyProjectConfig_ConfigFieldsApplied verifies that when a nexus3.yaml
// is present, its sandbox fields are applied to zero-valued flags.
//
// Mutation target: if any field is silently dropped (e.g. vcpus is never
// copied from resolved to f.vcpus), the corresponding assertion turns RED.
func TestApplyProjectConfig_ConfigFieldsApplied(t *testing.T) {
	dir := t.TempDir()
	writeGitRoot(t, dir)
	writeYaml(t, dir, `version: 1
sandbox:
  image: nexus3-agent-base
  memory: 8192
  vcpus: 6
`)
	t.Chdir(dir)

	f := sandboxCreateFlags{}
	if err := applyProjectConfig(&f); err != nil {
		t.Fatalf("applyProjectConfig: %v", err)
	}

	if f.imageRef != "nexus3-agent-base" {
		t.Errorf("imageRef = %q, want %q", f.imageRef, "nexus3-agent-base")
	}
	if f.memoryMiB != 8192 {
		t.Errorf("memoryMiB = %d, want 8192", f.memoryMiB)
	}
	if f.vcpus != 6 {
		t.Errorf("vcpus = %d, want 6", f.vcpus)
	}
}

// TestApplyProjectConfig_ExplicitFlagBeatsConfig verifies the precedence:
// explicit CLI flag > project config.
//
// Mutation target: if the resolver is inverted (config beats explicit flag),
// f.imageRef ends up as "config-image" instead of "cli-image" — the assertion
// turns RED.
func TestApplyProjectConfig_ExplicitFlagBeatsConfig(t *testing.T) {
	dir := t.TempDir()
	writeGitRoot(t, dir)
	writeYaml(t, dir, `version: 1
sandbox:
  image: config-image
  memory: 8192
  vcpus: 4
`)
	t.Chdir(dir)

	f := sandboxCreateFlags{
		imageRef:  "cli-image",
		memoryMiB: 2048,
		vcpus:     2,
	}
	if err := applyProjectConfig(&f); err != nil {
		t.Fatalf("applyProjectConfig: %v", err)
	}

	if f.imageRef != "cli-image" {
		t.Errorf("imageRef = %q, want %q (explicit flag must beat config)", f.imageRef, "cli-image")
	}
	if f.memoryMiB != 2048 {
		t.Errorf("memoryMiB = %d, want 2048 (explicit flag must beat config)", f.memoryMiB)
	}
	if f.vcpus != 2 {
		t.Errorf("vcpus = %d, want 2 (explicit flag must beat config)", f.vcpus)
	}
}

// TestApplyProjectConfig_ConfigMountsResolvedAgainstConfigDir verifies that a
// relative host path in sandbox.mounts is made absolute relative to the
// nexus3.yaml file's directory, NOT the process cwd.
//
// Mutation target: if the resolver uses cwd instead of the config dir,
// the host path becomes filepath.Join(cwd, ".") = cwd, which differs from
// the config dir when the two are different. The assertion turns RED.
func TestApplyProjectConfig_ConfigMountsResolvedAgainstConfigDir(t *testing.T) {
	// repoRoot holds the nexus3.yaml. cwd is a subdirectory simulating the
	// user running the command from inside the repo.
	repoRoot := t.TempDir()
	writeGitRoot(t, repoRoot)
	writeYaml(t, repoRoot, `version: 1
sandbox:
  mounts: [".:/work"]
`)

	subdir := filepath.Join(repoRoot, "sub")
	if err := os.MkdirAll(subdir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Chdir(subdir) // cwd ≠ repoRoot

	f := sandboxCreateFlags{} // no --mount flags
	if err := applyProjectConfig(&f); err != nil {
		t.Fatalf("applyProjectConfig: %v", err)
	}

	if len(f.mountLive) != 1 {
		t.Fatalf("mountLive len = %d, want 1", len(f.mountLive))
	}
	// The host path must be the repo root (where nexus3.yaml lives), not the
	// subdirectory cwd.
	wantHost := repoRoot
	gotSpec := f.mountLive[0]
	// Spec is "<host>:<guest>". Extract the host portion.
	colonIdx := 0
	for i, c := range gotSpec {
		if c == ':' {
			colonIdx = i
			break
		}
	}
	gotHost := gotSpec[:colonIdx]
	if gotHost != wantHost {
		t.Errorf("mount host = %q, want %q (must be config dir, not cwd %q)",
			gotHost, wantHost, subdir)
	}
}

// TestApplyProjectConfig_ExplicitMountReplacesConfigMounts verifies the
// REPLACE semantics: an explicit --mount flag supersedes the config mounts
// entirely. No config mount leaks into f.mountLive.
func TestApplyProjectConfig_ExplicitMountReplacesConfigMounts(t *testing.T) {
	dir := t.TempDir()
	writeGitRoot(t, dir)
	writeYaml(t, dir, `version: 1
sandbox:
  mounts: [".:/work", ".:/also"]
`)
	t.Chdir(dir)

	f := sandboxCreateFlags{
		mountLive: []string{"/explicit:/guest"},
	}
	if err := applyProjectConfig(&f); err != nil {
		t.Fatalf("applyProjectConfig: %v", err)
	}

	// The explicit --mount must be the only entry; config mounts must not
	// appear alongside it.
	if len(f.mountLive) != 1 || f.mountLive[0] != "/explicit:/guest" {
		t.Errorf("mountLive = %v, want [/explicit:/guest] (explicit --mount must replace config mounts)",
			f.mountLive)
	}
}
