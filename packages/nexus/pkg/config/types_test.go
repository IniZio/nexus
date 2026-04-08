package config

import "testing"

func TestWorkspaceConfig_VersionRequired(t *testing.T) {
	var cfg WorkspaceConfig
	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected error for missing/invalid version")
	}
}

func TestWorkspaceConfig_ReadinessCheckNameRequired(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Readiness: ReadinessConfig{
			Profiles: map[string][]ReadinessCheck{
				"default": {{Name: ""}},
			},
		},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for empty check name")
	}
}

func TestWorkspaceConfig_ValidMinimal(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{Required: []string{"linux"}},
	}
	err := cfg.ValidateBasic()
	if err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestWorkspaceConfig_DoctorRequiredHostPortValidation(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Doctor:  DoctorConfig{RequiredHostPorts: []int{0}},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for invalid doctor.requiredHostPorts value")
	}
}

func TestWorkspaceConfig_DoctorProbeValidation(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Doctor:  DoctorConfig{Probes: []DoctorCommandProbe{{Name: "", Command: "bash"}}},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for empty doctor probe name")
	}
}

func TestWorkspaceConfig_DoctorProbeRetriesValidation(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Doctor:  DoctorConfig{Probes: []DoctorCommandProbe{{Name: "runtime", Command: "bash", Retries: -1}}},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for negative retries")
	}
}

func TestWorkspaceConfig_RuntimeAndDoctorTestsValidation(t *testing.T) {
	cfg := WorkspaceConfig{
		Version:      1,
		Runtime:      RuntimeConfig{Required: []string{"linux"}, Selection: "prefer-first"},
		Capabilities: CapabilityRequirements{Required: []string{"spotlight.tunnel"}},
		Doctor:       DoctorConfig{Tests: []DoctorCommandCheck{{Name: "auth-flow", Command: "bash", Args: []string{".nexus/lifecycles/test-auth-flow.sh"}, Required: true}}},
	}
	if err := cfg.ValidateBasic(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestWorkspaceConfig_RuntimeRequired_AllowsLinux(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{Required: []string{"linux"}, Selection: "prefer-first"},
	}

	if err := cfg.ValidateBasic(); err != nil {
		t.Fatalf("expected linux runtime to validate, got %v", err)
	}
}

func TestRuntimeRequired_RejectsLegacyAndGenericBackends(t *testing.T) {
	for _, backend := range []string{"dind", "vm", "firecracker", "lxc"} {
		cfg := WorkspaceConfig{
			Version: 1,
			Runtime: RuntimeConfig{Required: []string{backend}, Selection: "prefer-first"},
		}

		if err := cfg.ValidateBasic(); err == nil {
			t.Fatalf("expected %s to be rejected", backend)
		}
	}
}

func TestWorkspaceConfig_InvalidRuntimeRequiredValue(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{Required: []string{"invalid-backend"}},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for invalid runtime.required value")
	}
}

func TestWorkspaceConfig_InvalidRuntimeSelection(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{Selection: "invalid-selection"},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for invalid runtime.selection")
	}
}

func TestWorkspaceConfig_DoctorTestsMissingCommand(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Doctor:  DoctorConfig{Tests: []DoctorCommandCheck{{Name: "auth-flow", Command: ""}}},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for doctor.tests missing command")
	}
}

func TestWorkspaceConfig_DoctorTestsMissingName(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Doctor:  DoctorConfig{Tests: []DoctorCommandCheck{{Name: "", Command: "bash"}}},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for doctor.tests missing name")
	}
}

func TestWorkspaceConfig_DoctorTestsNegativeTimeoutMs(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Doctor:  DoctorConfig{Tests: []DoctorCommandCheck{{Name: "auth-flow", Command: "bash", TimeoutMs: -1}}},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for negative doctor.tests[].timeoutMs")
	}
}

func TestWorkspaceConfig_DoctorTestsNegativeRetries(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Doctor:  DoctorConfig{Tests: []DoctorCommandCheck{{Name: "auth-flow", Command: "bash", Retries: -1}}},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected validation error for negative doctor.tests[].retries")
	}
}
