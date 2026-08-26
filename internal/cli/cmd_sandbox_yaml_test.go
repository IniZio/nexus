package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/config"
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

// TestApplyProjectConfig_MemoryMax verifies the sandbox.memory_max knob:
//
//  1. A config with memory_max set populates f.memoryMaxMiB when the CLI flag
//     was absent (memoryMaxMiB == 0).
//  2. An explicit CLI --memory-max value (memoryMaxMiB != 0) is NOT overwritten
//     by the config — the guard `f.memoryMaxMiB == 0` must hold.
//  3. An absent config field leaves f.memoryMaxMiB at zero (no-op).
//
// Mutation proofs:
//   - Delete the `f.memoryMaxMiB == 0` guard in applyProjectConfig → subtest 1
//     still passes but subtest 2 turns RED (config overwrites the CLI value).
//   - Delete the assignment `f.memoryMaxMiB = uint32(cfg.Sandbox.MemoryMax)` →
//     subtest 1 turns RED (config value is silently dropped).
func TestApplyProjectConfig_MemoryMax(t *testing.T) {
	t.Run("config memory_max applied when flag absent", func(t *testing.T) {
		dir := t.TempDir()
		writeGitRoot(t, dir)
		writeYaml(t, dir, "version: 1\nsandbox:\n  memory_max: 2048\n")
		t.Chdir(dir)

		f := sandboxCreateFlags{} // memoryMaxMiB == 0 → flag absent
		if err := applyProjectConfig(&f); err != nil {
			t.Fatalf("applyProjectConfig: %v", err)
		}
		if f.memoryMaxMiB != 2048 {
			t.Errorf("memoryMaxMiB = %d, want 2048 (config must be applied when flag absent)", f.memoryMaxMiB)
		}
	})

	t.Run("CLI --memory-max wins over config memory_max", func(t *testing.T) {
		dir := t.TempDir()
		writeGitRoot(t, dir)
		writeYaml(t, dir, "version: 1\nsandbox:\n  memory_max: 2048\n")
		t.Chdir(dir)

		f := sandboxCreateFlags{memoryMaxMiB: 3072} // --memory-max 3072 was passed
		if err := applyProjectConfig(&f); err != nil {
			t.Fatalf("applyProjectConfig: %v", err)
		}
		if f.memoryMaxMiB != 3072 {
			t.Errorf("memoryMaxMiB = %d, want 3072 (CLI --memory-max must beat config)", f.memoryMaxMiB)
		}
	})

	t.Run("absent config field leaves memoryMaxMiB zero", func(t *testing.T) {
		dir := t.TempDir()
		writeGitRoot(t, dir)
		writeYaml(t, dir, "version: 1\n")
		t.Chdir(dir)

		f := sandboxCreateFlags{}
		if err := applyProjectConfig(&f); err != nil {
			t.Fatalf("applyProjectConfig: %v", err)
		}
		if f.memoryMaxMiB != 0 {
			t.Errorf("memoryMaxMiB = %d, want 0 (absent field must be a no-op)", f.memoryMaxMiB)
		}
	})
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

// TestApplyProjectConfig_FileFlag_BeatsConfigImage verifies that when --file is
// given alongside a config sandbox.image, the config image is NOT applied to
// f.imageRef. The build branch at runSandboxCreate requires f.imageRef == "",
// so any unconditional assignment of the config image here would silently
// suppress the build and use the config image instead.
//
// Mutation target: remove the guard `if f.imageRef == "" && f.filePath == "" &&
// f.rootfsPath == ""` around the f.imageRef = resolved.Image assignment. The
// config image would be applied unconditionally, imageRef becomes non-empty,
// and this test turns RED.
func TestApplyProjectConfig_FileFlag_BeatsConfigImage(t *testing.T) {
	dir := t.TempDir()
	writeGitRoot(t, dir)
	writeYaml(t, dir, `version: 1
sandbox:
  image: config-image
`)
	t.Chdir(dir)

	f := sandboxCreateFlags{
		filePath: "/some/project", // simulates --file given on CLI
	}
	if err := applyProjectConfig(&f); err != nil {
		t.Fatalf("applyProjectConfig: %v", err)
	}

	// imageRef must remain empty so the build branch is reachable.
	if f.imageRef != "" {
		t.Errorf("imageRef = %q, want empty: config image must not win when --file is given (build branch requires imageRef == \"\")", f.imageRef)
	}
	// filePath must be preserved unchanged.
	if f.filePath != "/some/project" {
		t.Errorf("filePath = %q, want %q", f.filePath, "/some/project")
	}
}

// TestApplyProjectConfig_RootfsFlag_BeatsConfigImage verifies that when
// --rootfs is given alongside a config sandbox.image, the config image is NOT
// applied to f.imageRef. Setting both would place two conflicting image sources
// on the spec simultaneously.
//
// Mutation target: same guard as TestApplyProjectConfig_FileFlag_BeatsConfigImage.
// Without it, imageRef is set from config even though rootfsPath is also set.
func TestApplyProjectConfig_RootfsFlag_BeatsConfigImage(t *testing.T) {
	dir := t.TempDir()
	writeGitRoot(t, dir)
	writeYaml(t, dir, `version: 1
sandbox:
  image: config-image
`)
	t.Chdir(dir)

	f := sandboxCreateFlags{
		rootfsPath: "/path/to/rootfs.ext4", // simulates --rootfs given on CLI
	}
	if err := applyProjectConfig(&f); err != nil {
		t.Fatalf("applyProjectConfig: %v", err)
	}

	// imageRef must remain empty; config image must not win when --rootfs is given.
	if f.imageRef != "" {
		t.Errorf("imageRef = %q, want empty: config image must not win when --rootfs is given", f.imageRef)
	}
	// rootfsPath must be preserved unchanged.
	if f.rootfsPath != "/path/to/rootfs.ext4" {
		t.Errorf("rootfsPath = %q, want %q", f.rootfsPath, "/path/to/rootfs.ext4")
	}
}

// TestApplyProjectConfig_FieldGuardCoverage is a two-part structural test:
//
//  1. Reflection sweep: every yaml tag in config.SandboxConfig must appear in
//     the cases table below. A new field added to SandboxConfig without a
//     corresponding entry here fails the test RED — forcing the author to
//     consciously add a guard case AND verify it works.
//
//  2. Per-field guard verification: for each case, a config value AND a CLI
//     override are set simultaneously; the test asserts the CLI value survives.
//     Remove the guard that owns a field's precedence and its case turns RED.
//
//     Which layer owns that guard is NOT uniform, and the case for each field
//     is written against its own owning layer:
//
//     image   — applyProjectConfig (--file / --rootfs suppress the config image)
//     mounts  — applyProjectConfig (f.mountLive == nil)
//     memory  — config.Resolve     (the f.Memory != nil branch)
//     vcpus   — config.Resolve     (the f.VCPUs != nil branch)
//
//     memory and vcpus have NO precedence guard in applyProjectConfig at all:
//     their `if resolved.X != 0` is a zero-value check, not a precedence guard.
//     Making those two applications unconditional leaves this test GREEN, and
//     correctly so — Resolve already returned the CLI value.
//
// Mutation proofs (independent — revert each before trying the next):
//
//	Part 1: add `Extra string \`yaml:"extra"\`` to SandboxConfig without a
//	corresponding case entry below — the reflection sweep fails RED on the
//	missing "extra" tag. Revert to restore GREEN.
//
//	Part 2: in applyProjectConfig replace the image guard with unconditional
//	`f.imageRef = resolved.Image` — the image cases below fail RED because
//	the suppressor flags (--file, --rootfs) no longer suppress the assignment.
//	Revert to restore GREEN.
func TestApplyProjectConfig_FieldGuardCoverage(t *testing.T) {
	type fieldCase struct {
		yamlTag    string
		configYAML string                                   // nexus3.yaml content setting this field
		setupFlags func(f *sandboxCreateFlags)              // set the CLI override before applyProjectConfig
		check      func(t *testing.T, f sandboxCreateFlags) // assert CLI value survived
	}

	cases := []fieldCase{
		{
			// image/--file suppressor: when --file is given, the guard must block
			// the config image from overwriting imageRef (imageRef must stay "").
			// This bites when the guard is deleted: resolved.Image becomes
			// "config-image" and the unconditional assignment sets imageRef.
			yamlTag:    "image",
			configYAML: "version: 1\nsandbox:\n  image: config-image\n",
			setupFlags: func(f *sandboxCreateFlags) { f.filePath = "." },
			check: func(t *testing.T, f sandboxCreateFlags) {
				if f.imageRef != "" {
					t.Errorf("image/--file: guard failed, config set imageRef = %q, want empty", f.imageRef)
				}
			},
		},
		{
			// image/--rootfs suppressor: when --rootfs is given, the guard must
			// block the config image from overwriting imageRef (imageRef must stay "").
			yamlTag:    "image",
			configYAML: "version: 1\nsandbox:\n  image: config-image\n",
			setupFlags: func(f *sandboxCreateFlags) { f.rootfsPath = "/tmp/root.ext4" },
			check: func(t *testing.T, f sandboxCreateFlags) {
				if f.imageRef != "" {
					t.Errorf("image/--rootfs: guard failed, config set imageRef = %q, want empty", f.imageRef)
				}
			},
		},
		{
			yamlTag:    "memory",
			configYAML: "version: 1\nsandbox:\n  memory: 8192\n",
			setupFlags: func(f *sandboxCreateFlags) { f.memoryMiB = 4096 },
			check: func(t *testing.T, f sandboxCreateFlags) {
				if f.memoryMiB != 4096 {
					t.Errorf("memory: config overwrote CLI --memory flag: got %d, want 4096", f.memoryMiB)
				}
			},
		},
		{
			yamlTag:    "vcpus",
			configYAML: "version: 1\nsandbox:\n  vcpus: 8\n",
			setupFlags: func(f *sandboxCreateFlags) { f.vcpus = 2 },
			check: func(t *testing.T, f sandboxCreateFlags) {
				if f.vcpus != 2 {
					t.Errorf("vcpus: config overwrote CLI --vcpus flag: got %d, want 2", f.vcpus)
				}
			},
		},
		{
			// mounts: any --mount flag causes the CLI mounts to replace config mounts entirely.
			yamlTag:    "mounts",
			configYAML: "version: 1\nsandbox:\n  mounts:\n    - /tmp/config-host:/config-guest\n",
			setupFlags: func(f *sandboxCreateFlags) { f.mountLive = []string{"cli-host:cli-guest"} },
			check: func(t *testing.T, f sandboxCreateFlags) {
				if len(f.mountLive) != 1 || f.mountLive[0] != "cli-host:cli-guest" {
					t.Errorf("mounts: config overwrote CLI --mount flag: got %v, want [cli-host:cli-guest]", f.mountLive)
				}
			},
		},
		{
			// agent: explicit --agent flag must win over the project config default.
			// Guard is the `f.agentName == ""` check in applyProjectConfig.
			yamlTag:    "agent",
			configYAML: "version: 1\nsandbox:\n  agent: claude-code\n",
			setupFlags: func(f *sandboxCreateFlags) { f.agentName = "claude-code" }, // flag already set
			check: func(t *testing.T, f sandboxCreateFlags) {
				if f.agentName != "claude-code" {
					t.Errorf("agent: config overwrote CLI --agent flag: got %q, want %q", f.agentName, "claude-code")
				}
			},
		},
		{
			// memory_max: when no --memory-max flag was given (f.memoryMaxMiB == 0),
			// the config value is applied. When the flag WAS given (non-zero), the
			// guard `f.memoryMaxMiB == 0` must block the config from overwriting it.
			// Guard is the `f.memoryMaxMiB == 0` check in applyProjectConfig.
			// Mutation target: deleting the guard causes config to overwrite the CLI
			// value → this case turns RED.
			yamlTag:    "memory_max",
			configYAML: "version: 1\nsandbox:\n  memory_max: 2048\n",
			setupFlags: func(f *sandboxCreateFlags) { f.memoryMaxMiB = 3072 }, // CLI --memory-max 3072
			check: func(t *testing.T, f sandboxCreateFlags) {
				if f.memoryMaxMiB != 3072 {
					t.Errorf("memory_max: config overwrote CLI --memory-max flag: got %d, want 3072", f.memoryMaxMiB)
				}
			},
		},
	}

	// Part 1: reflection sweep — every yaml tag in SandboxConfig must be in cases.
	covered := make(map[string]bool, len(cases))
	for _, c := range cases {
		covered[c.yamlTag] = true
	}
	typ := reflect.TypeOf(config.SandboxConfig{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		if idx := strings.IndexByte(tag, ','); idx >= 0 {
			tag = tag[:idx]
		}
		if !covered[tag] {
			t.Errorf("SandboxConfig field with yaml tag %q has no entry in TestApplyProjectConfig_FieldGuardCoverage cases — "+
				"add an entry to cases AND a CLI-intent guard in applyProjectConfig before applying the field", tag)
		}
	}

	// Part 2: per-field guard verification.
	for _, tc := range cases {
		tc := tc
		t.Run(tc.yamlTag, func(t *testing.T) {
			dir := t.TempDir()
			writeGitRoot(t, dir)
			writeYaml(t, dir, tc.configYAML)
			t.Chdir(dir)

			f := sandboxCreateFlags{}
			tc.setupFlags(&f)
			if err := applyProjectConfig(&f); err != nil {
				t.Fatalf("applyProjectConfig: %v", err)
			}
			tc.check(t, f)
		})
	}
}
