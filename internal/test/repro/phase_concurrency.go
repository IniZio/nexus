// phase_concurrency.go — concurrent-build phase for the repro harness.
// Launches N builds simultaneously per wave to stress the shared buildkit
// state backend and the per-sandbox VirtIO-blk cache disk under contention.
package repro

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// ConcurrencyPhaseConfig configures the concurrent-build phase.
type ConcurrencyPhaseConfig struct {
	Build           BuildConfig
	ReproDir        string
	ExpectedBackend StateBackend
	// N is the number of builds to launch concurrently per wave. Defaults to 3.
	N int
	// Waves is the number of concurrent waves to execute. Defaults to 2.
	Waves int
	// StaggerDelay is the pause between launching successive builds within a wave.
	// Defaults to 2 seconds. Helps avoid the "supervisor exited before writing
	// supervisor.pid" race observed when 3 VMs start simultaneously.
	StaggerDelay time.Duration
	// Runner is the build runner. Nil uses RunBuild (production default).
	// Set to a fake in tests to avoid real nexus3/VM invocations.
	Runner BuildRunner
	// MemChecker returns the current MemAvailable in MiB. Nil uses readMemAvailableMiB.
	// Set to a fake in tests to bypass the pre-wave memory gate.
	MemChecker func() (int64, error)
}

// concSlotResult holds the outcome of one slot within a concurrent wave.
type concSlotResult struct {
	slot  int
	label string
	build BuildResult
	hif   *ProbeResult
	err   error
}

// RunConcurrencyPhase runs the concurrent-build phase.
// Returns one RunResult per (wave × slot) pair, in order:
// wave0-slot0, wave0-slot1, wave0-slot2, wave1-slot0, ...
func RunConcurrencyPhase(ctx context.Context, cfg ConcurrencyPhaseConfig) ([]RunResult, error) {
	n := cfg.N
	if n <= 0 {
		n = 3
	}
	waves := cfg.Waves
	if waves <= 0 {
		waves = 2
	}
	stagger := cfg.StaggerDelay
	if stagger <= 0 {
		stagger = 2 * time.Second
	}

	logsDir := cfg.Build.LogsDir
	if logsDir == "" {
		logsDir = filepath.Join(cfg.ReproDir, "logs")
	}

	memMiB := int64(cfg.Build.BuilderMemoryMiB)
	if memMiB <= 0 {
		memMiB = 2048
	}
	required := int64(n)*memMiB + 2048

	runner := cfg.Runner
	if runner == nil {
		runner = RunBuild
	}
	memChecker := cfg.MemChecker
	if memChecker == nil {
		memChecker = readMemAvailableMiB
	}

	var results []RunResult

	for wave := 0; wave < waves; wave++ {
		fmt.Printf("[concurrency] === wave %d/%d: %d concurrent builds, stagger=%s ===\n",
			wave, waves, n, stagger.Round(time.Second))

		// Memory check with retry loop before each wave.
		skipWave := false
		var availMiB int64
		memDeadline := time.Now().Add(15 * time.Minute)
		for {
			var memErr error
			availMiB, memErr = memChecker()
			if memErr != nil {
				fmt.Printf("[concurrency] WARN: cannot read MemAvailable: %v\n", memErr)
				break // proceed with uncertainty
			}
			fmt.Printf("[concurrency] MemAvailable=%d MiB required=%d MiB\n", availMiB, required)
			if availMiB >= required {
				break
			}
			if time.Now().After(memDeadline) {
				// Emit HIFs for all slots and skip this wave.
				for i := 0; i < n; i++ {
					label := fmt.Sprintf("conc-wave%d-slot%d-memblock", wave, i)
					results = append(results, RunResult{
						Label: label,
						Probes: []ProbeResult{probeHIF("concurrency.mem_insufficient",
							fmt.Sprintf("MemAvailable=%d MiB < required %d MiB after 15min wait", availMiB, required))},
					})
				}
				skipWave = true
				break
			}
			fmt.Printf("[concurrency] memory low; retrying in 60s (deadline %s)\n",
				memDeadline.Format("15:04:05"))
			select {
			case <-ctx.Done():
				for i := 0; i < n; i++ {
					label := fmt.Sprintf("conc-wave%d-slot%d-memblock", wave, i)
					results = append(results, RunResult{
						Label: label,
						Probes: []ProbeResult{probeHIF("concurrency.mem_insufficient",
							"context cancelled while waiting for memory")},
					})
				}
				return results, nil
			case <-time.After(60 * time.Second):
			}
		}
		if skipWave {
			continue
		}

		// Snapshot logs once per wave before launching goroutines.
		if snap, snapErr := SnapshotLogs(logsDir); snapErr != nil {
			fmt.Printf("[repro] WARN: snapshot logs failed: %v\n", snapErr)
		} else if snap != "" {
			fmt.Printf("[repro] logs snapshotted → %s\n", snap)
		}

		// Build the allowed-handles set for this wave.
		allowedHandles := make(map[string]struct{}, n)
		for i := 0; i < n; i++ {
			allowedHandles[fmt.Sprintf("repro/conc-%d", i)] = struct{}{}
		}

		resCh := make(chan concSlotResult, n)

		// Launch N goroutines, staggering launches in the main goroutine.
		for i := 0; i < n; i++ {
			if i > 0 {
				time.Sleep(stagger)
			}
			go func(slot int) {
				label := fmt.Sprintf("conc-wave%d-slot%d-%s", wave, slot, time.Now().Format("20060102-150405"))
				buildCfg := cfg.Build
				buildCfg.SandboxName = fmt.Sprintf("conc-%d", slot)
				buildCfg.AllowedSandboxHandles = allowedHandles
				if buildCfg.LogsDir == "" {
					buildCfg.LogsDir = logsDir
				}
				b, hif, err := runner(ctx, buildCfg, label)
				resCh <- concSlotResult{slot: slot, label: label, build: b, hif: hif, err: err}
			}(i)
		}

		// Collect N results from the channel.
		slotResults := make([]concSlotResult, n)
		for i := 0; i < n; i++ {
			slotResults[i] = <-resCh
		}

		// Sort by slot for deterministic output order.
		sort.Slice(slotResults, func(i, j int) bool {
			return slotResults[i].slot < slotResults[j].slot
		})

		// Assemble RunResults for this wave.
		expectedBackend := cfg.ExpectedBackend
		if expectedBackend == Unknown {
			expectedBackend = PersistentExt4
		}

		waveResults := make([]RunResult, 0, n)

		for _, slotRes := range slotResults {
			result := RunResult{
				Label:           slotRes.label,
				Elapsed:         slotRes.build.Elapsed,
				HostDiskFreeGiB: slotRes.build.HostDiskFreeGiB,
				RunID:           slotRes.build.RunID,
			}

			if slotRes.err != nil {
				result.Probes = []ProbeResult{probeHIF("concurrency.build_error",
					fmt.Sprintf("slot %d: RunBuild error: %v", slotRes.slot, slotRes.err))}
				printRunResult(result)
				waveResults = append(waveResults, result)
				continue
			}

			if slotRes.hif != nil {
				result.Probes = []ProbeResult{*slotRes.hif}
				printRunResult(result)
				waveResults = append(waveResults, result)
				continue
			}

			// State backend.
			backend, backendProbe := ParseStateBackend(slotRes.build.BuildLog)
			result.StateBackend = backend
			result.Probes = append(result.Probes, backendProbe)

			if backend != Unknown && backend != expectedBackend {
				result.Probes = append(result.Probes, probeHIF("builder.state_backend_mismatch",
					fmt.Sprintf("got %s, expected %s", backend, expectedBackend)))
			}

			// Stage A.
			agentBinSize := int64(0)
			if slotRes.build.BuildLog != "" {
				if info, statErr := os.Stat(cfg.Build.AgentBin); statErr == nil {
					agentBinSize = info.Size()
				}
			}
			stageA := ParseManifestStageA(slotRes.build.BuildLog, agentBinSize, cfg.Build.ElfSize)
			result.Probes = append(result.Probes, stageA...)

			// Stage B: size probes.
			stageB := StageBSizeProbes(slotRes.build.ImageFile, agentBinSize, cfg.Build.ElfSize)
			result.Probes = append(result.Probes, stageB...)

			// Stage B: hash probes.
			hashProbes := StageBHashProbes(slotRes.build.ImageFile, cfg.Build.ExpectedHashes)
			result.Probes = append(result.Probes, hashProbes...)

			// Stage B: run-id probe (out-of-band channel via Containerfile).
			runIDProbe := StageBRunIDProbe(slotRes.build.ImageFile, slotRes.build.RunID)
			result.Probes = append(result.Probes, runIDProbe)

			printRunResult(result)
			waveResults = append(waveResults, result)
		}

		// Check for truncation evidence before moving to the next wave.
		for _, r := range waveResults {
			if r.FinalVerdict() == TruncationReproduced {
				fmt.Printf("[concurrency] TRUNCATION observed in wave %d — preserving evidence\n", wave)
				preserveTruncationEvidence()
				break
			}
		}

		results = append(results, waveResults...)
		fmt.Printf("[concurrency] wave %d done\n", wave)
	}

	return results, nil
}

// preserveTruncationEvidence copies the buildkit cache disk to a timestamped
// directory in ~/nexus3-truncation-evidence/ for post-mortem analysis.
func preserveTruncationEvidence() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("[concurrency] WARN: cannot determine home dir for evidence: %v\n", err)
		return
	}
	stateDir := os.Getenv("NEXUS3_STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".local", "state", "nexus3")
	}
	src := filepath.Join(stateDir, "caches", "buildkit.ext4")
	if _, err := os.Stat(src); err != nil {
		fmt.Printf("[concurrency] evidence: buildkit.ext4 not found at %s: %v\n", src, err)
		return
	}
	ts := time.Now().Format("20060102-150405")
	dstDir := filepath.Join(home, "nexus3-truncation-evidence", ts+"-cachedisk")
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		fmt.Printf("[concurrency] evidence: mkdir %s: %v\n", dstDir, err)
		return
	}
	dst := filepath.Join(dstDir, "buildkit.ext4")
	// Sparse copy to preserve holes.
	if out, cpErr := exec.Command("cp", "--sparse=always", src, dst).CombinedOutput(); cpErr != nil {
		fmt.Printf("[concurrency] evidence: sparse copy failed: %v\n%s\n", cpErr, string(out))
		return
	}
	// SHA256 of the copy.
	out, shaErr := exec.Command("sha256sum", dst).Output()
	if shaErr != nil {
		fmt.Printf("[concurrency] evidence: sha256sum failed: %v\n", shaErr)
	} else {
		fmt.Printf("[concurrency] TRUNCATION EVIDENCE preserved: %s\nsha256: %s", dstDir, string(out))
	}
}
