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
	// Redirect the durable state root into a throwaway directory for the whole
	// package run.
	//
	// Several CreateAndBoot tests leave CreateAndBootOptions.DiskDir empty, which
	// falls back to defaultDiskDir() -> store.DefaultRoot()/disks. Unset, that is
	// the OPERATOR's real ~/.local/state/nexus3/disks, and each run deposited a
	// 16-byte "fake-ext4-rootfs" stub there. The cost is not disk space: every
	// stub is reported by `nexus3 reap` as an ORPHAN, so the reaper's output
	// stops being readable as a clean-teardown signal for real work. Measured
	// 2026-08-19: 118 files in the operator's disks dir, ALL of them test stubs,
	// zero real disks (TBD-PD-29).
	//
	// This is set here rather than fixed at the ~15 call sites that omit DiskDir,
	// because a call-site fix leaves the next test free to reintroduce the leak.
	// store.DefaultRoot() reads XDG_STATE_HOME first (store.go:116), so pointing
	// it at a temp dir makes escaping the test tree structurally impossible.
	//
	// Set BEFORE the subprocess-helper branch would matter only if the helper
	// re-entered here; it does not — helpers inherit this value from the parent
	// process environment, which is what we want.
	stateRoot, err := os.MkdirTemp("", "nexus3-service-state-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "service: create temp state root: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_STATE_HOME", stateRoot); err != nil {
		fmt.Fprintf(os.Stderr, "service: set XDG_STATE_HOME: %v\n", err)
		os.Exit(1)
	}

	// os.Exit skips defers, so clean up explicitly around the run.
	code := m.Run()
	_ = os.RemoveAll(stateRoot)
	os.Exit(code)
}
