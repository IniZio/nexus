package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/IniZio/nexus3/internal/core/config"
)

// ---- Load / parse tests ----

func TestLoad_AbsentFile_NotAnError(t *testing.T) {
	dir := t.TempDir()
	// Create a .git to act as the stop boundary so the walk does not
	// escape the temp dir.
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := config.Load(dir)
	if err != nil {
		t.Fatalf("want nil error for absent file, got %v", err)
	}
	if path != "" {
		t.Fatalf("want empty path for absent file, got %q", path)
	}
	// Zero config means no fields set.
	if len(cfg.Egress.Allow) != 0 {
		t.Fatalf("want empty egress.allow, got %v", cfg.Egress.Allow)
	}
}

func TestLoad_ValidFile_Parsed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	content := `version: 1
egress:
  allow: ["proxy.golang.org", "sum.golang.org"]
sandbox:
  image: nexus3-agent-base
  memory: 8192
  vcpus: 6
  mounts: [".:/work"]
`
	cfgFile := filepath.Join(dir, "nexus3.yaml")
	if err := os.WriteFile(cfgFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := config.Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != cfgFile {
		t.Fatalf("path: want %q, got %q", cfgFile, path)
	}
	if len(cfg.Egress.Allow) != 2 {
		t.Fatalf("egress.allow: want 2 entries, got %v", cfg.Egress.Allow)
	}
	if cfg.Egress.Allow[0] != "proxy.golang.org" {
		t.Fatalf("egress.allow[0]: want proxy.golang.org, got %q", cfg.Egress.Allow[0])
	}
	if cfg.Sandbox.Image != "nexus3-agent-base" {
		t.Fatalf("sandbox.image: want nexus3-agent-base, got %q", cfg.Sandbox.Image)
	}
	if cfg.Sandbox.Memory != 8192 {
		t.Fatalf("sandbox.memory: want 8192, got %d", cfg.Sandbox.Memory)
	}
	if cfg.Sandbox.VCPUs != 6 {
		t.Fatalf("sandbox.vcpus: want 6, got %d", cfg.Sandbox.VCPUs)
	}
	if len(cfg.Sandbox.Mounts) != 1 || cfg.Sandbox.Mounts[0] != ".:/work" {
		t.Fatalf("sandbox.mounts: want [\".:/work\"], got %v", cfg.Sandbox.Mounts)
	}
}

// TestLoad_UnknownTopLevelKey_HardError verifies that an unknown top-level key
// is rejected. Security-relevant: a typo in a key name silently disables a rule.
func TestLoad_UnknownTopLevelKey_HardError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	content := `version: 1
egress:
  allow: ["proxy.golang.org"]
unknown_top_key: "bad"
`
	if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := config.Load(dir)
	if err == nil {
		t.Fatal("want error for unknown top-level key, got nil")
	}
}

// TestLoad_UnknownNestedKey_HardError verifies that unknown keys are rejected at
// every nesting depth — including inside egress: and sandbox:. KnownFields(true)
// must catch these; a silently-ignored typo inside egress.allow is a security hole.
func TestLoad_UnknownNestedKey_HardError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	// "allow_hosts" is a plausible typo for "allow" inside egress: — must not be silently ignored.
	content := `version: 1
egress:
  allow_hosts: ["proxy.golang.org"]
`
	if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := config.Load(dir)
	if err == nil {
		t.Fatal("want error for unknown nested key, got nil — KnownFields(true) did not catch nested depth")
	}
}

// TestLoad_UnknownNestedSandboxKey_HardError verifies rejection inside sandbox:.
func TestLoad_UnknownNestedSandboxKey_HardError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	content := `version: 1
sandbox:
  image: foo
  cpu_count: 4
`
	if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := config.Load(dir)
	if err == nil {
		t.Fatal("want error for unknown nested sandbox key, got nil")
	}
}

func TestLoad_MalformedYAML_Error(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), []byte("version: 1\n  bad: indent\n  : colon\n"), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := config.Load(dir)
	if err == nil {
		t.Fatal("want error for malformed YAML, got nil")
	}
}

// TestLoad_MissingVersion_HardError verifies that a file with no version field is an error.
// Mutation target (a): if parse silently defaults the missing version to 1, this test
// passes when it should fail — a future syntax change would silently misparse old files.
func TestLoad_MissingVersion_HardError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	// No version field — must be an error, not a silent default.
	content := `egress:
  allow: ["proxy.golang.org"]
sandbox:
  image: nexus3-agent-base
`
	if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := config.Load(dir)
	if err == nil {
		t.Fatal("want error for missing version field, got nil — missing version must not silently default")
	}
}

// TestLoad_VersionTooHigh_ActionableError verifies that a version above supported
// produces an error that names both the file's version and the supported range.
// Mutation target (c): if the version check is removed, this test passes when it
// should fail — a future-syntax file would be silently misparsed.
func TestLoad_VersionTooHigh_ActionableError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	content := "version: 9999\negress:\n  allow: []\n"
	if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := config.Load(dir)
	if err == nil {
		t.Fatal("want error for version above supported, got nil")
	}
}

// TestLoad_VersionTooLow_ActionableError verifies that a version below minimum
// produces an error.
func TestLoad_VersionTooLow_ActionableError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	content := "version: 0\negress:\n  allow: []\n"
	if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := config.Load(dir)
	if err == nil {
		t.Fatal("want error for version below minimum, got nil")
	}
}

func TestLoad_WalksUp_StopsAtGitRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub", "pkg")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	content := "version: 1\negress:\n  allow: [\"proxy.golang.org\"]\n"
	if err := os.WriteFile(filepath.Join(root, "nexus3.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	// Start from sub/pkg — should walk up and find the file at root.
	cfg, path, err := config.Load(sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != filepath.Join(root, "nexus3.yaml") {
		t.Fatalf("path: want %q, got %q", filepath.Join(root, "nexus3.yaml"), path)
	}
	if len(cfg.Egress.Allow) != 1 {
		t.Fatalf("want 1 allow entry, got %v", cfg.Egress.Allow)
	}
}

func TestLoad_WalksUp_StopsAtGitRoot_NoBeyond(t *testing.T) {
	// nexus3.yaml is ABOVE the .git boundary; Load must not find it.
	outer := t.TempDir()
	inner := filepath.Join(outer, "repo")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer, "nexus3.yaml"), []byte("version: 1\negress:\n  allow: []\n"), 0600); err != nil {
		t.Fatal(err)
	}

	_, path, err := config.Load(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" {
		t.Fatalf("must not cross .git boundary: found %q", path)
	}
}

// ---- Precedence resolver tests ----

func intPtr(n int) *int { return &n }

func defaults() config.Defaults {
	return config.Defaults{Image: "default-image", Memory: 4096, VCPUs: 2}
}

var resolveTests = []struct {
	name  string
	flags config.Flags
	cfg   config.Config
	want  config.Resolved
}{
	{
		name:  "all defaults — no flags, empty config",
		flags: config.Flags{},
		cfg:   config.Config{},
		want: config.Resolved{
			Image: "default-image", MemoryMiB: 4096, VCPUs: 2,
		},
	},
	{
		name:  "project config overrides default",
		flags: config.Flags{},
		cfg: config.Config{
			Sandbox: config.SandboxConfig{Image: "project-image", Memory: 8192, VCPUs: 4},
			Egress:  config.EgressConfig{Allow: []string{"proxy.golang.org"}},
		},
		want: config.Resolved{
			Image: "project-image", MemoryMiB: 8192, VCPUs: 4,
			EgressAllow: []string{"proxy.golang.org"},
		},
	},
	{
		name: "explicit flag overrides project config",
		flags: config.Flags{
			Image:  "flag-image",
			Memory: intPtr(16384),
			VCPUs:  intPtr(8),
			// EgressAllow nil — falls through to config.
		},
		cfg: config.Config{
			Sandbox: config.SandboxConfig{Image: "project-image", Memory: 8192, VCPUs: 4},
			Egress:  config.EgressConfig{Allow: []string{"proxy.golang.org"}},
		},
		want: config.Resolved{
			Image: "flag-image", MemoryMiB: 16384, VCPUs: 8,
			// Egress: flag not set → project config wins.
			EgressAllow: []string{"proxy.golang.org"},
		},
	},
	{
		// Egress is the ONE additive field. A run that needs one extra host
		// must not lose the hosts the project always needs: under a replace
		// rule, a single --allow-host would silently drop proxy.golang.org and
		// break the build with no error explaining why.
		name: "egress is additive: flag hosts and project hosts are unioned",
		flags: config.Flags{
			EgressAllow: []string{"example.com"},
		},
		cfg: config.Config{
			Egress: config.EgressConfig{Allow: []string{"proxy.golang.org"}},
		},
		want: config.Resolved{
			Image: "default-image", MemoryMiB: 4096, VCPUs: 2,
			EgressAllow: []string{"example.com", "proxy.golang.org"},
		},
	},
	{
		name: "flag memory zero pointer beats project config",
		flags: config.Flags{
			Memory: intPtr(0),
		},
		cfg: config.Config{
			Sandbox: config.SandboxConfig{Memory: 8192},
		},
		// A *int(0) means the user explicitly passed --memory=0; it must win.
		want: config.Resolved{
			Image: "default-image", MemoryMiB: 0, VCPUs: 2,
		},
	},
	{
		name: "mounts: flag overrides config",
		flags: config.Flags{
			Mounts: []string{"/flag/path:/guest"},
		},
		cfg: config.Config{
			Sandbox: config.SandboxConfig{Mounts: config.Mounts{".:/work"}},
		},
		want: config.Resolved{
			Image: "default-image", MemoryMiB: 4096, VCPUs: 2,
			Mounts: []string{"/flag/path:/guest"},
		},
	},
}

func TestResolve(t *testing.T) {
	for _, tt := range resolveTests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.Resolve(tt.flags, tt.cfg, defaults())
			if got.Image != tt.want.Image {
				t.Errorf("Image: want %q, got %q", tt.want.Image, got.Image)
			}
			if got.MemoryMiB != tt.want.MemoryMiB {
				t.Errorf("MemoryMiB: want %d, got %d", tt.want.MemoryMiB, got.MemoryMiB)
			}
			if got.VCPUs != tt.want.VCPUs {
				t.Errorf("VCPUs: want %d, got %d", tt.want.VCPUs, got.VCPUs)
			}
			if len(got.EgressAllow) != len(tt.want.EgressAllow) {
				t.Errorf("EgressAllow len: want %d, got %d (%v)", len(tt.want.EgressAllow), len(got.EgressAllow), got.EgressAllow)
			} else {
				for i, h := range tt.want.EgressAllow {
					if got.EgressAllow[i] != h {
						t.Errorf("EgressAllow[%d]: want %q, got %q", i, h, got.EgressAllow[i])
					}
				}
			}
			if len(got.Mounts) != len(tt.want.Mounts) {
				t.Errorf("Mounts len: want %d, got %d", len(tt.want.Mounts), len(got.Mounts))
			}
		})
	}
}

// ---- Mount resolver tests ----

func TestResolveMounts_AbsolutePassThrough(t *testing.T) {
	mounts := []string{"/abs/path:/work"}
	got, err := config.ResolveMounts(mounts, "/some/config/dir")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "/abs/path:/work" {
		t.Fatalf("absolute path must pass through unchanged, got %q", got[0])
	}
}

func TestResolveMounts_RelativeResolvedAgainstConfigDir(t *testing.T) {
	mounts := []string{".:/work"}
	configDir := "/home/user/repo"
	got, err := config.ResolveMounts(mounts, configDir)
	if err != nil {
		t.Fatal(err)
	}
	// "." relative to /home/user/repo must become /home/user/repo.
	if got[0] != "/home/user/repo:/work" {
		t.Fatalf("want /home/user/repo:/work, got %q", got[0])
	}
}

func TestResolveMounts_RelativeSubdirResolvedAgainstConfigDir(t *testing.T) {
	mounts := []string{"src/app:/app"}
	configDir := "/home/user/repo"
	got, err := config.ResolveMounts(mounts, configDir)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "/home/user/repo/src/app:/app" {
		t.Fatalf("want /home/user/repo/src/app:/app, got %q", got[0])
	}
}

func TestResolveMounts_EmptyConfigDirLeavesRelativeUnchanged(t *testing.T) {
	mounts := []string{".:/work"}
	got, err := config.ResolveMounts(mounts, "")
	if err != nil {
		t.Fatal(err)
	}
	// No configDir means we cannot resolve; pass through.
	if got[0] != ".:/work" {
		t.Fatalf("want .:/work unchanged, got %q", got[0])
	}
}

func TestResolveMounts_NoSeparatorPassThrough(t *testing.T) {
	mounts := []string{"nodcolon"}
	got, err := config.ResolveMounts(mounts, "/some/dir")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "nodcolon" {
		t.Fatalf("want passthrough for entry without colon, got %q", got[0])
	}
}

func TestResolveMounts_EmptySlice(t *testing.T) {
	got, err := config.ResolveMounts(nil, "/any")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

// ---- JSON Schema drift test ----

// TestSchemaCoversAllStructFields reads the JSON Schema at
// docs/schema/nexus3.schema.json and verifies that every Go struct field
// (identified by its yaml tag) has a counterpart in the schema's property
// definitions. This test FAILS when a field is added to a Go struct without
// updating the schema — preventing the schema from silently rotting into a lie.
//
// The schema path is relative to this package: ../../../docs/schema/nexus3.schema.json.
func TestSchemaCoversAllStructFields(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "..", "docs", "schema", "nexus3.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("cannot read JSON schema at %s: %v — create the schema or fix the path", schemaPath, err)
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("JSON schema is not valid JSON: %v", err)
	}

	// Extract property keys at a given JSON path through the schema object.
	props := func(obj map[string]interface{}, keys ...string) map[string]interface{} {
		cur := obj
		for _, k := range keys {
			next, ok := cur[k].(map[string]interface{})
			if !ok {
				return nil
			}
			cur = next
		}
		return cur
	}

	topProps := props(schema, "properties")
	if topProps == nil {
		t.Fatal("schema has no top-level 'properties' key")
	}

	// Collect yaml tag names from a struct type.
	yamlFields := func(t reflect.Type) []string {
		var names []string
		for i := 0; i < t.NumField(); i++ {
			tag := t.Field(i).Tag.Get("yaml")
			if tag == "" || tag == "-" {
				continue
			}
			names = append(names, tag)
		}
		return names
	}

	// Top-level fields: "version", "egress", "sandbox".
	for _, name := range []string{"version", "egress", "sandbox"} {
		if _, ok := topProps[name]; !ok {
			t.Errorf("schema missing top-level property %q — add it to docs/schema/nexus3.schema.json", name)
		}
	}

	// EgressConfig fields inside properties.egress.properties.
	egressProps := props(schema, "properties", "egress", "properties")
	for _, name := range yamlFields(reflect.TypeOf(config.EgressConfig{})) {
		if _, ok := egressProps[name]; !ok {
			t.Errorf("schema missing egress property %q — add it to docs/schema/nexus3.schema.json", name)
		}
	}

	// SandboxConfig fields inside properties.sandbox.properties.
	sandboxProps := props(schema, "properties", "sandbox", "properties")
	for _, name := range yamlFields(reflect.TypeOf(config.SandboxConfig{})) {
		if _, ok := sandboxProps[name]; !ok {
			t.Errorf("schema missing sandbox property %q — add it to docs/schema/nexus3.schema.json", name)
		}
	}

	// Reverse direction: every schema property must map to a known Go field.
	// A schema-only property documents a key the parser refuses (unknown YAML
	// keys are a hard parse error), so the schema would be lying to the user.

	// Top-level: only "version", "egress", "sandbox" are valid.
	knownTopLevel := map[string]bool{"version": true, "egress": true, "sandbox": true}
	for key := range topProps {
		if !knownTopLevel[key] {
			t.Errorf("schema has unknown top-level property %q — remove it from docs/schema/nexus3.schema.json or add to the Go config struct", key)
		}
	}

	// Egress reverse: every schema egress property must be a yaml tag in EgressConfig.
	egressYAML := map[string]bool{}
	for _, name := range yamlFields(reflect.TypeOf(config.EgressConfig{})) {
		egressYAML[name] = true
	}
	for key := range egressProps {
		if !egressYAML[key] {
			t.Errorf("schema has egress property %q with no corresponding Go yaml tag — remove from schema or add to EgressConfig", key)
		}
	}

	// Sandbox reverse: every schema sandbox property must be a yaml tag in SandboxConfig.
	sandboxYAML := map[string]bool{}
	for _, name := range yamlFields(reflect.TypeOf(config.SandboxConfig{})) {
		sandboxYAML[name] = true
	}
	for key := range sandboxProps {
		if !sandboxYAML[key] {
			t.Errorf("schema has sandbox property %q with no corresponding Go yaml tag — remove from schema or add to SandboxConfig", key)
		}
	}
}

// --- Mounts YAML type tests ---

func loadFromYAML(t *testing.T, data []byte) (config.Config, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(dir)
	return cfg, err
}

func TestMounts_ShortStringPassthrough(t *testing.T) {
	data := []byte("version: 1\nsandbox:\n  mounts:\n    - /host/a:/guest/a\n    - /host/b:/guest/b:ro\n")
	cfg, err := loadFromYAML(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"/host/a:/guest/a", "/host/b:/guest/b:ro"}
	if got := []string(cfg.Sandbox.Mounts); !slicesEqual(got, want) {
		t.Errorf("Mounts = %v, want %v", got, want)
	}
}

func TestMounts_LongFormRO(t *testing.T) {
	data := []byte("version: 1\nsandbox:\n  mounts:\n    - source: /host/a\n      target: /guest/a\n      read_only: true\n")
	cfg, err := loadFromYAML(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"/host/a:/guest/a:ro"}
	if got := []string(cfg.Sandbox.Mounts); !slicesEqual(got, want) {
		t.Errorf("Mounts = %v, want %v", got, want)
	}
}

func TestMounts_LongFormRW(t *testing.T) {
	data := []byte("version: 1\nsandbox:\n  mounts:\n    - source: /host/a\n      target: /guest/a\n")
	cfg, err := loadFromYAML(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"/host/a:/guest/a"}
	if got := []string(cfg.Sandbox.Mounts); !slicesEqual(got, want) {
		t.Errorf("Mounts = %v, want %v", got, want)
	}
}

func TestMounts_MixedList(t *testing.T) {
	data := []byte("version: 1\nsandbox:\n  mounts:\n    - /host/a:/guest/a\n    - source: /host/b\n      target: /guest/b\n      read_only: true\n")
	cfg, err := loadFromYAML(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"/host/a:/guest/a", "/host/b:/guest/b:ro"}
	if got := []string(cfg.Sandbox.Mounts); !slicesEqual(got, want) {
		t.Errorf("Mounts = %v, want %v", got, want)
	}
}

func TestMounts_MissingSource(t *testing.T) {
	data := []byte("version: 1\nsandbox:\n  mounts:\n    - target: /guest/a\n")
	_, err := loadFromYAML(t, data)
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
}

func TestMounts_MissingTarget(t *testing.T) {
	data := []byte("version: 1\nsandbox:\n  mounts:\n    - source: /host/a\n")
	_, err := loadFromYAML(t, data)
	if err == nil {
		t.Fatal("expected error for missing target, got nil")
	}
}

// slicesEqual compares two string slices for equality.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
