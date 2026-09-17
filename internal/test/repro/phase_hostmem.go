// phase_hostmem.go — host-memory-pressure sweep phase for the repro harness.
// Allocates host RAM via a memhog subprocess to hold MemAvailable at a target
// level during cache-miss builds; observes truncation at each pressure axis.
package repro

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HostMemTarget is one axis of the host-memory-pressure sweep.
type HostMemTarget struct {
	FreeMiB int // target MemAvailable (MiB) to hold during the build
	Runs    int // number of cache-MISS builds to run at this axis value
}

// HostMemPhaseConfig configures the host-memory-pressure phase.
type HostMemPhaseConfig struct {
	Build           BuildConfig
	ReproDir        string
	ExpectedBackend StateBackend
	// Targets defines the pressure sweep. If nil, defaults are used.
	Targets []HostMemTarget
	// Runner is the build runner. Nil uses RunBuild (production default).
	// Set to a fake in tests to avoid real nexus/VM invocations.
	Runner BuildRunner
	// MemChecker returns the current MemAvailable in MiB. Nil uses readMemAvailableMiB.
	// Set to a fake in tests to bypass the pre-target memory reading (avoids real /proc/meminfo
	// dependency and hog-effectiveness check).
	MemChecker func() (int64, error)
}

// defaultHostMemTargets are the default axis values. Two targets, two runs each.
var defaultHostMemTargets = []HostMemTarget{
	{FreeMiB: 3072, Runs: 2},
	{FreeMiB: 2048, Runs: 2},
}

// hostMemFloorMiB is the minimum MemAvailable the hog is permitted to target.
// Driving below this risks OOM-killing the host kernel or sandbox processes.
const hostMemFloorMiB = 1536

// memhogMainGo is the absolute path used for `go run` fallback, derived from
// this source file's location so it works from any checkout.
var memhogMainGo = func() string {
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "cmd", "memhog", "main.go")
}()

// RunHostMemPhase runs the host-memory-pressure phase.
// Returns one RunResult per completed build run (including HIFs).
func RunHostMemPhase(ctx context.Context, cfg HostMemPhaseConfig) ([]RunResult, error) {
	targets := cfg.Targets
	if targets == nil {
		targets = defaultHostMemTargets
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
	memChecker := cfg.MemChecker
	if memChecker == nil {
		memChecker = readMemAvailableMiB
	}

	var results []RunResult

	for _, target := range targets {
		fmt.Printf("[hostmem] === axis FreeMiB=%d ===\n", target.FreeMiB)

		// Hard floor: refuse to drive MemAvailable below 1536 MiB.
		if target.FreeMiB < hostMemFloorMiB {
			fmt.Printf("[hostmem] WARN: target.FreeMiB=%d < floor %d MiB — skipping\n",
				target.FreeMiB, hostMemFloorMiB)
			results = append(results, RunResult{
				Label: fmt.Sprintf("hostmem-free%d-skip", target.FreeMiB),
				Probes: []ProbeResult{probeHIF("hostmem.floor_violated",
					fmt.Sprintf("target %d MiB < floor %d MiB", target.FreeMiB, hostMemFloorMiB))},
			})
			continue
		}

		// Read MemAvailable before starting the hog.
		currentAvailMiB, err := memChecker()
		if err != nil {
			fmt.Printf("[hostmem] WARN: cannot read MemAvailable: %v\n", err)
			results = append(results, RunResult{
				Label: fmt.Sprintf("hostmem-free%d-hif", target.FreeMiB),
				Probes: []ProbeResult{probeHIF("hostmem.meminfo_read",
					fmt.Sprintf("before hog: %v", err))},
			})
			continue
		}
		fmt.Printf("[hostmem] MemAvailable before hog: %d MiB (target free: %d MiB)\n",
			currentAvailMiB, target.FreeMiB)

		needed := currentAvailMiB - int64(target.FreeMiB)

		var hogCmd *exec.Cmd
		hogStarted := false

		if needed > 0 {
			hogCmd = startMemhog(ctx, needed)
			if hogCmd == nil {
				results = append(results, RunResult{
					Label: fmt.Sprintf("hostmem-free%d-hif", target.FreeMiB),
					Probes: []ProbeResult{probeHIF("hostmem.hog_failed",
						"failed to start memhog process")},
				})
				continue
			}
			hogStarted = true

			// Wait 3 s for allocation to settle.
			time.Sleep(3 * time.Second)

			// Read MemAvailable "during" snapshot; verify drop > 10%.
			duringMiB, duringErr := readMemAvailableMiB()
			if duringErr != nil {
				fmt.Printf("[hostmem] WARN: cannot read MemAvailable during hog: %v\n", duringErr)
			} else {
				fmt.Printf("[hostmem] MemAvailable during hog: %d MiB\n", duringMiB)
				threshold := currentAvailMiB - currentAvailMiB/10
				if duringMiB >= threshold {
					fmt.Printf("[hostmem] WARN: hog ineffective (before=%d during=%d threshold=%d)\n",
						currentAvailMiB, duringMiB, threshold)
					stopMemhog(hogCmd)
					results = append(results, RunResult{
						Label: fmt.Sprintf("hostmem-free%d-hif", target.FreeMiB),
						Probes: []ProbeResult{probeHIF("hostmem.hog_failed",
							fmt.Sprintf("avail before=%dMiB during=%dMiB; drop < 10%%",
								currentAvailMiB, duringMiB))},
					})
					continue
				}
			}
		} else {
			fmt.Printf("[hostmem] needed=%d MiB ≤ 0; skipping hog (already at or below target)\n", needed)
		}

		// Run builds at this axis.
		for i := 0; i < target.Runs; i++ {
			// Print free -m for observability.
			if out, ferr := exec.Command("free", "-m").CombinedOutput(); ferr == nil {
				fmt.Printf("[hostmem] free -m:\n%s\n", string(out))
			}

			beforeMiB, _ := readMemAvailableMiB()
			fmt.Printf("[hostmem] MemAvailable before build run %d: %d MiB\n", i, beforeMiB)

			label := fmt.Sprintf("hostmem-free%d-run%d-%s",
				target.FreeMiB, i, time.Now().Format("20060102-150405"))

			buildCfg := cfg.Build
			buildCfg.SandboxName = fmt.Sprintf("hostmem-%d-%d", target.FreeMiB, i)

			// Snapshot logs before each run.
			if snap, snapErr := SnapshotLogs(logsDir); snapErr != nil {
				fmt.Printf("[repro] WARN: snapshot logs failed: %v\n", snapErr)
			} else if snap != "" {
				fmt.Printf("[repro] logs snapshotted → %s\n", snap)
			}

			build, hif, buildErr := runner(ctx, buildCfg, label)
			if buildErr != nil {
				if hogStarted && hogCmd != nil {
					stopMemhog(hogCmd)
				}
				return results, fmt.Errorf("RunBuild: %w", buildErr)
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

			// Parse state backend.
			backend, backendProbe := ParseStateBackend(build.BuildLog)
			result.StateBackend = backend
			result.Probes = append(result.Probes, backendProbe)

			if backend != Unknown && backend != expectedBackend {
				result.Probes = append(result.Probes, probeHIF("builder.state_backend_mismatch",
					fmt.Sprintf("got %s, expected %s", backend, expectedBackend)))
			}

			// Stage A: parse manifest from build log.
			agentBinSize := int64(0)
			if buildCfg.AgentBin != "" {
				if info, statErr := os.Stat(buildCfg.AgentBin); statErr == nil {
					agentBinSize = info.Size()
				}
			}
			stageA := ParseManifestStageA(build.BuildLog, agentBinSize, buildCfg.ElfSize)
			result.Probes = append(result.Probes, stageA...)

			// Stage B: debugfs size probes.
			stageB := StageBSizeProbes(build.ImageFile, agentBinSize, buildCfg.ElfSize)
			result.Probes = append(result.Probes, stageB...)

			// Stage B: hash probes.
			hashProbes := StageBHashProbes(build.ImageFile, buildCfg.ExpectedHashes)
			result.Probes = append(result.Probes, hashProbes...)

			// Stage B: run-id probe (out-of-band channel via Containerfile).
			runIDProbe := StageBRunIDProbe(build.ImageFile, build.RunID)
			result.Probes = append(result.Probes, runIDProbe)

			printRunResult(result)
			results = append(results, result)
		}

		// Stop hog after all runs at this axis.
		if hogStarted && hogCmd != nil {
			stopMemhog(hogCmd)
		}

		// Read MemAvailable after releasing hog.
		afterMiB, afterErr := readMemAvailableMiB()
		if afterErr != nil {
			fmt.Printf("[hostmem] WARN: cannot read MemAvailable after hog release: %v\n", afterErr)
		} else {
			fmt.Printf("[hostmem] MemAvailable after hog release: %d MiB\n", afterMiB)
		}
	}

	return results, nil
}

// startMemhog starts a background process to pin neededMiB of RAM.
// Prefers stress-ng if available; falls back to `go run cmd/memhog/main.go`.
// Returns nil if no process could be started.
func startMemhog(ctx context.Context, neededMiB int64) *exec.Cmd {
	// Try stress-ng first.
	if _, err := exec.LookPath("stress-ng"); err == nil {
		vmBytes := fmt.Sprintf("%dM", neededMiB)
		cmd := exec.CommandContext(ctx, "stress-ng",
			"--vm", "1", "--vm-bytes", vmBytes, "--vm-keep", "-q")
		if startErr := cmd.Start(); startErr == nil {
			return cmd
		}
	}

	// Fall back to go run memhog. The memhog binary takes --target-free-mib,
	// so compute target-free from current MemAvailable.
	curMiB, err := readMemAvailableMiB()
	if err != nil {
		fmt.Printf("[hostmem] WARN: cannot read MemAvailable for memhog target: %v\n", err)
		return nil
	}
	targetFree := curMiB - neededMiB
	if targetFree < hostMemFloorMiB {
		targetFree = hostMemFloorMiB
	}
	cmd := exec.CommandContext(ctx, "go", "run", memhogMainGo,
		fmt.Sprintf("--target-free-mib=%d", targetFree))
	if startErr := cmd.Start(); startErr != nil {
		fmt.Printf("[hostmem] WARN: go run memhog failed: %v\n", startErr)
		return nil
	}
	return cmd
}

// stopMemhog gracefully terminates a hog process: SIGTERM, then SIGKILL after 2 s.
func stopMemhog(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

// readMemAvailableMiB returns the MemAvailable field from /proc/meminfo in MiB.
func readMemAvailableMiB() (int64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, parseErr := strconv.ParseInt(fields[1], 10, 64)
				return kb / 1024, parseErr
			}
		}
	}
	return 0, fmt.Errorf("MemAvailable not found in /proc/meminfo")
}
