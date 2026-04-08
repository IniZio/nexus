package config

import "testing"

func TestRuntimeRequired_MissingFails(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{
			Required:  []string{},
			Selection: "prefer-first",
		},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected error for missing/empty runtime.required")
	}
}

func TestRuntimeRequired_AllowsLinux(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{
			Required:  []string{"linux"},
			Selection: "prefer-first",
		},
	}

	err := cfg.ValidateBasic()
	if err != nil {
		t.Fatalf("expected linux to validate, got %v", err)
	}
}

func TestRuntimeRequired_RejectsUnknownBackends(t *testing.T) {
	for _, backend := range []string{"dind", "vm", "docker", "kubernetes", "firecracker", "lxc"} {
		cfg := WorkspaceConfig{
			Version: 1,
			Runtime: RuntimeConfig{
				Required:  []string{backend},
				Selection: "prefer-first",
			},
		}

		if err := cfg.ValidateBasic(); err == nil {
			t.Fatalf("expected %s to be rejected", backend)
		}
	}
}

func TestRuntimeRequired_RejectsMixedValidAndInvalid(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{
			Required:  []string{"linux", "invalid-backend"},
			Selection: "prefer-first",
		},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected error when runtime.required contains invalid backend")
	}
}
