package safeenv

import (
	"strings"
	"testing"
)

func TestBaseIncludesAllowedAndExcludesUnknown(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("NEXUS_SECRET_SHOULD_NOT_LEAK", "secret")

	got := Base()
	joined := strings.Join(got, "\n")

	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("expected PATH in base env, got %q", joined)
	}
	if !strings.Contains(joined, "HOME=/tmp/home") {
		t.Fatalf("expected HOME in base env, got %q", joined)
	}
	if strings.Contains(joined, "NEXUS_SECRET_SHOULD_NOT_LEAK=secret") {
		t.Fatalf("unexpected secret key in base env: %q", joined)
	}
}

