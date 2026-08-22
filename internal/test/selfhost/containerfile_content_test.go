// Package selfhost — unit tests for generateAgentContainerfile output content.
// No build tags: runs on every "go test ./..." invocation.
// These tests assert on the STRING OUTPUT of generateAgentContainerfile so that
// removing the mechanism makes the test RED — a test on a static file would be
// inert for the same reason the wrong-file bug was inert.
package selfhost

import (
	"strings"
	"testing"
)

// sampleContainerfile calls generateAgentContainerfile with stable inputs.
// srcDirs uses a known non-empty subset so the %s COPY block is populated.
func sampleContainerfile(t *testing.T) string {
	t.Helper()
	cf := generateAgentContainerfile(GoVersion, "deadbeef"+strings.Repeat("0", 56), []string{"cmd", "internal"})
	if cf == "" {
		t.Fatal("generateAgentContainerfile returned empty string")
	}
	return cf
}

// TestGeneratedContainerfileGoSymlinks asserts that the generated Containerfile
// symlinks /usr/local/go/bin/go and /usr/local/go/bin/gofmt into /usr/bin,
// making both reachable on the caller-injected guest PATH
// ("/usr/bin:/bin:/usr/sbin:/sbin").
//
// Mutation test (verbatim):
//
//	Delete the two "ln -sf /usr/local/go/bin/go" lines → test prints:
//	  "generated Containerfile does not symlink go into /usr/bin"
//	  "generated Containerfile does not symlink gofmt into /usr/bin"
func TestGeneratedContainerfileGoSymlinks(t *testing.T) {
	cf := sampleContainerfile(t)

	if !strings.Contains(cf, "ln -sf /usr/local/go/bin/go /usr/bin/go") {
		t.Error("generated Containerfile does not symlink go into /usr/bin")
	}
	if !strings.Contains(cf, "ln -sf /usr/local/go/bin/gofmt /usr/bin/gofmt") {
		t.Error("generated Containerfile does not symlink gofmt into /usr/bin")
	}
}

// TestGeneratedContainerfileEtcEnvironment asserts that the generated
// Containerfile writes /etc/environment using the DERIVED form (printf '%s=%s\n'
// with shell-variable expansions of the ENV declarations, not re-typed literals).
// This is required because nexus3-agent's readEtcEnvironment() merges
// /etc/environment into every exec'd process env.
//
// Mutation test (verbatim):
//
//	Delete the "printf '%s=%s\n'" RUN block → test prints:
//	  "generated Containerfile does not write /etc/environment via derived printf"
//	  "generated Containerfile does not write /etc/profile.d/nexus3-go.sh"
//	  "generated Containerfile does not include GOPATH in /etc/environment block"
//	  "generated Containerfile does not include GOMODCACHE in /etc/environment block"
func TestGeneratedContainerfileEtcEnvironment(t *testing.T) {
	cf := sampleContainerfile(t)

	// The mechanism must use printf with shell-variable expansion (not re-typed literals).
	if !strings.Contains(cf, `printf '%s=%s\n'`) {
		t.Error("generated Containerfile does not write /etc/environment via derived printf")
	}
	if !strings.Contains(cf, "/etc/environment") {
		t.Error("generated Containerfile does not reference /etc/environment")
	}
	if !strings.Contains(cf, "/etc/profile.d/nexus3-go.sh") {
		t.Error("generated Containerfile does not write /etc/profile.d/nexus3-go.sh")
	}

	// GOPATH and GOMODCACHE must appear in the printf block.
	if !strings.Contains(cf, `GOPATH "$GOPATH"`) {
		t.Error(`generated Containerfile does not include GOPATH in /etc/environment block`)
	}
	if !strings.Contains(cf, `GOMODCACHE "$GOMODCACHE"`) {
		t.Error(`generated Containerfile does not include GOMODCACHE in /etc/environment block`)
	}
}
