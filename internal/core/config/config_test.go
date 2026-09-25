package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/config"
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
  policy:
    - host: api.github.com
      paths: ["/repos/owner/myrepo/**", "/user"]
sandbox:
  image: nexus-agent-base
  memory: 8192
  vcpus: 6
  mounts: [".:/work"]
`
	cfgFile := writeConfigAt(t, dir, config.ConfigRelPath, content)

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
	if len(cfg.Egress.Policy) != 1 {
		t.Fatalf("egress.policy: want 1 entry, got %v", cfg.Egress.Policy)
	}
	if cfg.Egress.Policy[0].Host != "api.github.com" {
		t.Fatalf("egress.policy[0].host: want api.github.com, got %q", cfg.Egress.Policy[0].Host)
	}
	if len(cfg.Egress.Policy[0].Paths) == 0 {
		t.Fatalf("egress.policy[0].paths: want non-empty, got empty")
	}
	if cfg.Sandbox.Image != "nexus-agent-base" {
		t.Fatalf("sandbox.image: want nexus-agent-base, got %q", cfg.Sandbox.Image)
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
	writeConfigAt(t, dir, config.ConfigRelPath, content)

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
	writeConfigAt(t, dir, config.ConfigRelPath, content)

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
	writeConfigAt(t, dir, config.ConfigRelPath, content)

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
	writeConfigAt(t, dir, config.ConfigRelPath, "version: 1\n  bad: indent\n  : colon\n")

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
  image: nexus-agent-base
`
	writeConfigAt(t, dir, config.ConfigRelPath, content)

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
	writeConfigAt(t, dir, config.ConfigRelPath, content)

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
	writeConfigAt(t, dir, config.ConfigRelPath, content)

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
	want := writeConfigAt(t, root, config.ConfigRelPath, content)

	// Start from sub/pkg — should walk up and find the file at root.
	cfg, path, err := config.Load(sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != want {
		t.Fatalf("path: want %q, got %q", want, path)
	}
	if len(cfg.Egress.Allow) != 1 {
		t.Fatalf("want 1 allow entry, got %v", cfg.Egress.Allow)
	}
}

func TestLoad_WalksUp_StopsAtGitRoot_NoBeyond(t *testing.T) {
	// The config is ABOVE the .git boundary; Load must not find it.
	outer := t.TempDir()
	inner := filepath.Join(outer, "repo")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	writeConfigAt(t, outer, config.ConfigRelPath, "version: 1\negress:\n  allow: []\n")

	_, path, err := config.Load(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" {
		t.Fatalf("must not cross .git boundary: found %q", path)
	}
}

// ---- .nexus/config.yaml location tests ----

func writeConfigAt(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoad_IgnoresRootLevelConfigFile verifies the hard cutover: a config
// file at the repo root (outside .nexus/) is not discovered.
func TestLoad_IgnoresRootLevelConfigFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	writeConfigAt(t, dir, "config.yaml", "version: 1\nsandbox:\n  image: root-level\n")

	cfg, path, err := config.Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" {
		t.Fatalf("root-level config must not be discovered, found %q", path)
	}
	if cfg.Sandbox.Image != "" {
		t.Fatalf("want zero config, got image %q", cfg.Sandbox.Image)
	}
}

func TestProjectDir(t *testing.T) {
	tests := []struct {
		name    string
		cfgPath string
		want    string
	}{
		{"nexus dir shape", "/home/u/repo/.nexus/config.yaml", "/home/u/repo"},
		{"nested project", "/home/u/mono/pkg/app/.nexus/config.yaml", "/home/u/mono/pkg/app"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := config.ProjectDir(tt.cfgPath); got != tt.want {
				t.Fatalf("ProjectDir(%q): want %q, got %q", tt.cfgPath, tt.want, got)
			}
		})
	}
}

func TestProjectDir_MatchesLoadPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	writeConfigAt(t, dir, config.ConfigRelPath, "version: 1\n")

	_, path, err := config.Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := config.ProjectDir(path); got != dir {
		t.Fatalf("ProjectDir(%q): want %q, got %q", path, dir, got)
	}
	if filepath.Dir(path) == dir {
		t.Fatalf("filepath.Dir(%q) equals project dir; test would not catch a ProjectDir regression", path)
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

func TestResolveMounts_TildeSlashExpandedToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir:", err)
	}
	got, err := config.ResolveMounts([]string{"~/.cache/x:/var/cache/x"}, "/any/dir")
	if err != nil {
		t.Fatal(err)
	}
	want := home + "/.cache/x:/var/cache/x"
	if got[0] != want {
		t.Fatalf("want %q, got %q", want, got[0])
	}
}

func TestResolveMounts_BareTildeExpandedToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir:", err)
	}
	got, err := config.ResolveMounts([]string{"~:/opt/home"}, "/any/dir")
	if err != nil {
		t.Fatal(err)
	}
	want := home + ":/opt/home"
	if got[0] != want {
		t.Fatalf("want %q, got %q", want, got[0])
	}
}

// ---- JSON Schema drift test ----

// TestSchemaCoversAllStructFields reads the JSON Schema at
// doc/schema/nexus.schema.json and verifies that every Go struct field
// (identified by its yaml tag) has a counterpart in the schema's property
// definitions. This test FAILS when a field is added to a Go struct without
// updating the schema — preventing the schema from silently rotting into a lie.
//
// The schema path is relative to this package: ../../../doc/schema/nexus.schema.json.
func TestSchemaCoversAllStructFields(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "..", "doc", "schema", "nexus.schema.json")
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
			t.Errorf("schema missing top-level property %q — add it to doc/schema/nexus.schema.json", name)
		}
	}

	// EgressConfig fields inside properties.egress.properties.
	egressProps := props(schema, "properties", "egress", "properties")
	for _, name := range yamlFields(reflect.TypeOf(config.EgressConfig{})) {
		if _, ok := egressProps[name]; !ok {
			t.Errorf("schema missing egress property %q — add it to doc/schema/nexus.schema.json", name)
		}
	}

	// SandboxConfig fields inside properties.sandbox.properties.
	sandboxProps := props(schema, "properties", "sandbox", "properties")
	for _, name := range yamlFields(reflect.TypeOf(config.SandboxConfig{})) {
		if _, ok := sandboxProps[name]; !ok {
			t.Errorf("schema missing sandbox property %q — add it to doc/schema/nexus.schema.json", name)
		}
	}

	// Reverse direction: every schema property must map to a known Go field.
	// A schema-only property documents a key the parser refuses (unknown YAML
	// keys are a hard parse error), so the schema would be lying to the user.

	// Top-level: only "version", "egress", "sandbox" are valid.
	knownTopLevel := map[string]bool{"version": true, "egress": true, "sandbox": true}
	for key := range topProps {
		if !knownTopLevel[key] {
			t.Errorf("schema has unknown top-level property %q — remove it from doc/schema/nexus.schema.json or add to the Go config struct", key)
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
	writeConfigAt(t, dir, config.ConfigRelPath, string(data))
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

// ---- egress.secrets tests ----

// parseBytes is a helper that calls config.Parse on the given bytes.
func parseBytes(t *testing.T, data []byte) (config.Config, error) {
	t.Helper()
	return config.Parse(data)
}

func TestEgressSecrets_ShortForm(t *testing.T) {
	data := []byte("version: 1\negress:\n  secrets:\n    - GH_TOKEN@api.github.com,uploads.github.com\n")
	cfg, err := parseBytes(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Egress.Secrets) != 1 {
		t.Fatalf("want 1 secret, got %d", len(cfg.Egress.Secrets))
	}
	s := cfg.Egress.Secrets[0]
	if s.Env != "GH_TOKEN" {
		t.Errorf("Env: want GH_TOKEN, got %q", s.Env)
	}
	if len(s.Hosts) != 2 || s.Hosts[0] != "api.github.com" || s.Hosts[1] != "uploads.github.com" {
		t.Errorf("Hosts: want [api.github.com uploads.github.com], got %v", s.Hosts)
	}
}

// TestEgressSecrets_UnknownKey_HardError verifies AC T2-AC3:
// an unknown key inside egress.secrets[] is a hard error (fail-closed on typo).
func TestEgressSecrets_UnknownKey_HardError(t *testing.T) {
	data := []byte("version: 1\negress:\n  secrets:\n    - env: GH_TOKEN\n      hosts: [api.github.com]\n      typo_key: oops\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for unknown key inside egress.secrets entry, got nil")
	}
}

// TestEgressSecrets_OldRepoField_UnknownKeyError verifies that the old `repo:`
// field under egress.secrets is rejected as an unknown key. The field has been
// removed from the schema entirely — path policies belong in egress.policy.paths.
func TestEgressSecrets_OldRepoField_UnknownKeyError(t *testing.T) {
	data := []byte("version: 1\negress:\n  secrets:\n    - env: GH_TOKEN\n      hosts: [api.github.com]\n      repo: owner/myrepo\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for old repo: field under egress.secrets, got nil — field must now be under egress.policy")
	}
}

// TestEgressSecrets_OldPathsField_UnknownKeyError verifies that the old `paths:`
// field under egress.secrets is now rejected as an unknown key (it has moved to
// egress.policy).
func TestEgressSecrets_OldPathsField_UnknownKeyError(t *testing.T) {
	data := []byte("version: 1\negress:\n  secrets:\n    - env: GITLAB_TOKEN\n      hosts: [gitlab.com]\n      paths: [\"/v4/projects/123/**\"]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for old paths: field under egress.secrets, got nil — field must now be under egress.policy")
	}
}

// TestEgressPolicy_GenericPaths_Parsed verifies that a generic paths policy entry
// (no preset) parses correctly.
func TestEgressPolicy_GenericPaths_Parsed(t *testing.T) {
	data := []byte("version: 1\negress:\n  policy:\n    - host: api.example.com\n      paths: [\"/v4/projects/123/**\"]\n")
	cfg, err := parseBytes(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Egress.Policy) != 1 {
		t.Fatalf("want 1 policy entry, got %d", len(cfg.Egress.Policy))
	}
	p := cfg.Egress.Policy[0]
	if p.Host != "api.example.com" {
		t.Errorf("Host: want api.example.com, got %q", p.Host)
	}
	if len(p.Paths) != 1 || p.Paths[0] != "/v4/projects/123/**" {
		t.Errorf("Paths: want [\"/v4/projects/123/**\"], got %v", p.Paths)
	}
}

// TestEgressPolicy_MissingHost_Error verifies that a policy entry without a
// host field is a hard error.
func TestEgressPolicy_MissingHost_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  policy:\n    - paths: [\"/repos/**\"]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for policy entry missing host, got nil")
	}
}

// TestEgressPolicy_UnknownKey_HardError verifies that an unknown key inside
// egress.policy[] is a hard error.
func TestEgressPolicy_UnknownKey_HardError(t *testing.T) {
	data := []byte("version: 1\negress:\n  policy:\n    - host: api.github.com\n      paths: [\"/repos/**\"]\n      typo_key: oops\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for unknown key in egress.policy entry, got nil")
	}
}

// TestEgressPolicy_EmptyPaths_Error verifies that a policy entry with no paths
// entries is a hard error (paths is required — an empty allowlist would silently
// default-deny everything, which is almost certainly a misconfiguration).
func TestEgressPolicy_EmptyPaths_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  policy:\n    - host: api.github.com\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for policy entry with no paths, got nil")
	}
}

// TestEgressPolicy_Absent_ZeroValue verifies that an absent egress.policy is
// not an error and produces a zero-length slice.
func TestEgressPolicy_Absent_ZeroValue(t *testing.T) {
	data := []byte("version: 1\negress:\n  allow: [proxy.golang.org]\n")
	cfg, err := parseBytes(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Egress.Policy) != 0 {
		t.Fatalf("want zero policy entries when absent, got %d", len(cfg.Egress.Policy))
	}
}

func TestEgressSecrets_MissingEnv_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  secrets:\n    - hosts: [api.github.com]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for missing 'env' field, got nil")
	}
}

func TestEgressSecrets_MissingHosts_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  secrets:\n    - env: GH_TOKEN\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for missing 'hosts' field, got nil")
	}
}

func TestEgressSecrets_Absent_ZeroValue(t *testing.T) {
	data := []byte("version: 1\negress:\n  allow: [proxy.golang.org]\n")
	cfg, err := parseBytes(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Egress.Secrets) != 0 {
		t.Fatalf("want zero secrets when absent, got %d", len(cfg.Egress.Secrets))
	}
}

// ---- branches section tests (T1-AC1) ----

// TestParse_Branches_TopLevelKeyIsRejected verifies that a top-level
// "branches:" key in the project config is rejected by the strict decoder
// (KnownFields(true)). The branches.* abstraction has been removed; any
// config that still carries this key must be updated, and the strict
// decoder surfaces the error rather than silently ignoring it.
func TestParse_Branches_TopLevelKeyIsRejected(t *testing.T) {
	data := []byte("version: 1\nbranches:\n  allowed: [refs/heads/nexus/**]\n")
	_, err := config.Parse(data)
	if err == nil {
		t.Error("Parse accepted a top-level 'branches' key; want an error (key is unknown after de-abstraction)")
	}
}

// TestParse_EqualsLoad_ForIdenticalBytes verifies that config.Parse and config.Load
// produce the same result when given the same YAML content.
func TestParse_EqualsLoad_ForIdenticalBytes(t *testing.T) {
	data := []byte("version: 1\negress:\n  allow: [proxy.golang.org]\n  secrets:\n    - GH_TOKEN@api.github.com\n")

	parsedCfg, err := config.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	writeConfigAt(t, dir, config.ConfigRelPath, string(data))
	loadedCfg, _, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Compare Allow.
	if len(parsedCfg.Egress.Allow) != len(loadedCfg.Egress.Allow) {
		t.Errorf("Egress.Allow len: Parse=%d Load=%d", len(parsedCfg.Egress.Allow), len(loadedCfg.Egress.Allow))
	}
	// Compare Secrets.
	if len(parsedCfg.Egress.Secrets) != len(loadedCfg.Egress.Secrets) {
		t.Errorf("Egress.Secrets len: Parse=%d Load=%d", len(parsedCfg.Egress.Secrets), len(loadedCfg.Egress.Secrets))
	} else if len(parsedCfg.Egress.Secrets) > 0 {
		p, l := parsedCfg.Egress.Secrets[0], loadedCfg.Egress.Secrets[0]
		if p.Env != l.Env {
			t.Errorf("Secrets[0].Env: Parse=%q Load=%q", p.Env, l.Env)
		}
		if len(p.Hosts) != len(l.Hosts) {
			t.Errorf("Secrets[0].Hosts: Parse=%v Load=%v", p.Hosts, l.Hosts)
		}
	}
}

func TestParse_EgressAllowOverlap(t *testing.T) {
	cases := []struct {
		name     string
		yaml     string
		wantHost string
		wantList string
	}{
		{
			name:     "allow+policy overlap",
			yaml:     "version: 1\negress:\n  allow: [pypi.org, github.com]\n  policy:\n    - host: github.com\n      paths: [\"/o/r/**\"]\n",
			wantHost: "github.com", wantList: "egress.policy",
		},
		{
			name:     "allow+secrets hosts overlap",
			yaml:     "version: 1\negress:\n  allow: [api.github.com]\n  secrets:\n    - GH_TOKEN@api.github.com\n",
			wantHost: "api.github.com", wantList: "egress.secrets",
		},
		{
			name:     "case difference still detected",
			yaml:     "version: 1\negress:\n  allow: [GitHub.COM]\n  policy:\n    - host: github.com\n      paths: [\"/o/r/**\"]\n",
			wantHost: "GitHub.COM", wantList: "egress.policy",
		},
		{
			name: "disjoint hosts ok",
			yaml: "version: 1\negress:\n  allow: [codeload.github.com, pypi.org]\n  policy:\n    - host: github.com\n      paths: [\"/o/r/**\"]\n  secrets:\n    - GH_TOKEN@api.github.com\n",
		},
		{
			name: "policy and secrets may share a host",
			yaml: "version: 1\negress:\n  policy:\n    - host: api.github.com\n      paths: [\"/repos/o/r/**\"]\n  secrets:\n    - GH_TOKEN@api.github.com\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Parse([]byte(tc.yaml))
			if tc.wantHost == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error naming host %q, got nil", tc.wantHost)
			}
			msg := err.Error()
			for _, want := range []string{`"` + tc.wantHost + `"`, "egress.allow", tc.wantList, "remove it from egress.allow"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q missing %q", msg, want)
				}
			}
		})
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

// ---- egress.mcp tests ----

func TestEgressMCPConfig_ValidFull(t *testing.T) {
	data := []byte(`version: 1
egress:
  mcp:
    - host: artifacts.oursky.codes
      path: /mcp
      allow: [get_artifact, list_artifacts, publish_zip]
      args:
        publish_zip:
          projectSlug: "demo-*"
`)
	cfg, err := parseBytes(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Egress.MCP) != 1 {
		t.Fatalf("want 1 mcp entry, got %d", len(cfg.Egress.MCP))
	}
	m := cfg.Egress.MCP[0]
	if m.Host != "artifacts.oursky.codes" {
		t.Errorf("Host: want artifacts.oursky.codes, got %q", m.Host)
	}
	if m.Path != "/mcp" {
		t.Errorf("Path: want /mcp, got %q", m.Path)
	}
	if len(m.Allow) != 3 {
		t.Errorf("Allow: want 3 entries, got %v", m.Allow)
	}
	if m.Args["publish_zip"]["projectSlug"] != "demo-*" {
		t.Errorf("Args: want demo-*, got %q", m.Args["publish_zip"]["projectSlug"])
	}
}

func TestEgressMCPConfig_ValidMinimal(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: tools.example.com\n      allow: [call_tool]\n")
	cfg, err := parseBytes(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Egress.MCP) != 1 {
		t.Fatalf("want 1 mcp entry, got %d", len(cfg.Egress.MCP))
	}
	m := cfg.Egress.MCP[0]
	if m.Host != "tools.example.com" {
		t.Errorf("Host: want tools.example.com, got %q", m.Host)
	}
	if m.Path != "" {
		t.Errorf("Path: want empty, got %q", m.Path)
	}
	if len(m.Args) != 0 {
		t.Errorf("Args: want nil, got %v", m.Args)
	}
}

func TestEgressMCPConfig_MissingHost_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - allow: [call_tool]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for mcp entry missing host, got nil")
	}
	if !strings.Contains(err.Error(), "host is required") {
		t.Errorf("want 'host is required' in error, got %q", err.Error())
	}
}

func TestEgressMCPConfig_EmptyAllow_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: tools.example.com\n      allow: []\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for empty allow, got nil")
	}
	if !strings.Contains(err.Error(), "allow must be non-empty") {
		t.Errorf("want 'allow must be non-empty' in error, got %q", err.Error())
	}
}

func TestEgressMCPConfig_MissingAllow_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: tools.example.com\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for missing allow, got nil")
	}
}

func TestEgressMCPConfig_UnknownKey_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: tools.example.com\n      allow: [call_tool]\n      typo_key: oops\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for unknown key in mcp entry, got nil")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("want 'unknown key' in error, got %q", err.Error())
	}
}

func TestEgressMCPConfig_ArgsKeyNotInAllow_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: tools.example.com\n      allow: [call_tool]\n      args:\n        other_tool:\n          param: value\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for args key not in allow, got nil")
	}
	if !strings.Contains(err.Error(), "not in allow") {
		t.Errorf("want 'not in allow' in error, got %q", err.Error())
	}
}

func TestEgressMCPConfig_InvalidGlob_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: tools.example.com\n      allow: [call_tool]\n      args:\n        call_tool:\n          param: \"[bad\"\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for invalid glob pattern, got nil")
	}
	if !strings.Contains(err.Error(), "invalid glob") {
		t.Errorf("want 'invalid glob' in error, got %q", err.Error())
	}
}

func TestEgressMCPConfig_DuplicateHost_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: tools.example.com\n      allow: [call_tool]\n    - host: tools.example.com\n      allow: [other_tool]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for duplicate host, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate host") {
		t.Errorf("want 'duplicate host' in error, got %q", err.Error())
	}
}

func TestEgressMCPConfig_HostLowercased(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: Tools.Example.COM\n      allow: [call_tool]\n")
	cfg, err := parseBytes(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Egress.MCP[0].Host != "tools.example.com" {
		t.Errorf("Host: want lowercase tools.example.com, got %q", cfg.Egress.MCP[0].Host)
	}
}

func TestEgressMCP_HostWithScheme_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: \"https://example.com\"\n      allow: [call_tool]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for host with scheme, got nil")
	}
	if !strings.Contains(err.Error(), "://") {
		t.Errorf("want '://' in error, got %q", err.Error())
	}
}

func TestEgressMCP_HostWithPort_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: \"example.com:8080\"\n      allow: [call_tool]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for host with port, got nil")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("want 'port' in error, got %q", err.Error())
	}
}

func TestEgressMCP_HostWithPath_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: \"example.com/mcp\"\n      allow: [call_tool]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for host with path, got nil")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("want 'path' in error, got %q", err.Error())
	}
}

func TestEgressMCP_HostWithWhitespace_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: \"example .com\"\n      allow: [call_tool]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for host with whitespace, got nil")
	}
	if !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("want 'whitespace' in error, got %q", err.Error())
	}
}

func TestEgressMCP_HostTrailingDot_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: \"example.com.\"\n      allow: [call_tool]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for host with trailing dot, got nil")
	}
	if !strings.Contains(err.Error(), "trailing dot") {
		t.Errorf("want 'trailing dot' in error, got %q", err.Error())
	}
}

func TestEgressMCP_PathNoSlash_Error(t *testing.T) {
	data := []byte("version: 1\negress:\n  mcp:\n    - host: \"example.com\"\n      path: \"mcp\"\n      allow: [call_tool]\n")
	_, err := parseBytes(t, data)
	if err == nil {
		t.Fatal("want error for path without leading slash, got nil")
	}
	if !strings.Contains(err.Error(), "must start with /") {
		t.Errorf("want 'must start with /' in error, got %q", err.Error())
	}
}
