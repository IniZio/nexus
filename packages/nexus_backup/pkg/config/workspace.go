package config

import (
	"fmt"
	"os"
	"strings"
)

// ValidateFirecrackerEnv validates that when NEXUS_RUNTIME_BACKEND=firecracker,
// removed legacy env keys are rejected with migration guidance.
func ValidateFirecrackerEnv() error {
	if strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_BACKEND")) != "firecracker" {
		return nil
	}

	removedKeys := []string{
		"NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE",
		"NEXUS_DOCTOR_FIRECRACKER_INSTANCE",
		"NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE",
	}

	for _, key := range removedKeys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return fmt.Errorf(
				"%s was removed in native firecracker cutover; configure NEXUS_FIRECRACKER_KERNEL and NEXUS_FIRECRACKER_ROOTFS instead",
				key,
			)
		}
	}

	return nil
}
