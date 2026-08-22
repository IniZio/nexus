package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestGuestBaselineEnv verifies that guestBaselineEnv returns a sensible default
// environment suitable for exec'd processes when the agent runs as PID 1.
func TestGuestBaselineEnv(t *testing.T) {
	env := guestBaselineEnv()

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
	env := mergeEnv(guestBaselineEnv(), map[string]string{"HOME": "/custom"})

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
	env := mergeEnv(guestBaselineEnv(), nil)
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
// read by the guest (the VM boots init=/sbin/nexus3-agent directly from ext4;
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

	env := guestBaselineEnv()
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
