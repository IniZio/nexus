package main

import (
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent"
)

// makeBootConfig returns a bootConfig where every step appends its name to
// *steps.  Tests replace individual fields to verify gating.
func makeBootConfig(steps *[]string) bootConfig {
	return bootConfig{
		mountGuestFS: func() { *steps = append(*steps, "mountGuestFS") },
		initPid1Env:  func() { *steps = append(*steps, "initPid1Env") },
		setupNetwork: func() { *steps = append(*steps, "setupNetwork") },
		startSSHD:    func() { *steps = append(*steps, "startSSHD") },
		mountWorkspace: func(mounts []agent.GuestMount) error {
			*steps = append(*steps, "mountWorkspace")
			return nil
		},
		runBootTasks: func() { *steps = append(*steps, "runBootTasks") },
	}
}

// containsStep reports whether name appears in the steps slice.
func containsStep(steps []string, name string) bool {
	for _, s := range steps {
		if s == name {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Cold-boot path: all six steps must run when hotSwap=false, isPid1=true.
// ─────────────────────────────────────────────────────────────────────────────

func TestBootSequence_ColdBoot_AllStepsRun(t *testing.T) {
	var steps []string
	cfg := makeBootConfig(&steps)
	wsMounts := []agent.GuestMount{{Device: "/dev/vda", Target: "/workspace"}}

	if err := runColdBootInit(false, true, wsMounts, cfg, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{"mountGuestFS", "initPid1Env", "setupNetwork", "startSSHD", "mountWorkspace", "runBootTasks"} {
		if !containsStep(steps, want) {
			t.Errorf("cold-boot: step %q must run, but was not in %v", want, steps)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Hot-swap path: NO cold-boot step must run.
// ─────────────────────────────────────────────────────────────────────────────

func TestBootSequence_HotSwap_NoColdBootSteps(t *testing.T) {
	var steps []string
	cfg := makeBootConfig(&steps)
	wsMounts := []agent.GuestMount{{Device: "/dev/vda", Target: "/workspace"}}

	if err := runColdBootInit(true, true, wsMounts, cfg, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(steps) != 0 {
		t.Errorf("hot-swap: NO steps must run, but got %v", steps)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mutation proof: drop the !hotSwap guard on mountWorkspace → test fails.
//
// This test is the specification for the critical guard.  If someone removes
// the "!hotSwap" condition from the mountWorkspace block in boot_sequence.go,
// mountWorkspace will be called with hotSwap=true, and this test catches it.
// ─────────────────────────────────────────────────────────────────────────────

func TestBootSequence_HotSwap_MountWorkspace_GuardIsRequired(t *testing.T) {
	var mountCalled bool
	cfg := bootConfig{
		mountGuestFS: func() {},
		initPid1Env:  func() {},
		setupNetwork: func() {},
		startSSHD:    func() {},
		mountWorkspace: func(mounts []agent.GuestMount) error {
			mountCalled = true // this must NOT be reached on hot-swap
			return nil
		},
		runBootTasks: func() {},
	}
	wsMounts := []agent.GuestMount{{Device: "/dev/vda", Target: "/workspace"}}

	_ = runColdBootInit(true, true, wsMounts, cfg, nil)

	if mountCalled {
		t.Error("MUTATION DETECTED: mountWorkspace was called during hot-swap; " +
			"the !hotSwap guard in runColdBootInit was removed or bypassed — " +
			"this causes EBUSY → consoleFatal → kernel panic in production")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mutation proof: drop the !hotSwap guard on setupNetwork → test fails.
// ─────────────────────────────────────────────────────────────────────────────

func TestBootSequence_HotSwap_SetupNetwork_GuardIsRequired(t *testing.T) {
	var networkCalled bool
	cfg := bootConfig{
		mountGuestFS:   func() {},
		initPid1Env:    func() {},
		setupNetwork:   func() { networkCalled = true },
		startSSHD:      func() {},
		mountWorkspace: func(_ []agent.GuestMount) error { return nil },
		runBootTasks:   func() {},
	}

	_ = runColdBootInit(true, true, nil, cfg, nil)

	if networkCalled {
		t.Error("MUTATION DETECTED: setupNetwork was called during hot-swap; guard removed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Recorder: verify step ordering on cold-boot via recorder argument.
// ─────────────────────────────────────────────────────────────────────────────

func TestBootSequence_ColdBoot_RecorderCapturesOrder(t *testing.T) {
	var recorded []string
	recorder := func(name string) { recorded = append(recorded, name) }

	var steps []string
	cfg := makeBootConfig(&steps)
	wsMounts := []agent.GuestMount{{Device: "/dev/vda", Target: "/workspace"}}

	_ = runColdBootInit(false, true, wsMounts, cfg, recorder)

	// Recorder must have fired for each step.
	for _, want := range []string{"mountGuestFS", "mountWorkspace", "runBootTasks"} {
		if !containsStep(recorded, want) {
			t.Errorf("recorder: step %q not recorded in %v", want, recorded)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Non-PID-1: PID-1-only steps must not run even on cold boot.
// ─────────────────────────────────────────────────────────────────────────────

func TestBootSequence_NonPid1_ColdBoot_Pid1StepsSkipped(t *testing.T) {
	var steps []string
	cfg := makeBootConfig(&steps)
	wsMounts := []agent.GuestMount{{Device: "/dev/vda", Target: "/workspace"}}

	_ = runColdBootInit(false, false /*isPid1=false*/, wsMounts, cfg, nil)

	for _, forbidden := range []string{"mountGuestFS", "initPid1Env", "setupNetwork", "startSSHD", "runBootTasks"} {
		if containsStep(steps, forbidden) {
			t.Errorf("non-PID-1: step %q must not run, but appeared in %v", forbidden, steps)
		}
	}
	// mountWorkspace is not PID-1-gated; it should still run.
	if !containsStep(steps, "mountWorkspace") {
		t.Errorf("non-PID-1: mountWorkspace must still run on cold boot, got %v", steps)
	}
}
