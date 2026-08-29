// phase_diskpressure.go — disk-pressure sweep phase.
// Runs builds with controlled host disk pressure via fill files.
// Fill files are always removed on exit (deferred); never more than 20 GiB allocated.
package repro

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// DiskPressureTarget is one axis of the disk-pressure sweep.
type DiskPressureTarget struct {
	// TargetFreeGiB is the desired available space on / during the build (GiB).
	TargetFreeGiB float64
	// Runs is the number of builds at this axis value. Default: 2.
	Runs int
	// ExpectHIF: if true, the phase records a HIF as the expected outcome for this axis
	// (the RunBuild precondition gate will fire — this is intentional).
	ExpectHIF bool
}

// DiskPressurePhaseConfig configures the disk-pressure phase.
type DiskPressurePhaseConfig struct {
	Build           BuildConfig
	ReproDir        string
	ExpectedBackend StateBackend
	// Targets defines the pressure axes. If nil, defaults are used.
	Targets []DiskPressureTarget
	// FillDir is where fill files are written (default: os.TempDir()).
	FillDir string
	// Runner is the build runner. Nil uses RunBuild (production default).
	// Set to a fake in tests to avoid real nexus3/VM invocations.
	Runner BuildRunner
}

var defaultDiskPressureTargets = []DiskPressureTarget{
	{TargetFreeGiB: 24, Runs: 2, ExpectHIF: false},
	{TargetFreeGiB: 21, Runs: 2, ExpectHIF: false},
	{TargetFreeGiB: 18, Runs: 2, ExpectHIF: true}, // below 20 GiB precondition
}

// diskFreeGiB returns available bytes on the partition containing path, in GiB.
// Uses Bavail (unprivileged-available blocks) for a conservative measure.
func diskFreeGiB(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return float64(st.Bavail) * float64(st.Bsize) / (1 << 30), nil
}

// RunDiskPressurePhase runs builds with controlled host disk pressure.
// Returns one RunResult per completed build run (including HIFs).
func RunDiskPressurePhase(ctx context.Context, cfg DiskPressurePhaseConfig) ([]RunResult, error) {
	targets := cfg.Targets
	if targets == nil {
		targets = defaultDiskPressureTargets
	}

	fillDir := cfg.FillDir
	if fillDir == "" {
		fillDir = os.TempDir()
	}

	expectedBackend := cfg.ExpectedBackend
	if expectedBackend == Unknown {
		expectedBackend = PersistentExt4
	}

	runner := cfg.Runner
	if runner == nil {
		runner = RunBuild
	}

	logsDir := cfg.Build.LogsDir
	if logsDir == "" {
		logsDir = filepath.Join(cfg.ReproDir, "logs")
	}

	var allResults []RunResult

	for _, target := range targets {
		runs := target.Runs
		if runs <= 0 {
			runs = 2
		}

		fmt.Printf("\n[diskpressure] === axis TargetFreeGiB=%.1f (expectHIF=%v) ===\n",
			target.TargetFreeGiB, target.ExpectHIF)

		// Measure current free space on /.
		currentFreeGiB, err := diskFreeGiB("/")
		if err != nil {
			return allResults, fmt.Errorf("diskFreeGiB(/): %w", err)
		}
		fmt.Printf("[diskpressure] current free=%.1f GiB, target=%.1f GiB\n",
			currentFreeGiB, target.TargetFreeGiB)

		// Compute fill size needed.
		fillGiB := currentFreeGiB - target.TargetFreeGiB
		fillBytes := int64(fillGiB * float64(1<<30))

		fillPath := filepath.Join(fillDir,
			fmt.Sprintf("repro-fill-%d.tmp", os.Getpid()))

		fillCreated := false
		cleanFill := func() {
			if fillCreated {
				if rmErr := os.Remove(fillPath); rmErr != nil && !os.IsNotExist(rmErr) {
					fmt.Printf("[diskpressure] WARN: remove fill file: %v\n", rmErr)
				} else if fillCreated {
					fmt.Printf("[diskpressure] fill file removed: %s\n", fillPath)
				}
				fillCreated = false
			}
		}
		defer cleanFill()

		if fillBytes <= 0 {
			fmt.Println("[diskpressure] already at or below target, skipping fill")
		} else {
			// Safety headroom check: need at least 1 GiB above target.
			if currentFreeGiB <= target.TargetFreeGiB+1.0 {
				fmt.Printf("[diskpressure] WARN: insufficient headroom (free=%.1f GiB, need >%.1f GiB), skipping fill for this axis\n",
					currentFreeGiB, target.TargetFreeGiB+1.0)
				fillBytes = 0
			} else {
				// Safety cap: never fill more than 20 GiB.
				const maxFillBytes = 20 * (1 << 30)
				if fillBytes > maxFillBytes {
					fmt.Printf("[diskpressure] WARN: capping fill at 20 GiB (requested %.1f GiB)\n",
						float64(fillBytes)/(1<<30))
					fillBytes = maxFillBytes
				}

				// Try fallocate first (fast, no real I/O).
				fallocCmd := exec.Command("fallocate", "-l",
					fmt.Sprintf("%d", fillBytes), fillPath)
				if fallocErr := fallocCmd.Run(); fallocErr != nil {
					fmt.Printf("[diskpressure] fallocate failed (%v), falling back to zero-fill\n", fallocErr)
					// Fallback: write zeros in 64 MiB chunks.
					f, createErr := os.Create(fillPath)
					if createErr != nil {
						return allResults, fmt.Errorf("create fill file: %w", createErr)
					}
					buf := make([]byte, 64<<20) // 64 MiB chunks
					remaining := fillBytes
					var writeErr error
					for remaining > 0 {
						n := int64(len(buf))
						if n > remaining {
							n = remaining
						}
						if _, writeErr = f.Write(buf[:n]); writeErr != nil {
							break
						}
						remaining -= n
					}
					f.Close()
					if writeErr != nil {
						os.Remove(fillPath)
						return allResults, fmt.Errorf("zero-fill: %w", writeErr)
					}
				}
				fillCreated = true

				// Measure actual free after fill.
				actualFreeGiB, measErr := diskFreeGiB("/")
				if measErr != nil {
					fmt.Printf("[diskpressure] WARN: post-fill diskFreeGiB: %v\n", measErr)
				} else {
					fmt.Printf("[diskpressure] fill=%.1fGiB → freeGiB=%.1f (target %.1f)\n",
						float64(fillBytes)/(1<<30), actualFreeGiB, target.TargetFreeGiB)
				}
			}
		}

		// Run builds for this axis.
		for i := 0; i < runs; i++ {
			cfg.Build.SandboxName = fmt.Sprintf("diskpressure-%d-%d",
				int(target.TargetFreeGiB), i)
			label := fmt.Sprintf("diskpressure-free%.0fg-run%d-%s",
				target.TargetFreeGiB, i, time.Now().Format("20060102-150405"))

			preFreeGiB, _ := diskFreeGiB("/")
			fmt.Printf("\n[diskpressure] run %d/%d label=%s free=%.1f GiB\n",
				i+1, runs, label, preFreeGiB)

			// Snapshot logs before the run.
			snap, snapErr := SnapshotLogs(logsDir)
			if snapErr != nil {
				fmt.Printf("[repro] WARN: snapshot logs failed: %v\n", snapErr)
			} else if snap != "" {
				fmt.Printf("[repro] logs snapshotted → %s\n", snap)
			}

			// Run build.
			build, hif, buildErr := runner(ctx, cfg.Build, label)
			if buildErr != nil {
				return allResults, fmt.Errorf("RunBuild: %w", buildErr)
			}

			result := RunResult{
				Label:           label,
				Elapsed:         build.Elapsed,
				HostDiskFreeGiB: build.HostDiskFreeGiB,
				RunID:           build.RunID,
			}

			if target.ExpectHIF && hif != nil {
				// Expected HIF: precondition gate fired as intended for this axis.
				fmt.Printf("[diskpressure] expected HIF: %s\n", hif.Detail)
				result.Probes = []ProbeResult{*hif}
				printRunResult(result)
				allResults = append(allResults, result)
				continue
			}

			if hif != nil {
				// Unexpected HIF on a non-ExpectHIF axis.
				result.Probes = []ProbeResult{*hif}
				printRunResult(result)
				allResults = append(allResults, result)
				continue
			}

			// Build succeeded — follow baseline pattern.
			backend, backendProbe := ParseStateBackend(build.BuildLog)
			result.StateBackend = backend
			result.Probes = append(result.Probes, backendProbe)

			if backend != Unknown && backend != expectedBackend {
				result.Probes = append(result.Probes, probeHIF("builder.state_backend_mismatch",
					fmt.Sprintf("got %s, expected %s", backend, expectedBackend)))
			}

			agentBinSize := int64(0)
			if cfg.Build.AgentBin != "" {
				if info, statErr := os.Stat(cfg.Build.AgentBin); statErr == nil {
					agentBinSize = info.Size()
				}
			}

			stageA := ParseManifestStageA(build.BuildLog, agentBinSize, cfg.Build.ElfSize)
			result.Probes = append(result.Probes, stageA...)

			stageB := StageBSizeProbes(build.ImageFile, agentBinSize, cfg.Build.ElfSize)
			result.Probes = append(result.Probes, stageB...)

			hashProbes := StageBHashProbes(build.ImageFile, cfg.Build.ExpectedHashes)
			result.Probes = append(result.Probes, hashProbes...)

			// Stage B: run-id probe (out-of-band channel via Containerfile).
			runIDProbe := StageBRunIDProbe(build.ImageFile, build.RunID)
			result.Probes = append(result.Probes, runIDProbe)

			printRunResult(result)
			allResults = append(allResults, result)
		}

		// Clean up fill file for this axis before the next.
		cleanFill()

		postFreeGiB, err := diskFreeGiB("/")
		if err != nil {
			fmt.Printf("[diskpressure] WARN: post-cleanup diskFreeGiB: %v\n", err)
		} else {
			fmt.Printf("[diskpressure] post-cleanup free=%.1f GiB\n", postFreeGiB)
		}
	}

	return allResults, nil
}
