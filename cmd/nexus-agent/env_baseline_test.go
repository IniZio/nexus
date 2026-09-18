package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/agent/wire"
	"github.com/IniZio/nexus/internal/core/bootspec"
)

// TestGuestBaselineEnv verifies that guestBaselineEnv returns a sensible default
// environment suitable for exec'd processes when the agent runs as PID 1.
func TestGuestBaselineEnv(t *testing.T) {
	env := guestBaselineEnv(false)

	first := envFirstValues(env)

	// HOME must be /root (uid 0 → /root in the guest passwd).
	if got, ok := first["HOME"]; !ok {
		t.Error("HOME missing from guestBaselineEnv()")
	} else if got != "/root" {
		t.Errorf("HOME = %q; want /root", got)
	}

	// PATH must be a non-empty string containing at least /usr/bin and /bin.
	path, ok := first["PATH"]
	if !ok {
		t.Fatal("PATH missing from guestBaselineEnv()")
	}
	for _, want := range []string{"/usr/bin", "/bin"} {
		if !strings.Contains(path, want) {
			t.Errorf("PATH %q does not contain %q", path, want)
		}
	}
}

// TestEnvBaselineCallerWins verifies that req.Env always overrides the baseline —
// a regression guard for the glibc first-match rule.
// The Linux kernel injects HOME=/ into PID 1's os.Environ(); the Exec path
// deliberately does NOT pass os.Environ() through so that the kernel's wrong
// HOME=/ cannot override our correct baseline. Callers that know better can
// still override via req.Env.
func TestEnvBaselineCallerWins(t *testing.T) {
	// Simulate: caller sets HOME to a custom value that overrides the baseline.
	env := mergeEnv(guestBaselineEnv(false), map[string]string{"HOME": "/custom"})

	first := envFirstValues(env)
	if got := first["HOME"]; got != "/custom" {
		t.Errorf("HOME = %q; caller value /custom should win over baseline /root", got)
	}
}

// TestEnvBaselineKernelHomeIgnored documents why os.Environ() is not passed to
// exec'd processes. The Linux kernel sets HOME=/ in the PID 1 environment; if
// os.Environ() were merged after the baseline, that wrong value would override
// HOME=/root and we would regress to the original defect.
func TestEnvBaselineKernelHomeIgnored(t *testing.T) {
	// The kernel HOME=/ must NOT make it into exec'd processes. The Exec path
	// uses only guestBaselineEnv() + req.Env; os.Environ() is excluded.
	// Verify: mergeEnv(baseline, nil) — simulating no caller req.Env — gives /root.
	env := mergeEnv(guestBaselineEnv(false), nil)
	first := envFirstValues(env)
	if got := first["HOME"]; got != "/root" {
		t.Errorf("HOME = %q; baseline /root should hold when no caller override provided", got)
	}
}

// TestEnvToMap verifies the envToMap helper correctly parses KEY=VALUE pairs and
// preserves first-match semantics for duplicate keys.
func TestEnvToMap(t *testing.T) {
	m := envToMap([]string{"A=1", "B=two", "A=3", "NOVALUE", "EQ=a=b"})

	if got := m["A"]; got != "1" {
		t.Errorf("A = %q; want 1 (first-match)", got)
	}
	if got := m["B"]; got != "two" {
		t.Errorf("B = %q; want two", got)
	}
	if got := m["EQ"]; got != "a=b" {
		t.Errorf("EQ = %q; want a=b", got)
	}
	if _, ok := m["NOVALUE"]; ok {
		t.Error("NOVALUE (no '=' in entry) should not appear in map")
	}
}

// envFirstValues builds a key→first-value map from a "KEY=VAL" slice,
// mirroring glibc getenv() first-match semantics.
func envFirstValues(env []string) map[string]string {
	m := make(map[string]string)
	for _, e := range env {
		idx := strings.IndexByte(e, '=')
		if idx < 0 {
			continue
		}
		k := e[:idx]
		if _, exists := m[k]; !exists {
			m[k] = e[idx+1:]
		}
	}
	return m
}

// TestGuestBaselineEtcEnvironment verifies that guestBaselineEnv picks up
// variables written to /etc/environment (by the Containerfile RUN that
// materialises OCI ENV declarations as real filesystem entries).
//
// Mechanism: OCI ENV metadata lives only in the image config JSON and is never
// read by the guest (the VM boots init=/sbin/nexus-agent directly from ext4;
// no container runtime ever reads Config.Env). Writing /etc/environment via a
// Containerfile RUN creates a real file that survives ext4 conversion.
// readEtcEnvironment() reads that file at exec time and guestBaselineEnv()
// merges it on top of the hardcoded fallback.
//
// Mutation guard: if the readEtcEnvironment() call is removed from
// guestBaselineEnv(), GOPATH and GOMODCACHE will be absent from the returned
// slice and this test will fail — that is the intended regression signal.
func TestGuestBaselineEtcEnvironment(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "etc-environment-*")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	fmt.Fprintln(f, "GOPATH=/go")
	fmt.Fprintln(f, "GOMODCACHE=/go/pkg/mod")
	fmt.Fprintln(f, "CGO_ENABLED=0")
	f.Close()

	// Redirect the package-level path; restore it after the test.
	orig := etcEnvironmentPath
	etcEnvironmentPath = f.Name()
	defer func() { etcEnvironmentPath = orig }()

	env := guestBaselineEnv(false)
	m := envFirstValues(env)

	for key, want := range map[string]string{
		"GOPATH":      "/go",
		"GOMODCACHE":  "/go/pkg/mod",
		"CGO_ENABLED": "0",
	} {
		got, ok := m[key]
		if !ok {
			t.Errorf("%s missing from guestBaselineEnv() — readEtcEnvironment propagation broken", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %q; want %q", key, got, want)
		}
	}

	// PATH must include the Go bin dir from /etc/environment.
	if path := m["PATH"]; !strings.Contains(path, "/usr/local/go/bin") {
		t.Errorf("PATH %q does not contain /usr/local/go/bin — readEtcEnvironment propagation broken", path)
	}
}

// TestGuestBaselineEnvScratchPresent verifies that guestBaselineEnv emits
// TMPDIR pointing at the scratch-disk mount point when scratchDiskPresent is true
// (SD-AC4).
func TestGuestBaselineEnvScratchPresent(t *testing.T) {
	env := guestBaselineEnv(true)
	first := envFirstValues(env)
	tmpdir, ok := first["TMPDIR"]
	if !ok {
		t.Fatal("TMPDIR missing from guestBaselineEnv(scratchDiskPresent=true) — TMPDIR rider not emitted")
	}
	if tmpdir != scratchDiskGuestMount {
		t.Errorf("TMPDIR = %q; want scratchDiskGuestMount = %q", tmpdir, scratchDiskGuestMount)
	}
}

// TestGuestBaselineEnvNoScratchByteIdentical verifies that without a scratch
// disk, guestBaselineEnv produces an environment byte-identical to the
// pre-motive output: TMPDIR must be absent (SD-AC4 negative, D-SD-02).
func TestGuestBaselineEnvNoScratchByteIdentical(t *testing.T) {
	env := guestBaselineEnv(false)
	for _, e := range env {
		if strings.HasPrefix(e, "TMPDIR=") {
			t.Errorf("TMPDIR present in no-scratch environment: %q — sandbox without scratch disk must be byte-identical to pre-motive baseline", e)
		}
	}
}

// TestScratchTMPDIRMatchesMountPoint proves that the TMPDIR rider does NOT
// substitute for the mount — it is a courtesy on top (SD-AC4 proof of
// distinction). The scratch disk is mounted at scratchDiskGuestMount (/tmp);
// TMPDIR is set to the same path. Therefore a literal /tmp/... write reaches
// the scratch device regardless of whether the caller consults TMPDIR.
//
// Mutation guard: if the "TMPDIR="+scratchDiskGuestMount append is removed from
// guestBaselineEnv, TestGuestBaselineEnvScratchPresent goes RED. If
// scratchDiskGuestMount is changed away from "/tmp", this test goes RED,
// catching a contract break (the mount point and the rider must stay in sync
// for literal /tmp writes to reach the device).
func TestScratchTMPDIRMatchesMountPoint(t *testing.T) {
	env := guestBaselineEnv(true)
	first := envFirstValues(env)
	tmpdir, ok := first["TMPDIR"]
	if !ok {
		t.Fatal("TMPDIR missing — cannot verify mount-point equivalence")
	}
	// The TMPDIR rider must point at the actual scratch-disk mount point.
	// A literal /tmp/... write reaches the scratch device via the mount; TMPDIR
	// pointing at the same path means both paths reach the same device.
	if tmpdir != scratchDiskGuestMount {
		t.Errorf("TMPDIR %q != scratchDiskGuestMount %q — literal /tmp writes and $TMPDIR writes would diverge", tmpdir, scratchDiskGuestMount)
	}
	// The scratch mount point must be /tmp so that literal /tmp/... writes
	// (which TMPDIR does not intercept) still reach the scratch device.
	const wantMountPoint = "/tmp"
	if scratchDiskGuestMount != wantMountPoint {
		t.Errorf("scratchDiskGuestMount = %q; want %q — mount contract changed, literal /tmp writes no longer reach scratch device", scratchDiskGuestMount, wantMountPoint)
	}
}

// TestInitPid1EnvPathFromEtcEnvironment verifies that initPid1Env lets the
// /etc/environment PATH win over the hardcoded fallback.
//
// This is the exact bug guarded here: when the hardcoded default was applied
// before reading /etc/environment, the merge loop's "skip keys already set"
// guard prevented the image's PATH from ever landing in the process env.
// initPid1Env reads /etc/environment FIRST; this test proves the invariant.
//
// The test covers the PID-1 init path in main.go (now delegated to
// initPid1Env), not only the guestBaselineEnv exec-env path in control.go.
func TestInitPid1EnvPathFromEtcEnvironment(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "etc-environment-*")
	if err != nil {
		t.Fatal(err)
	}
	// Write a PATH that includes /usr/local/go/bin — the canonical image PATH.
	fmt.Fprintln(f, "PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	fmt.Fprintln(f, "GOPATH=/go")
	f.Close()

	// Redirect the package-level /etc/environment path; restore after the test.
	origEtcEnv := etcEnvironmentPath
	etcEnvironmentPath = f.Name()
	defer func() { etcEnvironmentPath = origEtcEnv }()

	// Simulate PID 1: the kernel supplies no PATH, so clear it now.
	// Restore the original value on exit so we do not pollute other tests.
	origPath := os.Getenv("PATH")
	if err := os.Unsetenv("PATH"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if origPath != "" {
			os.Setenv("PATH", origPath)
		} else {
			os.Unsetenv("PATH")
		}
	}()

	// Also save/restore GOPATH in case the test host has it set.
	origGopath := os.Getenv("GOPATH")
	os.Unsetenv("GOPATH")
	defer func() {
		if origGopath != "" {
			os.Setenv("GOPATH", origGopath)
		} else {
			os.Unsetenv("GOPATH")
		}
	}()

	initPid1Env()

	got := os.Getenv("PATH")
	if !strings.Contains(got, "/usr/local/go/bin") {
		t.Errorf("PATH = %q; want /usr/local/go/bin from /etc/environment to win over hardcoded fallback — initPid1Env merge ordering broken", got)
	}
	// Confirm the hardcoded fallback portions are also present (via /etc/environment value).
	for _, seg := range []string{"/usr/bin", "/bin"} {
		if !strings.Contains(got, seg) {
			t.Errorf("PATH %q does not contain %q", got, seg)
		}
	}
	// GOPATH from /etc/environment must also be set.
	if gp := os.Getenv("GOPATH"); gp != "/go" {
		t.Errorf("GOPATH = %q; want /go from /etc/environment", gp)
	}
}

// TestReadEtcEnvironmentUnquotes verifies pam_env quote handling: a value
// wrapped in matching double or single quotes is unquoted, comments and blank
// lines are skipped, and an unbalanced quote is left untouched.
func TestReadEtcEnvironmentUnquotes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "environment")
	content := "# leading comment\n\n" +
		"FOO=\"bar baz\"\n" +
		"SINGLE='one two'\n" +
		"PLAIN=plain\n" +
		"UNBALANCED=\"open\n" +
		"MIXED=\"a'\n" +
		"EMPTYQ=\"\"\n" +
		"   # indented comment\n" +
		"NOEQ\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := etcEnvironmentPath
	etcEnvironmentPath = p
	t.Cleanup(func() { etcEnvironmentPath = orig })

	env := guestBaselineEnv(false)
	m := envFirstValues(env)
	for key, want := range map[string]string{
		"FOO":        "bar baz",
		"SINGLE":     "one two",
		"PLAIN":      "plain",
		"UNBALANCED": "\"open",
		"MIXED":      "\"a'",
		"EMPTYQ":     "",
	} {
		got, ok := m[key]
		if !ok {
			t.Errorf("%s missing from guestBaselineEnv()", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %q; want %q", key, got, want)
		}
	}
	if _, ok := m["NOEQ"]; ok {
		t.Error("NOEQ (no '=') must not appear")
	}
	for _, e := range env {
		if strings.HasPrefix(e, "#") {
			t.Errorf("comment line leaked into env: %q", e)
		}
	}
}

// TestExecEnvPrecedence drives the real Exec RPC with a temp /etc/environment
// and a temp boot.json and reads the child's environment back over the data
// plane. Precedence: req.Env > OCI ENV (boot.json) > /etc/environment > baseline.
func TestExecEnvPrecedence(t *testing.T) {
	etc := filepath.Join(t.TempDir(), "environment")
	etcContent := "ETC_ONLY=from-etc\n" +
		"BOTH=from-etc\n" +
		"ALL=from-etc\n" +
		"QUOTED=\"q one\"\n" +
		"HOME=/etc-home\n"
	if err := os.WriteFile(etc, []byte(etcContent), 0o644); err != nil {
		t.Fatal(err)
	}
	origEtc := etcEnvironmentPath
	etcEnvironmentPath = etc
	t.Cleanup(func() { etcEnvironmentPath = origEtc })

	origSpec := bootspecPath
	bootspecPath = writeBootspec(t, bootspec.Spec{Tasks: []bootspec.Task{{
		Argv:       []string{"/bin/true"},
		Env:        []string{"OCI_ONLY=from-oci", "BOTH=from-oci", "ALL=from-oci"},
		Background: true,
	}}})
	t.Cleanup(func() { bootspecPath = origSpec })

	client, dataLis, cancel := testHarness(t)
	defer cancel()

	const sid = "s-env-precedence"
	_, err := client.Exec(context.Background(), &agentpb.ExecRequest{
		SessionId: sid,
		Argv: []string{"sh", "-c",
			`printf 'ETC_ONLY=%s\nOCI_ONLY=%s\nBOTH=%s\nALL=%s\nQUOTED=%s\nHOME=%s\n' "$ETC_ONLY" "$OCI_ONLY" "$BOTH" "$ALL" "$QUOTED" "$HOME"`},
		Env: map[string]string{"ALL": "from-req"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	conn, w, r := dialData(t, dataLis)
	if err := w.WriteHandshake(wire.Handshake{SessionID: sid}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	if ack, err := r.ReadFrame(); err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck, got type=%v err=%v", ack.Type, err)
	}
	frames := collectFrames(t, conn, r, 5*time.Second)
	out := dataBytes(frames)
	if gotExit, code := hasExitFrame(frames); !gotExit || code != 0 {
		t.Fatalf("exit frame: got=%v code=%d; output %q", gotExit, code, out)
	}
	got := envFirstValues(strings.Split(strings.TrimSpace(string(out)), "\n"))
	for key, want := range map[string]string{
		"ETC_ONLY": "from-etc",
		"OCI_ONLY": "from-oci",
		"BOTH":     "from-oci",
		"ALL":      "from-req",
		"QUOTED":   "q one",
		"HOME":     "/etc-home",
	} {
		if got[key] != want {
			t.Errorf("%s = %q; want %q (output %q)", key, got[key], want, out)
		}
	}
}

func TestBootSpecEnvTopLevelEnv(t *testing.T) {
	origSpec := bootspecPath
	t.Cleanup(func() { bootspecPath = origSpec })

	cases := []struct {
		name string
		spec bootspec.Spec
		want []string
	}{
		{
			name: "env only, no task",
			spec: bootspec.Spec{Env: []string{"GOPATH=/go", "CGO_ENABLED=0"}},
			want: []string{"GOPATH=/go", "CGO_ENABLED=0"},
		},
		{
			name: "env and task with duplicate pairs",
			spec: bootspec.Spec{
				Env: []string{"A=1", "B=2"},
				Tasks: []bootspec.Task{{
					Argv: []string{"/bin/true"},
					Env:  []string{"A=1", "C=3"},
				}},
			},
			want: []string{"A=1", "B=2", "C=3"},
		},
		{
			name: "legacy task-only manifest",
			spec: bootspec.Spec{Tasks: []bootspec.Task{{
				Argv: []string{"/bin/true"},
				Env:  []string{"X=1"},
			}}},
			want: []string{"X=1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bootspecPath = writeBootspec(t, tc.spec)
			got := bootSpecEnv()
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("bootSpecEnv() = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestBootSpecEnvAbsentOrUnparseable(t *testing.T) {
	origSpec := bootspecPath
	t.Cleanup(func() { bootspecPath = origSpec })

	bootspecPath = filepath.Join(t.TempDir(), "absent.json")
	if got := bootSpecEnv(); got != nil {
		t.Errorf("absent manifest: bootSpecEnv() = %v; want nil", got)
	}

	bootspecPath = filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bootspecPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := bootSpecEnv(); got != nil {
		t.Errorf("unparseable manifest: bootSpecEnv() = %v; want nil", got)
	}
}

func TestGuestBaselineHostUID(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "hostuid-*")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "NEXUS_HOST_UID=2345")
	fmt.Fprintln(f, "NEXUS_HOST_GID=6789")
	f.Close()

	orig := nexusHostUIDEnvPath
	nexusHostUIDEnvPath = f.Name()
	defer func() { nexusHostUIDEnvPath = orig }()

	env := guestBaselineEnv(false)
	m := envFirstValues(env)

	for key, want := range map[string]string{
		"NEXUS_HOST_UID": "2345",
		"NEXUS_HOST_GID": "6789",
	} {
		if got, ok := m[key]; !ok {
			t.Errorf("%s missing from guestBaselineEnv()", key)
		} else if got != want {
			t.Errorf("%s = %q; want %q", key, got, want)
		}
	}
}

func TestReadNexusHostUIDEnvMissing(t *testing.T) {
	orig := nexusHostUIDEnvPath
	nexusHostUIDEnvPath = filepath.Join(t.TempDir(), "absent.env")
	defer func() { nexusHostUIDEnvPath = orig }()

	if got := readNexusHostUIDEnv(); got != nil {
		t.Errorf("absent file: readNexusHostUIDEnv() = %v; want nil", got)
	}
}
