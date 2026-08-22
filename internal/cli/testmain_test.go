package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if proc1comm, err := os.ReadFile("/proc/1/comm"); err == nil {
		if strings.TrimSpace(string(proc1comm)) == "nexus3-agent" {
			fmt.Fprintln(os.Stderr, "cli: skipping tests — running inside nexus3 guest VM (host-side package)")
			os.Exit(0)
		}
	}
	// Redirect the durable state root for the package run — same reason as
	// internal/core/service's TestMain (TBD-PD-29). CLI tests that reach
	// store.DefaultRoot() otherwise deposit stub disks in the OPERATOR's real
	// ~/.local/state/nexus3/disks, where `nexus3 reap` reports every one as an
	// ORPHAN and the reaper stops being readable as a teardown signal.
	// store.DefaultRoot() reads XDG_STATE_HOME first (store.go:116).
	stateRoot, err := os.MkdirTemp("", "nexus3-cli-state-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli: create temp state root: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_STATE_HOME", stateRoot); err != nil {
		fmt.Fprintf(os.Stderr, "cli: set XDG_STATE_HOME: %v\n", err)
		os.Exit(1)
	}

	// The install-default-shell probe runs the installed binary as
	// "nexus3-guest-shell". In tests the installed binary is the test runner
	// which lacks the argv[0] dispatch, so skip the probe to avoid spurious failure.
	herdrSkipInstallProbeForTest = true

	// os.Exit skips defers, so clean up explicitly around the run.
	code := m.Run()
	_ = os.RemoveAll(stateRoot)
	os.Exit(code)
}
