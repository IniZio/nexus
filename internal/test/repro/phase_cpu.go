// phase_cpu.go — CPU saturation phase.
// Spins goroutines to saturate host CPU cores during builds to probe truncation
// behaviour under scheduling pressure.
package repro

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// CPUPhaseConfig configures the CPU saturation phase.
type CPUPhaseConfig struct {
	Build           BuildConfig
	ReproDir        string
	ExpectedBackend StateBackend
	// NumCPU is the number of CPUs to saturate. If 0, defaults to runtime.NumCPU().
	NumCPU int
	// Runs is the number of builds per axis. If 0, defaults to 2.
	Runs int
	// Runner is the build runner. Nil uses RunBuild (production default).
	// Set to a fake in tests to avoid real nexus3/VM invocations.
	Runner BuildRunner
}

// RunCPUPhase saturates host CPU cores during the build using Go goroutines.
// Returns one RunResult per completed build run (including HIFs).
func RunCPUPhase(ctx context.Context, cfg CPUPhaseConfig) ([]RunResult, error) {
	numCPU := cfg.NumCPU
	if numCPU <= 0 {
		numCPU = runtime.NumCPU()
	}
	runs := cfg.Runs
	if runs <= 0 {
		runs = 2
	}

	logsDir := cfg.Build.LogsDir
	if logsDir == "" {
		logsDir = filepath.Join(cfg.ReproDir, "logs")
	}

	expectedBackend := cfg.ExpectedBackend
	if expectedBackend == Unknown {
		expectedBackend = PersistentExt4
	}

	fmt.Printf("[cpu] === spinning %d goroutines on %d CPUs ===\n", numCPU, numCPU)

	// Start spinner goroutines — busy loops that yield every 1M iterations so
	// the Go scheduler can still dispatch goroutines on the same thread.
	done := make(chan struct{})
	for g := 0; g < numCPU; g++ {
		go func() {
			var dummy uint64
			for {
				for j := 0; j < 1_000_000; j++ {
					dummy++
				}
				select {
				case <-done:
					return
				default:
					runtime.Gosched()
				}
			}
		}()
	}

	runner := cfg.Runner
	if runner == nil {
		runner = RunBuild
	}

	var results []RunResult

	for i := 0; i < runs; i++ {
		buildCfg := cfg.Build
		buildCfg.SandboxName = fmt.Sprintf("cpu-%d", i)
		if buildCfg.LogsDir == "" {
			buildCfg.LogsDir = logsDir
		}

		label := fmt.Sprintf("cpu-run%d-%s", i, time.Now().Format("20060102-150405"))

		snap, snapErr := SnapshotLogs(logsDir)
		if snapErr != nil {
			fmt.Printf("[repro] WARN: snapshot logs failed: %v\n", snapErr)
		} else if snap != "" {
			fmt.Printf("[repro] logs snapshotted → %s\n", snap)
		}

		fmt.Printf("[cpu] build %d/%d starting (host CPUs=%d spinner goroutines=%d)\n",
			i+1, runs, numCPU, numCPU)

		build, hif, err := runner(ctx, buildCfg, label)
		if err != nil {
			close(done)
			return results, fmt.Errorf("RunBuild (cpu run %d): %w", i, err)
		}

		result := RunResult{
			Label:           label,
			Elapsed:         build.Elapsed,
			HostDiskFreeGiB: build.HostDiskFreeGiB,
			RunID:           build.RunID,
		}

		if hif != nil {
			result.Probes = []ProbeResult{*hif}
			printRunResult(result)
			results = append(results, result)
			continue
		}

		backend, backendProbe := ParseStateBackend(build.BuildLog)
		result.StateBackend = backend
		result.Probes = append(result.Probes, backendProbe)

		if backend != Unknown && backend != expectedBackend {
			result.Probes = append(result.Probes, probeHIF("builder.state_backend_mismatch",
				fmt.Sprintf("got %s, expected %s", backend, expectedBackend)))
		}

		agentBinSize := int64(0)
		if buildCfg.AgentBin != "" {
			if info, statErr := os.Stat(buildCfg.AgentBin); statErr == nil {
				agentBinSize = info.Size()
			}
		}

		stageA := ParseManifestStageA(build.BuildLog, agentBinSize, buildCfg.ElfSize)
		result.Probes = append(result.Probes, stageA...)

		stageB := StageBSizeProbes(build.ImageFile, agentBinSize, buildCfg.ElfSize)
		result.Probes = append(result.Probes, stageB...)

		hashProbes := StageBHashProbes(build.ImageFile, buildCfg.ExpectedHashes)
		result.Probes = append(result.Probes, hashProbes...)

		// Stage B: run-id probe (out-of-band channel via Containerfile).
		runIDProbe := StageBRunIDProbe(build.ImageFile, build.RunID)
		result.Probes = append(result.Probes, runIDProbe)

		printRunResult(result)
		results = append(results, result)
	}

	close(done)
	return results, nil
}
