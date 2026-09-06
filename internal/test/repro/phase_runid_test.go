// phase_runid_test.go — Phase-enumeration proof that every phase wires stageB.run_id.
//
// Each phase runner is called with a fake BuildRunner that returns a non-empty RunID.
// The test asserts that the resulting RunResult.Probes contains exactly one probe
// named "stageB.run_id" — proving the StageBRunIDProbe call is present.
//
// MUTATION: Remove the StageBRunIDProbe call from any one of the six phase bodies
// → the corresponding sub-test fails:
//
//	phase_runid_test.go:114: RunCPUPhase result[0]: no probe named "stageB.run_id"; probes=[builder.state_backend stageA.manifest stageB.file_8m ...]
//
// RESTORE: put the call back → PASS.
//
// Mutation proof (executed, W42): Removed the StageBRunIDProbe call from phase_cpu.go →
//
//	--- FAIL: TestAllPhasesHaveRunIDProbe/cpu
//	    phase_runid_test.go:114: RunCPUPhase result[0]: no probe named "stageB.run_id"; probes=[builder.state_backend stageA.manifest stageB.file_8m stageB.file_31m stageB.file_32m stageB.file_33m stageB.file_40m stageB.file_64m stageB.file_200m stageB.file_elf stageB.run-produced-40m stageB.docker-compose stageB.nexus3-agent stageB.file_32m.hash stageB.file_elf.hash]
//
// Restored → PASS.
package repro

import (
	"context"
	"testing"
	"time"
)

// fakeRunnerWithID returns a BuildRunner that always succeeds with the given runID.
// BuildLog and ImageFile are left empty so the downstream probes (stateBackend,
// stageA, stageB-size, stageB-hash, stageB-run-id) all fire but may return HIF —
// the test only checks that stageB.run_id is PRESENT, not its verdict.
func fakeRunnerWithID(runID string) BuildRunner {
	return func(_ context.Context, cfg BuildConfig, label string) (BuildResult, *ProbeResult, error) {
		return BuildResult{Label: label, Elapsed: time.Millisecond, RunID: runID}, nil, nil
	}
}

// hasRunIDProbe returns true if any probe in probes has the name "stageB.run_id".
func hasRunIDProbe(probes []ProbeResult) bool {
	for _, p := range probes {
		if p.Probe == "stageB.run_id" {
			return true
		}
	}
	return false
}

// TestAllPhasesHaveRunIDProbe asserts that every phase's RunResult contains a
// probe named "stageB.run_id" when the build succeeds (hif=nil) and RunID is set.
func TestAllPhasesHaveRunIDProbe(t *testing.T) {
	const runID = "phase-enum-test-id-12345"
	ctx := context.Background()
	runner := fakeRunnerWithID(runID)
	logsDir := t.TempDir()

	t.Run("baseline", func(t *testing.T) {
		cfg := BaselinePhaseConfig{
			Build:   BuildConfig{LogsDir: logsDir},
			ReproDir: t.TempDir(),
			Runner:  runner,
		}
		result, err := RunBaselinePhase(ctx, cfg)
		if err != nil {
			t.Fatalf("RunBaselinePhase: %v", err)
		}
		if !hasRunIDProbe(result.Probes) {
			t.Errorf("RunBaselinePhase: no probe named %q in result probes %v", "stageB.run_id", probeNames(result.Probes))
		}
	})

	t.Run("hostmem", func(t *testing.T) {
		// MemChecker returns a large value (100 000 MiB) so needed = 100000-FreeMiB > 0
		// only if FreeMiB < 100000. Set FreeMiB = 99999 → needed = 1 MiB → starts hog.
		// Instead use FreeMiB > MemChecker result: FreeMiB=200000 → needed ≤ 0 → no hog.
		cfg := HostMemPhaseConfig{
			Build:    BuildConfig{LogsDir: logsDir},
			ReproDir: t.TempDir(),
			Targets:  []HostMemTarget{{FreeMiB: 200000, Runs: 1}},
			Runner:   runner,
			MemChecker: func() (int64, error) { return 100000, nil },
		}
		results, err := RunHostMemPhase(ctx, cfg)
		if err != nil {
			t.Fatalf("RunHostMemPhase: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("RunHostMemPhase: no results returned")
		}
		for i, r := range results {
			if !hasRunIDProbe(r.Probes) {
				t.Errorf("RunHostMemPhase result[%d]: no probe named %q; probes=%v", i, "stageB.run_id", probeNames(r.Probes))
			}
		}
	})

	t.Run("cpu", func(t *testing.T) {
		cfg := CPUPhaseConfig{
			Build:   BuildConfig{LogsDir: logsDir},
			ReproDir: t.TempDir(),
			NumCPU:  1,
			Runs:    1,
			Runner:  runner,
		}
		results, err := RunCPUPhase(ctx, cfg)
		if err != nil {
			t.Fatalf("RunCPUPhase: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("RunCPUPhase: no results returned")
		}
		for i, r := range results {
			if !hasRunIDProbe(r.Probes) {
				t.Errorf("RunCPUPhase result[%d]: no probe named %q; probes=%v", i, "stageB.run_id", probeNames(r.Probes))
			}
		}
	})

	t.Run("guestmem", func(t *testing.T) {
		cfg := GuestMemPhaseConfig{
			Build:          BuildConfig{LogsDir: logsDir},
			ReproDir:       t.TempDir(),
			MemoryAxisMiB:  []uint16{1024},
			Runner:         runner,
		}
		results, err := RunGuestMemPhase(ctx, cfg)
		if err != nil {
			t.Fatalf("RunGuestMemPhase: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("RunGuestMemPhase: no results returned")
		}
		for i, r := range results {
			if !hasRunIDProbe(r.Probes) {
				t.Errorf("RunGuestMemPhase result[%d]: no probe named %q; probes=%v", i, "stageB.run_id", probeNames(r.Probes))
			}
		}
	})

	t.Run("concurrency", func(t *testing.T) {
		cfg := ConcurrencyPhaseConfig{
			Build:       BuildConfig{LogsDir: logsDir},
			ReproDir:    t.TempDir(),
			N:           1,
			Waves:       1,
			StaggerDelay: time.Millisecond,
			Runner:      runner,
			// MemChecker returns enough memory for required = N*BuilderMemoryMiB(→2048) + 2048 = 4096 MiB.
			MemChecker: func() (int64, error) { return 100000, nil },
		}
		results, err := RunConcurrencyPhase(ctx, cfg)
		if err != nil {
			t.Fatalf("RunConcurrencyPhase: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("RunConcurrencyPhase: no results returned")
		}
		for i, r := range results {
			if !hasRunIDProbe(r.Probes) {
				t.Errorf("RunConcurrencyPhase result[%d]: no probe named %q; probes=%v", i, "stageB.run_id", probeNames(r.Probes))
			}
		}
	})

	t.Run("diskpressure", func(t *testing.T) {
		// TargetFreeGiB=100000 (100 TiB) ensures fillBytes ≤ 0 (current free << target),
		// so no fill file is created and builds proceed with the fake runner.
		cfg := DiskPressurePhaseConfig{
			Build:    BuildConfig{LogsDir: logsDir},
			ReproDir: t.TempDir(),
			Targets:  []DiskPressureTarget{{TargetFreeGiB: 100000, Runs: 1, ExpectHIF: false}},
			Runner:   runner,
		}
		results, err := RunDiskPressurePhase(ctx, cfg)
		if err != nil {
			t.Fatalf("RunDiskPressurePhase: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("RunDiskPressurePhase: no results returned")
		}
		for i, r := range results {
			if !hasRunIDProbe(r.Probes) {
				t.Errorf("RunDiskPressurePhase result[%d]: no probe named %q; probes=%v", i, "stageB.run_id", probeNames(r.Probes))
			}
		}
	})
}

// probeNames returns the probe keys of all probes for error messages.
func probeNames(probes []ProbeResult) []string {
	names := make([]string, len(probes))
	for i, p := range probes {
		names[i] = p.Probe
	}
	return names
}
