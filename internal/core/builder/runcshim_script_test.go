package builder

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const shimCAPath = "/etc/nexus/ca/ca-certificates.crt"

// shimInjectedEnv is the env prefix the shim prepends, in order.
var shimInjectedEnv = []string{
	"SSL_CERT_FILE=" + shimCAPath,
	"CURL_CA_BUNDLE=" + shimCAPath,
	"REQUESTS_CA_BUNDLE=" + shimCAPath,
	"PIP_CERT=" + shimCAPath,
	"NODE_EXTRA_CA_CERTS=" + shimCAPath,
	"GIT_SSL_CAINFO=" + shimCAPath,
	"CARGO_HTTP_CAINFO=" + shimCAPath,
	"NIX_SSL_CERT_FILE=" + shimCAPath,
	"DENO_CERT=" + shimCAPath,
	"WGETRC=/etc/nexus/ca/wgetrc",
	"APT_CONFIG=/etc/nexus/ca/apt.conf",
}

const fakeRuncScript = `#!/bin/sh
: >"$FAKE_OUT"
for a in "$@"; do printf '%s\n' "$a" >>"$FAKE_OUT"; done
prev=""; b=""
for a in "$@"; do
	case "$prev" in --bundle | -b) b="$a" ;; esac
	case "$a" in --bundle=*) b="${a#--bundle=}" ;; esac
	prev="$a"
done
if [ -n "$b" ] && [ -f "$b/config.json" ]; then cp "$b/config.json" "$FAKE_CFG"; fi
exit 0
`

type shimHarness struct {
	t        *testing.T
	root     string
	shim     string
	fake     string
	fakeOut  string
	fakeCfg  string
	caCrt    string
	sysCrt   string
	trustDir string
}

func newShimHarness(t *testing.T) *shimHarness {
	t.Helper()
	for _, bin := range []string{"sh", "sed"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	root := t.TempDir()
	h := &shimHarness{
		t:        t,
		root:     root,
		shim:     filepath.Join(root, "nexus-runc"),
		fake:     filepath.Join(root, "fake-runc"),
		fakeOut:  filepath.Join(root, "fake.argv"),
		fakeCfg:  filepath.Join(root, "fake.config.json"),
		caCrt:    filepath.Join(root, "nexus-mitm.crt"),
		sysCrt:   filepath.Join(root, "sys-ca-certificates.crt"),
		trustDir: filepath.Join(root, "trust"),
	}
	mustWrite(t, h.shim, runcShimScript, 0755)
	mustWrite(t, h.fake, []byte(fakeRuncScript), 0755)
	mustWrite(t, h.caCrt, []byte("-----BEGIN CERTIFICATE-----\nmitm\n-----END CERTIFICATE-----\n"), 0644)
	mustWrite(t, h.sysCrt, []byte("-----BEGIN CERTIFICATE-----\nsys-bundle\n-----END CERTIFICATE-----\n"), 0644)
	return h
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

// newBundle creates an OCI bundle dir with the given config.json (nil = none).
func (h *shimHarness) newBundle(name string, config []byte) string {
	h.t.Helper()
	dir := filepath.Join(h.root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.t.Fatal(err)
	}
	if config != nil {
		mustWrite(h.t, filepath.Join(dir, "config.json"), config, 0644)
	}
	return dir
}

// run executes the shim via sh with the given argv and cwd; returns the argv
// the fake runc received.
func (h *shimHarness) run(cwd string, args ...string) []string {
	h.t.Helper()
	_ = os.Remove(h.fakeOut)
	_ = os.Remove(h.fakeCfg)
	cmd := exec.Command("sh", append([]string{h.shim}, args...)...)
	cmd.Dir = cwd
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"NEXUS_RUNC=" + h.fake,
		"NEXUS_CA_CRT=" + h.caCrt,
		"NEXUS_SYS_BUNDLE=" + h.sysCrt,
		"NEXUS_TRUST_DIR=" + h.trustDir,
		"FAKE_OUT=" + h.fakeOut,
		"FAKE_CFG=" + h.fakeCfg,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("shim %v: %v\n%s", args, err, out)
	}
	raw, err := os.ReadFile(h.fakeOut)
	if err != nil {
		h.t.Fatalf("fake runc was not exec'd: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(got) != len(args) {
		h.t.Fatalf("fake runc argv = %q, want %q", got, args)
	}
	for i := range args {
		if got[i] != args[i] {
			h.t.Fatalf("fake runc argv[%d] = %q, want %q", i, got[i], args[i])
		}
	}
	return got
}

func readShimConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config.json invalid after shim: %v\n%s", err, raw)
	}
	return cfg
}

func shimMounts(t *testing.T, cfg map[string]any) []map[string]any {
	t.Helper()
	raw, ok := cfg["mounts"].([]any)
	if !ok {
		t.Fatalf("mounts missing or not an array: %v", cfg["mounts"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		mm, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("mount entry not an object: %v", m)
		}
		out = append(out, mm)
	}
	return out
}

func shimEnv(t *testing.T, cfg map[string]any) []string {
	t.Helper()
	proc, ok := cfg["process"].(map[string]any)
	if !ok {
		t.Fatalf("process missing: %v", cfg["process"])
	}
	raw, ok := proc["env"].([]any)
	if !ok {
		t.Fatalf("process.env missing or not an array: %v", proc["env"])
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("env entry not a string: %v", e)
		}
		out = append(out, s)
	}
	return out
}

func assertStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %q, want %q (full: %q)", what, i, got[i], want[i], got)
		}
	}
}

func (h *shimHarness) assertCAMount(m map[string]any) {
	h.t.Helper()
	if m["destination"] != "/etc/nexus/ca" || m["type"] != "bind" || m["source"] != h.trustDir {
		h.t.Fatalf("CA mount = %v, want destination=/etc/nexus/ca type=bind source=%s", m, h.trustDir)
	}
	opts, _ := m["options"].([]any)
	want := []any{"rbind", "ro", "nosuid", "nodev"}
	if len(opts) != len(want) {
		h.t.Fatalf("CA mount options = %v, want %v", opts, want)
	}
	for i := range want {
		if opts[i] != want[i] {
			h.t.Fatalf("CA mount options = %v, want %v", opts, want)
		}
	}
}

func (h *shimHarness) assertTrustDir() {
	h.t.Helper()
	sys, err := os.ReadFile(h.sysCrt)
	if err != nil {
		h.t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(h.trustDir, "ca-certificates.crt"))
	if err != nil {
		h.t.Fatal(err)
	}
	if !bytes.Equal(got, sys) {
		h.t.Fatalf("trust dir ca-certificates.crt = %q, want sys bundle %q", got, sys)
	}
	wgetrc, err := os.ReadFile(filepath.Join(h.trustDir, "wgetrc"))
	if err != nil {
		h.t.Fatal(err)
	}
	if string(wgetrc) != "ca_certificate=/etc/nexus/ca/ca-certificates.crt\n" {
		h.t.Fatalf("wgetrc = %q", wgetrc)
	}
	apt, err := os.ReadFile(filepath.Join(h.trustDir, "apt.conf"))
	if err != nil {
		h.t.Fatal(err)
	}
	if string(apt) != "Acquire::https::CAInfo \"/etc/nexus/ca/ca-certificates.crt\";\n" {
		h.t.Fatalf("apt.conf = %q", apt)
	}
}

const shimBasicConfig = `{"ociVersion":"1.0.2","process":{"env":["PATH=/usr/bin","FOO=bar"]},"mounts":[{"destination":"/proc","type":"proc","source":"proc"}]}`

func TestRuncShimScript_InjectsOnCreate(t *testing.T) {
	h := newShimHarness(t)
	dir := h.newBundle("b", []byte(shimBasicConfig))

	h.run(h.root, "--root", "/run/runc", "create", "--bundle", dir, "ctr1")

	cfg := readShimConfig(t, filepath.Join(dir, "config.json"))
	mounts := shimMounts(t, cfg)
	if len(mounts) != 2 {
		t.Fatalf("mounts = %v, want 2 entries", mounts)
	}
	h.assertCAMount(mounts[0])
	if mounts[1]["destination"] != "/proc" || mounts[1]["type"] != "proc" || mounts[1]["source"] != "proc" {
		t.Fatalf("mounts[1] = %v, want original proc mount", mounts[1])
	}
	assertStrings(t, "process.env", shimEnv(t, cfg), append(append([]string{}, shimInjectedEnv...), "PATH=/usr/bin", "FOO=bar"))
	if cfg["ociVersion"] != "1.0.2" {
		t.Fatalf("ociVersion = %v", cfg["ociVersion"])
	}

	// fake runc saw the rewritten config
	seen, err := os.ReadFile(h.fakeCfg)
	if err != nil {
		t.Fatal(err)
	}
	final, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !bytes.Equal(seen, final) {
		t.Fatalf("runc saw config %q, bundle has %q", seen, final)
	}
	h.assertTrustDir()
}

func TestRuncShimScript_EmptyArrays(t *testing.T) {
	h := newShimHarness(t)
	dir := h.newBundle("b", []byte(`{"ociVersion":"1.0.2","process":{"env":[]},"mounts":[]}`))

	h.run(h.root, "create", "--bundle", dir, "ctr1")

	cfg := readShimConfig(t, filepath.Join(dir, "config.json"))
	mounts := shimMounts(t, cfg)
	if len(mounts) != 1 {
		t.Fatalf("mounts = %v, want exactly the injected mount", mounts)
	}
	h.assertCAMount(mounts[0])
	assertStrings(t, "process.env", shimEnv(t, cfg), shimInjectedEnv)
}

func TestRuncShimScript_PrettyPrintedWhitespace(t *testing.T) {
	h := newShimHarness(t)
	pretty := "{\n  \"ociVersion\": \"1.0.2\",\n  \"process\": {\n    \"env\": [\n      \"PATH=/usr/bin\"\n    ]\n  },\n  \"mounts\": [\n    {\"destination\": \"/proc\", \"type\": \"proc\", \"source\": \"proc\"}\n  ]\n}\n"
	dir := h.newBundle("b", []byte(pretty))

	h.run(h.root, "create", "--bundle", dir, "ctr1")

	cfg := readShimConfig(t, filepath.Join(dir, "config.json"))
	mounts := shimMounts(t, cfg)
	if len(mounts) != 2 {
		t.Fatalf("mounts = %v, want 2 entries", mounts)
	}
	h.assertCAMount(mounts[0])
	assertStrings(t, "process.env", shimEnv(t, cfg), append(append([]string{}, shimInjectedEnv...), "PATH=/usr/bin"))
}

func TestRuncShimScript_BundleFlagFormsAndRun(t *testing.T) {
	cases := []struct {
		name string
		args func(dir string) []string
	}{
		{"bundle-equals", func(dir string) []string { return []string{"create", "--bundle=" + dir, "ctr1"} }},
		{"short-b", func(dir string) []string { return []string{"create", "-b", dir, "ctr1"} }},
		{"run-verb", func(dir string) []string { return []string{"--log", "/dev/null", "run", "--bundle", dir, "ctr1"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newShimHarness(t)
			dir := h.newBundle("b", []byte(shimBasicConfig))

			h.run(h.root, tc.args(dir)...)

			cfg := readShimConfig(t, filepath.Join(dir, "config.json"))
			mounts := shimMounts(t, cfg)
			if len(mounts) != 2 {
				t.Fatalf("mounts = %v, want 2 entries", mounts)
			}
			h.assertCAMount(mounts[0])
			env := shimEnv(t, cfg)
			if len(env) == 0 || env[0] != shimInjectedEnv[0] {
				t.Fatalf("process.env = %q, want SSL_CERT_FILE first", env)
			}
		})
	}
}

func TestRuncShimScript_Idempotent(t *testing.T) {
	h := newShimHarness(t)
	dir := h.newBundle("b", []byte(shimBasicConfig))

	h.run(h.root, "create", "--bundle", dir, "ctr1")
	first, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	h.run(h.root, "create", "--bundle", dir, "ctr1")
	second, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !bytes.Equal(first, second) {
		t.Fatalf("second run changed config:\n%s\n---\n%s", first, second)
	}

	cfg := readShimConfig(t, filepath.Join(dir, "config.json"))
	ca := 0
	for _, m := range shimMounts(t, cfg) {
		if m["destination"] == "/etc/nexus/ca" {
			ca++
		}
	}
	if ca != 1 {
		t.Fatalf("CA mounts = %d, want 1", ca)
	}
	ssl := 0
	for _, e := range shimEnv(t, cfg) {
		if strings.HasPrefix(e, "SSL_CERT_FILE=") {
			ssl++
		}
	}
	if ssl != 1 {
		t.Fatalf("SSL_CERT_FILE entries = %d, want 1", ssl)
	}
}

func TestRuncShimScript_InertWhenCAGateClosed(t *testing.T) {
	h := newShimHarness(t)
	if err := os.Remove(h.caCrt); err != nil {
		t.Fatal(err)
	}
	dir := h.newBundle("b", []byte(shimBasicConfig))

	h.run(h.root, "create", "--bundle", dir, "ctr1")

	got, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if string(got) != shimBasicConfig {
		t.Fatalf("config.json modified with CA gate closed:\n%s", got)
	}
	if _, err := os.Stat(h.trustDir); !os.IsNotExist(err) {
		t.Fatalf("trust dir created with CA gate closed (stat err=%v)", err)
	}
}

func TestRuncShimScript_PassThroughNonCreateVerbs(t *testing.T) {
	cases := [][]string{
		{"--root", "/x", "delete", "id"},
		{"features"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			h := newShimHarness(t)
			dir := h.newBundle("b", []byte(shimBasicConfig))

			h.run(dir, args...)

			got, _ := os.ReadFile(filepath.Join(dir, "config.json"))
			if string(got) != shimBasicConfig {
				t.Fatalf("config.json modified by non-create verb:\n%s", got)
			}
			if _, err := os.Stat(h.trustDir); !os.IsNotExist(err) {
				t.Fatalf("trust dir created by non-create verb (stat err=%v)", err)
			}
		})
	}
}

func TestRuncShimScript_RefreshesStaleTrustBundle(t *testing.T) {
	h := newShimHarness(t)
	if err := os.MkdirAll(h.trustDir, 0755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(h.trustDir, "ca-certificates.crt")
	mustWrite(t, stale, []byte("old bundle\n"), 0644)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	dir := h.newBundle("b", []byte(shimBasicConfig))

	h.run(h.root, "create", "--bundle", dir, "ctr1")

	h.assertTrustDir()
}

func TestRuncShimScript_MissingConfigJSON(t *testing.T) {
	h := newShimHarness(t)
	dir := h.newBundle("b", nil)

	h.run(h.root, "create", "--bundle", dir, "ctr1")

	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("config.json appeared (stat err=%v)", err)
	}
	if _, err := os.Stat(h.trustDir); !os.IsNotExist(err) {
		t.Fatalf("trust dir created without config.json (stat err=%v)", err)
	}
}
