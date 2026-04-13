package lxc

import (
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime/drivers/shared"
)

func TestFilterCandidatesByAvailability_PrefersConfiguredExistingOrder(t *testing.T) {
	candidates := []string{"nexus-lxc", "default"}
	available := []string{"default", "nexus-firecracker"}

	got := shared.FilterCandidatesSortedFallback(candidates, available)
	if len(got) != 1 || got[0] != "default" {
		t.Fatalf("unexpected filtered candidates: %v", got)
	}
}

func TestFilterCandidatesByAvailability_FallsBackToAvailableWhenNoConfiguredMatch(t *testing.T) {
	candidates := []string{"nexus-lxc"}
	available := []string{"default", "nexus-firecracker"}

	got := shared.FilterCandidatesSortedFallback(candidates, available)
	if len(got) != 2 {
		t.Fatalf("expected 2 fallback candidates, got %v", got)
	}
	if !(got[0] == "default" && got[1] == "nexus-firecracker") {
		t.Fatalf("expected sorted fallback [default nexus-firecracker], got %v", got)
	}
}
