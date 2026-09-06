// phase_guestmem.go — guest-memory-pressure phase.
// Sweeps BuilderMemoryMiB values to probe truncation behaviour under constrained
// guest RAM. The memory governor may resize memory back up during the build;
// callers should inspect the build log for resize lines.
package repro

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GuestMemPhaseConfig configures the guest-memory-pressure phase.
type GuestMemPhaseConfig struct {
	Build           BuildConfig
	ReproDir        string
	ExpectedBackend StateBackend
	// MemoryAxisMiB lists the BuilderMemoryMiB values to sweep. If nil, defaults are used.
	MemoryAxisMiB []uint16
	// Runner is the build runner. Nil uses RunBuild (production default).
	// Set to a fake in tests to avoid real nexus3/VM invocations.
	Runner BuildRunner
}

var defaultGuestMemAxis = []uint16{1024, 768}

// RunGuestMemPhase runs builds with reduced BuilderMemoryMiB values.
// Returns one RunResult per completed build run (including HIFs).
func RunGuestMemPhase(ctx context.Context, cfg GuestMemPhaseConfig) ([]RunResult, error) {
	axis := cfg.MemoryAxisMiB
	if len(axis) == 0 {
		axis = defaultGuestMemAxis
	}

	logsDir := cfg.Build.LogsDir
	if logsDir == "" {
		logsDir = filepath.Join(cfg.ReproDir, "logs")
	}

	expectedBackend := cfg.ExpectedBackend
	if expectedBackend == Unknown {
		expectedBackend = PersistentExt4
	}

	runner := cfg.Runner
	if runner == nil {
		runner = RunBuild
	}

	var results []RunResult

	for _, memMiB := range axis {
		fmt.Printf("[guestmem] === axis BuilderMemoryMiB=%d ===\n", memMiB)

		for i := 0; i < 2; i++ {
			buildCfg := cfg.Build
			buildCfg.BuilderMemoryMiB = memMiB
			buildCfg.SandboxName = fmt.Sprintf("guestmem-%d-%d", memMiB, i)
			if buildCfg.LogsDir == "" {
				buildCfg.LogsDir = logsDir
			}

			label := fmt.Sprintf("guestmem-%dmib-run%d-%s", memMiB, i, time.Now().Format("20060102-150405"))

			snap, snapErr := SnapshotLogs(logsDir)
			if snapErr != nil {
				fmt.Printf("[repro] WARN: snapshot logs failed: %v\n", snapErr)
			} else if snap != "" {
				fmt.Printf("[repro] logs snapshotted → %s\n", snap)
			}

			build, hif, err := runner(ctx, buildCfg, label)
			if err != nil {
				return results, fmt.Errorf("RunBuild (guestmem %dMiB run %d): %w", memMiB, i, err)
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

			fmt.Printf("[guestmem] NOTE: check build log for resize lines (memory governor may have expanded guest RAM)\n")

			printRunResult(result)
			results = append(results, result)
		}
	}

	return results, nil
}
