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
	cfg := WorkspaceConfig{Version: 1}
	err := cfg.ValidateBasic()
	if err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}
