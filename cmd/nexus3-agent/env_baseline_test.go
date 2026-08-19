package main

import (
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
