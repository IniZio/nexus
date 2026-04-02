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

func TestRuntimeRequired_AllowsFirecracker(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{
			Required:  []string{"firecracker"},
			Selection: "prefer-first",
		},
	}

	err := cfg.ValidateBasic()
	if err != nil {
		t.Fatalf("expected firecracker to validate, got %v", err)
	}
}

func TestRuntimeRequired_AllowsLocal(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{
			Required:  []string{"local"},
			Selection: "prefer-first",
		},
	}

	err := cfg.ValidateBasic()
	if err != nil {
		t.Fatalf("expected local to validate, got %v", err)
	}
}

func TestRuntimeRequired_AllowsBothFirecrackerAndLocal(t *testing.T) {
	cfg := WorkspaceConfig{
		Version: 1,
		Runtime: RuntimeConfig{
			Required:  []string{"firecracker", "local"},
			Selection: "prefer-first",
		},
	}

	err := cfg.ValidateBasic()
	if err != nil {
		t.Fatalf("expected firecracker+local to validate, got %v", err)
	}
}

func TestRuntimeRequired_RejectsUnknownBackends(t *testing.T) {
	for _, backend := range []string{"dind", "lxc", "vm", "docker", "kubernetes"} {
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
			Required:  []string{"firecracker", "invalid-backend"},
			Selection: "prefer-first",
		},
	}

	err := cfg.ValidateBasic()
	if err == nil {
		t.Fatal("expected error when runtime.required contains invalid backend")
	}
}
