// phase_baseline.go — sequential baseline phase.
// Runs ONE end-to-end build and prints a verdict.
// Other phases (concurrency, disk-pressure) live in their own files.
package repro

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// BuildRunner is the RunBuild signature. Set Runner in BaselinePhaseConfig
// to inject a fake build for unit testing — nil uses RunBuild.
type BuildRunner func(ctx context.Context, cfg BuildConfig, label string) (BuildResult, *ProbeResult, error)

// BaselinePhaseConfig configures the sequential baseline phase.
type BaselinePhaseConfig struct {
	// Build is the build runner configuration.
	Build BuildConfig
	// ReproDir is the absolute path to internal/test/repro/ (contains logs/, workspace/).
	ReproDir string
	// ExpectedBackend is the buildkit state backend required for a valid
	// observation. Zero value (Unknown) means "use the default", which is
	// PersistentExt4 (production-parity configuration). A detected backend that
	// differs from the effective expected value causes a HIF probe
	// "builder.state_backend_mismatch" — the harness cannot claim
	// NoTruncationObserved for a configuration that differs from production.
	ExpectedBackend StateBackend
	// Runner is the build runner. Nil uses RunBuild (production default).
	// Set to a fake in tests to avoid real nexus3/VM invocations.
	Runner BuildRunner
}

// RunBaselinePhase executes a single sequential baseline run:
//  1. Snapshots existing logs/
//  2. Runs one nexus3 build with cache-miss gate
//  3. Parses the Stage A manifest from the build log
//  4. Runs Stage B size + hash probes on the packed ext4 artifact
//  5. Prints and returns the RunResult
//
// A HarnessIntegrityFailure anywhere (build gate, manifest, probe) is returned
// as the sole probe in the result — it is never conflated with NoTruncationObserved.
func RunBaselinePhase(ctx context.Context, cfg BaselinePhaseConfig) (RunResult, error) {
	label := fmt.Sprintf("baseline-%s", time.Now().Format("20060102-150405"))

	// 1. Snapshot existing logs before any writes.
	logsDir := cfg.Build.LogsDir
	if logsDir == "" {
		logsDir = filepath.Join(cfg.ReproDir, "logs")
	}
	snap, err := SnapshotLogs(logsDir)
	if err != nil {
		// Snapshot failure is non-fatal but notable.
		fmt.Printf("[repro] WARN: snapshot logs failed: %v\n", err)
	} else if snap != "" {
		fmt.Printf("[repro] logs snapshotted → %s\n", snap)
	}

	// 2. Run build.
	runner := cfg.Runner
	if runner == nil {
		runner = RunBuild
	}
	build, hif, err := runner(ctx, cfg.Build, label)
	if err != nil {
		return RunResult{}, fmt.Errorf("RunBuild: %w", err)
	}

	result := RunResult{
		Label:           label,
		Elapsed:         build.Elapsed,
		HostDiskFreeGiB: build.HostDiskFreeGiB,
		RunID:           build.RunID,
	}

	// Populate provenance early — recorded even if a HIF probe follows.
	nexus3Bin := cfg.Build.nexus3Bin()
	if p, err := exec.LookPath(nexus3Bin); err == nil {
		nexus3Bin = p
	}
	PopulateProvenance(&result, cfg.Build.AgentBin, nexus3Bin)

	if hif != nil {
		// Cache-miss gate fired or build failed — cannot observe anything valid.
		result.Probes = []ProbeResult{*hif}
		printRunResult(result)
		return result, nil
	}

	// 3. Probe the buildkit state backend from the build log.
	// Do this before stage A so the backend is always the first probe in the
	// result — it contextualises every subsequent size/hash probe.
	backend, backendProbe := ParseStateBackend(build.BuildLog)
	result.StateBackend = backend
	result.Probes = append(result.Probes, backendProbe)

	// Enforce expected backend. Zero value (Unknown) means "use the default"
	// which is PersistentExt4 (production parity). A mismatch means the harness
	// was not running the configuration it claimed to test — HIF, never
	// NoTruncationObserved.
	expectedBackend := cfg.ExpectedBackend
	if expectedBackend == Unknown {
		expectedBackend = PersistentExt4
	}
	if backend != Unknown && backend != expectedBackend {
		result.Probes = append(result.Probes, probeHIF("builder.state_backend_mismatch",
			fmt.Sprintf("got %s, expected %s", backend, expectedBackend)))
	}

	// 4. Stage A: parse the rootfs-size-manifest lines from the build log.
	agentBinSize := int64(0)
	if cfg.Build.AgentBin != "" {
		if info, statErr := os.Stat(cfg.Build.AgentBin); statErr == nil {
			agentBinSize = info.Size()
		}
	}
	stageA := ParseManifestStageA(build.BuildLog, agentBinSize, cfg.Build.ElfSize)
	result.Probes = append(result.Probes, stageA...)

	// 5. Stage B: debugfs size probes.
	stageB := StageBSizeProbes(build.ImageFile, agentBinSize, cfg.Build.ElfSize)
	result.Probes = append(result.Probes, stageB...)

	// 5b. Stage B: hash probes (file_32m, file_elf).
	hashProbes := StageBHashProbes(build.ImageFile, cfg.Build.ExpectedHashes)
	result.Probes = append(result.Probes, hashProbes...)

	// 5c. Stage B: run-id probe (out-of-band channel via Containerfile).
	runIDProbe := StageBRunIDProbe(build.ImageFile, build.RunID)
	result.Probes = append(result.Probes, runIDProbe)

	// 6. Print verdict.
	printRunResult(result)
	return result, nil
}

// printRunResult writes the run verdict and per-probe details to stdout.
func printRunResult(r RunResult) {
	v := r.FinalVerdict()
	diskStr := ""
	if r.HostDiskFreeGiB > 0 {
		diskStr = fmt.Sprintf(" disk_free=%.1fGiB", r.HostDiskFreeGiB)
	}
	runIDStr := ""
	if r.RunID != "" {
		runIDStr = fmt.Sprintf(" run_id=%s", r.RunID)
	}
	fmt.Printf("\n=== REPRO VERDICT: %s (label=%s elapsed=%v%s backend=%s%s) ===\n",
		v, r.Label, r.Elapsed.Round(time.Second), diskStr, r.StateBackend, runIDStr)
	if r.Nexus3BinPath != "" {
		sha := r.Nexus3SHA256
		if len(sha) > 16 {
			sha = sha[:16] + "..."
		}
		fmt.Printf("  [nexus3]   %s  sha256=%s\n", r.Nexus3BinPath, sha)
	}
	if r.AgentBinPath != "" {
		sha := r.AgentBinSHA256
		if len(sha) > 16 {
			sha = sha[:16] + "..."
		}
		fmt.Printf("  [agent]    %s  sha256=%s  linkage=%s\n", r.AgentBinPath, sha, r.AgentLinkage)
	}
	for _, p := range r.Probes {
		skipTag := ""
		if p.SkipVerdict {
			skipTag = "*"
		}
		fmt.Printf("  [%-24s%s] %-40s %s\n", p.Verdict, skipTag, p.Probe, p.Detail)
	}
	if any := func() bool {
		for _, p := range r.Probes {
			if p.SkipVerdict {
				return true
			}
		}
		return false
	}(); any {
		fmt.Println("  (* = informational only; does not affect verdict)")
	}
	fmt.Println()
}
