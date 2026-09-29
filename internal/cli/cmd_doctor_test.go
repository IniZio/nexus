package cli

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/hostbin"
)

func doctorTestProbes() probes {
	return probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "/usr/bin/x", nil },
		openKVM:  func() error { return nil },
		userns:   func() error { return nil },
		resolveHostBin: func(_ context.Context, name string) (hostbin.Resolved, error) {
			return hostbin.Resolved{Path: "/opt/" + name, Source: hostbin.SourceEmbedded}, nil
		},
		resolveAgent: func() error { return nil },
	}
}

func checkByName(checks []CheckResult, name string) (CheckResult, bool) {
	for _, c := range checks {
		if c.Name == name {
			return c, true
		}
	}
	return CheckResult{}, false
}

func TestDoctor_Classification(t *testing.T) {
	checks, _ := runAllChecks(doctorTestProbes())
	required := []string{"platform", "binary", "kvm", "userns", "agent", "virtiofsd", "tool_mke2fs", "tool_e2fsck", "tool_resize2fs"}
	for _, n := range required {
		c, ok := checkByName(checks, n)
		if !ok {
			if n == "virtiofsd" {
				continue
			}
			t.Errorf("required check %q missing", n)
			continue
		}
		if c.Optional {
			t.Errorf("check %q must be required", n)
		}
	}
	for _, n := range []string{"git", "ssh", "gh", "lsof", "ss", "ps", "kernel"} {
		c, ok := checkByName(checks, n)
		if !ok {
			if n == "kernel" {
				continue
			}
			t.Errorf("optional check %q missing", n)
			continue
		}
		if !c.Optional {
			t.Errorf("check %q must be optional", n)
		}
	}
	if c, _ := checkByName(checks, "git"); !strings.Contains(c.Detail, "worktree sandboxes") {
		t.Errorf("git detail must name its feature, got %q", c.Detail)
	}
}

func TestDoctor_RequiredMessagesNamePackages(t *testing.T) {
	p := doctorTestProbes()
	p.openKVM = func() error { return os.ErrPermission }
	p.userns = func() error { return errors.New("disabled") }
	p.resolveAgent = func() error { return errors.New("missing") }
	p.resolveHostBin = func(_ context.Context, name string) (hostbin.Resolved, error) {
		return hostbin.Resolved{}, errors.New("missing " + name)
	}
	checks, _ := runAllChecks(p)
	banned := regexp.MustCompile(`(?i)\b(apt|apt-get|dnf|yum|pacman|brew|zypper|apk)\b|e2fsprogs`)
	for _, c := range checks {
		if c.Optional {
			continue
		}
		if banned.MatchString(c.Detail + " " + c.Remediation + " " + c.Description) {
			t.Errorf("required check %q names a distro package: %q / %q", c.Name, c.Detail, c.Remediation)
		}
	}
}

func TestDoctor_OptionalFailureDoesNotFailExit(t *testing.T) {
	p := doctorTestProbes()
	p.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	checks, _ := runAllChecks(p)
	var optFailed bool
	for _, c := range checks {
		if c.Optional && !c.OK {
			optFailed = true
		}
	}
	if !optFailed {
		t.Fatal("expected optional failures with empty PATH")
	}
	if requiredFailed([]CheckResult{{Name: "git", Optional: true}}) {
		t.Error("optional-only failure must not count as required failure")
	}
	baseline, _ := runAllChecks(doctorTestProbes())
	if requiredFailed(checks) != requiredFailed(baseline) {
		t.Error("optional tool failures changed the required verdict")
	}
}

func TestDoctor_RequiredFailureExitsNonZero(t *testing.T) {
	p := doctorTestProbes()
	p.openKVM = func() error { return os.ErrNotExist }
	out, stdout, _ := capture(true)
	err := doctorWith(out, p, "")
	var ec *ExitCodeError
	if !errors.As(err, &ec) || ec.Code == 0 {
		t.Fatalf("want non-zero ExitCodeError, got %v", err)
	}
	var env map[string]any
	decodeOne(t, stdout, &env)
	if env["kind"] != "doctor" {
		t.Errorf("kind = %v, want doctor", env["kind"])
	}
}

func TestDoctor_UsernsNet(t *testing.T) {
	p := doctorTestProbes()
	p.usernsNet = func() error { return errors.New("restricted") }
	checks, _ := runAllChecks(p)
	c, ok := checkByName(checks, "userns_net")
	if !ok || c.OK || c.Optional {
		t.Fatalf("want required failing userns_net, got %+v ok=%v", c, ok)
	}
	if !strings.Contains(c.Remediation, "apparmor_restrict_unprivileged_userns=0") {
		t.Errorf("remediation missing sysctl: %q", c.Remediation)
	}
	out, _, _ := capture(true)
	var ec *ExitCodeError
	if err := doctorWith(out, p, ""); !errors.As(err, &ec) || ec.Code != 1 {
		t.Errorf("want exit 1, got %v", err)
	}

	p.usernsNet = func() error { return nil }
	checks, _ = runAllChecks(p)
	if c, _ := checkByName(checks, "userns_net"); !c.OK {
		t.Errorf("unrestricted must be OK, got %+v", c)
	}
}

func TestFormatDoctorHuman_Groups(t *testing.T) {
	out := formatDoctorHuman("none", false, []CheckResult{
		{Name: "kvm", OK: true},
		{Name: "git", OK: false, Optional: true, Detail: "git: worktree sandboxes"},
	})
	ri, oi := strings.Index(out, "Required:"), strings.Index(out, "Optional:")
	if ri < 0 || oi < ri {
		t.Errorf("want Required before Optional headings, got:\n%s", out)
	}
	if strings.Index(out, "kvm") > oi || strings.Index(out, "git") < oi {
		t.Errorf("checks under wrong heading:\n%s", out)
	}
}

// TestDoctor_ExitZero_NoSubstrate verifies that doctor exits 0 even when the
// substrate is explicitly disabled via NEXUS_SUBSTRATE=none.
func TestDoctor_ExitZero_NoSubstrate(t *testing.T) {
	t.Setenv("NEXUS_SUBSTRATE", "none")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"doctor"})
	if code != 0 {
		t.Errorf("doctor with NEXUS_SUBSTRATE=none: exit code = %d, want 0", code)
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

// TestDoctor_JSON_NoSubstrate verifies the envelope when NEXUS_SUBSTRATE=none:
// selected must be false and checks must be an empty (not null) array.
func TestDoctor_JSON_NoSubstrate(t *testing.T) {
	t.Setenv("NEXUS_SUBSTRATE", "none")

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
		t.Errorf("data.selected = %v, want false when NEXUS_SUBSTRATE=none", data["selected"])
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

// TestDoctor_JSON_InvalidOverride verifies that an unrecognised NEXUS_SUBSTRATE
// value still exits 0 and produces a valid envelope.
func TestDoctor_JSON_InvalidOverride(t *testing.T) {
	t.Setenv("NEXUS_SUBSTRATE", "fake")

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
