package service_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Cross-process helper mode: the test binary is re-executed as a subprocess
	// helper for the volume guard concurrency tests (volume_guard_xproc_test.go).
	// This must be checked BEFORE the in-VM skip below, so helpers run inside
	// guest VMs without being silently discarded.
	if helper := os.Getenv("NEXUS3_VOL_GUARD_HELPER"); helper != "" {
		runSubprocessHelper(helper)
		panic("unreachable — runSubprocessHelper calls os.Exit")
	}

	if proc1comm, err := os.ReadFile("/proc/1/comm"); err == nil {
		if strings.TrimSpace(string(proc1comm)) == "nexus3-agent" {
			fmt.Fprintln(os.Stderr, "service: skipping tests — running inside nexus3 guest VM (host-side package)")
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
