package workspacemgr

import "testing"

func TestValidatePolicy_RejectsUnknownCredentialMode(t *testing.T) {
	err := ValidatePolicy(Policy{GitCredentialMode: GitCredentialMode("invalid")})
	if err == nil {
		t.Fatal("expected error for invalid credential mode")
	}
}

func TestValidatePolicy_AcceptsSupportedModes(t *testing.T) {
	validModes := []GitCredentialMode{
		GitCredentialHostHelper,
		GitCredentialEphemeralHelper,
		GitCredentialNone,
	}

	for _, mode := range validModes {
		err := ValidatePolicy(Policy{GitCredentialMode: mode})
		if err != nil {
			t.Fatalf("expected mode %q to be valid, got %v", mode, err)
		}
	}
}

func TestValidatePolicy_RejectsUnknownAuthProfile(t *testing.T) {
	err := ValidatePolicy(Policy{AuthProfiles: []AuthProfile{"unknown"}})
	if err == nil {
		t.Fatal("expected error for invalid auth profile")
	}
}
