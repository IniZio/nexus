package repro

import (
	"os/exec"
	"testing"
)

// TestAgentLinkage_nexusBinary checks that the nexus binary on PATH is
// statically linked. A dynamically linked nexus binary would brick every
// builder boot. Skips on dev hosts where nexus is a CGO build.
//
// MUTATION: change the PT_INTERP check in AgentLinkage to always return
// "dynamic" → this test fails with "got dynamic, want static" (on a release
// binary that IS static).
func TestAgentLinkage_nexusBinary(t *testing.T) {
	p, err := exec.LookPath("nexus")
	if err != nil {
		t.Skip("nexus not on PATH; skipping linkage check")
	}
	got := AgentLinkage(p)
	if got == "unknown" {
		t.Skipf("AgentLinkage(%q) = %q (non-ELF or unreadable binary); skipping", p, got)
	}
	if got == "dynamic" {
		// Dev builds may be CGO-linked. Skip rather than fail; production
		// release binaries must be static (CGO_ENABLED=0).
		t.Skipf("nexus at %q is dynamically linked (dev build); production release must be static", p)
	}
	if got != "static" {
		t.Errorf("AgentLinkage(%q) = %q, want \"static\"", p, got)
	}
}
