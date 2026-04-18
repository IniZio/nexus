package config

import "testing"

func TestWorkspaceConfig_ZeroVersionAllowedForConventionMode(t *testing.T) {
	var cfg WorkspaceConfig
	if err := cfg.ValidateBasic(); err != nil {
		t.Fatalf("expected zero-value config to be valid, got %v", err)
	}
}

func TestWorkspaceConfig_VersionOneValid(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
	}
	if err := cfg.ValidateBasic(); err != nil {
		t.Fatalf("expected version=1 to be valid, got %v", err)
	}
}

func TestWorkspaceConfig_NegativeVersionRejected(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: -1,
	}
	if err := cfg.ValidateBasic(); err == nil {
		t.Fatal("expected validation error for negative version")
	}
}
