package cli

import (
	"context"
	"strings"
	"testing"
)

// TestDoctor_ExitZero_WithSubstrate verifies that the doctor command exits 0
// regardless of whether a substrate is available. Doctor is a diagnostic; it
// must never exit non-zero.
func TestDoctor_ExitZero_WithSubstrate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"doctor"})
	if code != 0 {
		t.Errorf("doctor: exit code = %d, want 0", code)
	}
}

// TestDoctor_ExitZero_NoSubstrate verifies that doctor exits 0 even when the
// substrate is explicitly disabled via NEXUS3_SUBSTRATE=none.
func TestDoctor_ExitZero_NoSubstrate(t *testing.T) {
	t.Setenv("NEXUS3_SUBSTRATE", "none")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"doctor"})
	if code != 0 {
		t.Errorf("doctor with NEXUS3_SUBSTRATE=none: exit code = %d, want 0", code)
	}
}

// TestDoctor_JSON_Parseable verifies that --json produces exactly one
// schema-versioned envelope object in both the substrate-available and
// no-substrate cases.
func TestDoctor_JSON_Parseable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	out, stdout, _ := capture(true)
	if err := runDoctor(context.Background(), []string{}, out); err != nil {
		t.Fatalf("runDoctor returned non-nil error: %v (doctor must always return nil)", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if v, ok := env["schema_version"].(float64); !ok || v != 1 {
		t.Errorf("schema_version: got %v, want 1", env["schema_version"])
	}
	if env["kind"] != "doctor" {
		t.Errorf("kind = %v, want \"doctor\"", env["kind"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not a JSON object; got %T", env["data"])
	}
	if _, ok := data["selected"]; !ok {
		t.Error("data.selected field is missing")
	}
	if _, ok := data["substrate"]; !ok {
		t.Error("data.substrate field is missing")
	}
	if _, ok := data["checks"]; !ok {
		t.Error("data.checks field is missing")
	}
}

// TestDoctor_JSON_NoSubstrate verifies the envelope when NEXUS3_SUBSTRATE=none:
// selected must be false and checks must be an empty (not null) array.
func TestDoctor_JSON_NoSubstrate(t *testing.T) {
	t.Setenv("NEXUS3_SUBSTRATE", "none")

	out, stdout, _ := capture(true)
	if err := runDoctor(context.Background(), []string{}, out); err != nil {
		t.Fatalf("runDoctor returned non-nil error: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "doctor" {
		t.Errorf("kind = %v, want \"doctor\"", env["kind"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not a JSON object")
	}
	if data["selected"] != false {
		t.Errorf("data.selected = %v, want false when NEXUS3_SUBSTRATE=none", data["selected"])
	}
	if data["substrate"] != "none" {
		t.Errorf("data.substrate = %v, want \"none\"", data["substrate"])
	}
	// checks must be an array (may be empty, but not null)
	checks, ok := data["checks"].([]any)
	if !ok {
		t.Errorf("data.checks is %T, want []any (JSON array)", data["checks"])
	}
	if len(checks) != 0 {
		t.Errorf("data.checks: expected empty array for override=none, got %d items", len(checks))
	}
}

// TestDoctorChecksJSON_FailedCheck verifies that toDoctorChecksJSON correctly
// converts a failed check — ok:false and non-empty remediation text preserved.
func TestDoctorChecksJSON_FailedCheck(t *testing.T) {
	raw := []CheckResult{
		{
			Name:        "kvm",
			Description: "/dev/kvm accessible",
			OK:          false,
			Detail:      "/dev/kvm: permission denied",
			Remediation: "add user to kvm group: sudo usermod -aG kvm $USER",
		},
	}
	got := toDoctorChecksJSON(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 check, got %d", len(got))
	}
	c := got[0]
	if c.OK {
		t.Error("ok should be false for failed check")
	}
	if c.Name != "kvm" {
		t.Errorf("name = %q, want \"kvm\"", c.Name)
	}
	if c.Remediation == "" {
		t.Error("remediation should be non-empty for failed check")
	}
	if c.Detail != "/dev/kvm: permission denied" {
		t.Errorf("detail = %q, unexpected", c.Detail)
	}
}

// TestFormatDoctorHuman_FailedCheck verifies that formatDoctorHuman emits
// remediation text for a failed check in the human-readable path.
func TestFormatDoctorHuman_FailedCheck(t *testing.T) {
	checks := []CheckResult{
		{Name: "platform", Description: "OS is Linux", OK: true, Detail: "linux"},
		{
			Name:        "kvm",
			Description: "/dev/kvm accessible",
			OK:          false,
			Detail:      "/dev/kvm: permission denied",
			Remediation: "add user to kvm group: sudo usermod -aG kvm $USER",
		},
	}
	out := formatDoctorHuman("none", false, checks)
	if !strings.Contains(out, "FAIL") {
		t.Error("human output should contain FAIL for failed check")
	}
	if !strings.Contains(out, "kvm group") {
		t.Error("human output should contain remediation text for failed check")
	}
}

// TestDoctor_JSON_InvalidOverride verifies that an unrecognised NEXUS3_SUBSTRATE
// value still exits 0 and produces a valid envelope.
func TestDoctor_JSON_InvalidOverride(t *testing.T) {
	t.Setenv("NEXUS3_SUBSTRATE", "fake")

	out, stdout, _ := capture(true)
	if err := runDoctor(context.Background(), []string{}, out); err != nil {
		t.Fatalf("runDoctor returned non-nil error for invalid override: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "doctor" {
		t.Errorf("kind = %v, want \"doctor\"", env["kind"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not a JSON object")
	}
	if data["selected"] != false {
		t.Errorf("data.selected = %v, want false for invalid override", data["selected"])
	}
}
