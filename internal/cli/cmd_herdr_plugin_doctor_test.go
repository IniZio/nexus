package cli

// Tests for herdrPluginDoctor — specifically the ABI file check branch which
// requires HERDR_PLUGIN_ROOT to be set and could not be reached in a plain
// test environment before these tests were added.
//
// MUTATION PROOF: change `if expected == herdrPluginABIVersion {` to `if true {`
// in herdrPluginDoctor. TestHerdrPluginDoctor_ABIMismatch stops seeing "MISMATCH"
// in the output → RED.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrPluginDoctor_ABIMatch(t *testing.T) {
	// When the abi file in HERDR_PLUGIN_ROOT matches herdrPluginABIVersion,
	// the output must contain "ABI file check: ok".
	// MUTATION PROOF: change the match branch to always report MISMATCH.
	// RED: "ABI file check: ok" not found in output.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abi"), []byte(herdrPluginABIVersion+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_ROOT", dir)

	var w strings.Builder
	if err := herdrPluginDoctor(&w); err != nil {
		t.Fatalf("herdrPluginDoctor: %v", err)
	}
	got := w.String()
	if !strings.Contains(got, "ABI file check: ok") {
		t.Errorf("expected 'ABI file check: ok' in output; got:\n%s", got)
	}
}

func TestHerdrPluginDoctor_ABIMismatch(t *testing.T) {
	// When the abi file in HERDR_PLUGIN_ROOT does NOT match herdrPluginABIVersion,
	// the output must contain "MISMATCH".
	// MUTATION PROOF: change `if expected == herdrPluginABIVersion {` to `if true {`.
	// The mismatch branch is never taken; "MISMATCH" absent → RED.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abi"), []byte("999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_ROOT", dir)

	var w strings.Builder
	if err := herdrPluginDoctor(&w); err != nil {
		t.Fatalf("herdrPluginDoctor: %v", err)
	}
	got := w.String()
	if !strings.Contains(got, "MISMATCH") {
		t.Errorf("expected 'MISMATCH' in output when abi file disagrees; got:\n%s", got)
	}
	if !strings.Contains(got, "999") {
		t.Errorf("expected the mismatched abi value '999' in output; got:\n%s", got)
	}
}

func TestHerdrPluginDoctor_ABIRootUnset(t *testing.T) {
	// When HERDR_PLUGIN_ROOT is unset, the ABI file check must report the
	// "unset" status rather than "ok" or "MISMATCH".
	t.Setenv("HERDR_PLUGIN_ROOT", "")

	var w strings.Builder
	if err := herdrPluginDoctor(&w); err != nil {
		t.Fatalf("herdrPluginDoctor: %v", err)
	}
	got := w.String()
	if !strings.Contains(got, "HERDR_PLUGIN_ROOT unset") {
		t.Errorf("expected 'HERDR_PLUGIN_ROOT unset' status when env is empty; got:\n%s", got)
	}
}
