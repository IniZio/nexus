package config

import (
	"os"
	"strings"
	"testing"
)

func TestValidateFirecrackerEnvRejectsRemovedLXCKeys(t *testing.T) {
	// Save and restore original env vars
	originalBackend := os.Getenv("NEXUS_RUNTIME_BACKEND")
	originalExecMode := os.Getenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE")
	originalInstance := os.Getenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE")
	originalDockerMode := os.Getenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")
	defer func() {
		os.Setenv("NEXUS_RUNTIME_BACKEND", originalBackend)
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", originalExecMode)
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE", originalInstance)
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE", originalDockerMode)
	}()

	// Clear all legacy keys first
	os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE")
	os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE")
	os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")

	// Test 1: NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE should be rejected
	t.Run("rejects NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", func(t *testing.T) {
		os.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", "sudo-lxc")
		os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE")
		os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")

		err := ValidateFirecrackerEnv()
		if err == nil {
			t.Fatal("expected error for NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE, got nil")
		}
		if !strings.Contains(err.Error(), "removed in native firecracker cutover") {
			t.Fatalf("expected 'removed in native firecracker cutover' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE") {
			t.Fatalf("expected key name in error, got: %v", err)
		}
	})

	// Test 2: NEXUS_DOCTOR_FIRECRACKER_INSTANCE should be rejected
	t.Run("rejects NEXUS_DOCTOR_FIRECRACKER_INSTANCE", func(t *testing.T) {
		os.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
		os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE")
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE", "ws-123")
		os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")

		err := ValidateFirecrackerEnv()
		if err == nil {
			t.Fatal("expected error for NEXUS_DOCTOR_FIRECRACKER_INSTANCE, got nil")
		}
		if !strings.Contains(err.Error(), "removed in native firecracker cutover") {
			t.Fatalf("expected 'removed in native firecracker cutover' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "NEXUS_DOCTOR_FIRECRACKER_INSTANCE") {
			t.Fatalf("expected key name in error, got: %v", err)
		}
	})

	// Test 3: NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE should be rejected
	t.Run("rejects NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE", func(t *testing.T) {
		os.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
		os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE")
		os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE")
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE", "host-proxy")

		err := ValidateFirecrackerEnv()
		if err == nil {
			t.Fatal("expected error for NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE, got nil")
		}
		if !strings.Contains(err.Error(), "removed in native firecracker cutover") {
			t.Fatalf("expected 'removed in native firecracker cutover' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE") {
			t.Fatalf("expected key name in error, got: %v", err)
		}
	})
}

func TestValidateFirecrackerEnvAllowsNewContract(t *testing.T) {
	// Save and restore original env vars
	originalBackend := os.Getenv("NEXUS_RUNTIME_BACKEND")
	originalKernel := os.Getenv("NEXUS_FIRECRACKER_KERNEL")
	originalRootfs := os.Getenv("NEXUS_FIRECRACKER_ROOTFS")
	originalExecMode := os.Getenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE")
	originalInstance := os.Getenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE")
	originalDockerMode := os.Getenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")
	defer func() {
		os.Setenv("NEXUS_RUNTIME_BACKEND", originalBackend)
		os.Setenv("NEXUS_FIRECRACKER_KERNEL", originalKernel)
		os.Setenv("NEXUS_FIRECRACKER_ROOTFS", originalRootfs)
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", originalExecMode)
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE", originalInstance)
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE", originalDockerMode)
	}()

	// Clear legacy keys first
	os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE")
	os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE")
	os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")

	// Test 4: Valid firecracker config with new keys should pass
	t.Run("allows new firecracker contract", func(t *testing.T) {
		os.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
		os.Setenv("NEXUS_FIRECRACKER_KERNEL", "/var/lib/nexus/vmlinux.bin")
		os.Setenv("NEXUS_FIRECRACKER_ROOTFS", "/var/lib/nexus/rootfs.ext4")

		err := ValidateFirecrackerEnv()
		if err != nil {
			t.Fatalf("expected no error for valid firecracker config, got: %v", err)
		}
	})

	// Test 5: Non-firecracker backend should not validate legacy keys
	t.Run("ignores legacy keys for non-firecracker backend", func(t *testing.T) {
		os.Setenv("NEXUS_RUNTIME_BACKEND", "dind")
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", "sudo-lxc")
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE", "ws-123")

		err := ValidateFirecrackerEnv()
		if err != nil {
			t.Fatalf("expected no error for non-firecracker backend, got: %v", err)
		}
	})

	// Test 6: Empty backend should not validate legacy keys
	t.Run("ignores legacy keys for empty backend", func(t *testing.T) {
		os.Unsetenv("NEXUS_RUNTIME_BACKEND")
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", "sudo-lxc")

		err := ValidateFirecrackerEnv()
		if err != nil {
			t.Fatalf("expected no error for empty backend, got: %v", err)
		}
	})
}

func TestValidateFirecrackerEnvErrorMessages(t *testing.T) {
	// Save and restore original env vars
	originalBackend := os.Getenv("NEXUS_RUNTIME_BACKEND")
	originalExecMode := os.Getenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE")
	originalKernel := os.Getenv("NEXUS_FIRECRACKER_KERNEL")
	originalRootfs := os.Getenv("NEXUS_FIRECRACKER_ROOTFS")
	defer func() {
		os.Setenv("NEXUS_RUNTIME_BACKEND", originalBackend)
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", originalExecMode)
		os.Setenv("NEXUS_FIRECRACKER_KERNEL", originalKernel)
		os.Setenv("NEXUS_FIRECRACKER_ROOTFS", originalRootfs)
	}()

	// Clear legacy keys
	os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE")
	os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE")
	os.Unsetenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")

	// Test 7: Error message should include migration guidance
	t.Run("error includes migration guidance", func(t *testing.T) {
		os.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
		os.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", "sudo-lxc")

		err := ValidateFirecrackerEnv()
		if err == nil {
			t.Fatal("expected error")
		}

		// Should mention the new required keys
		if !strings.Contains(err.Error(), "NEXUS_FIRECRACKER_KERNEL") {
			t.Fatalf("expected NEXUS_FIRECRACKER_KERNEL in migration guidance, got: %v", err)
		}
		if !strings.Contains(err.Error(), "NEXUS_FIRECRACKER_ROOTFS") {
			t.Fatalf("expected NEXUS_FIRECRACKER_ROOTFS in migration guidance, got: %v", err)
		}
	})
}
