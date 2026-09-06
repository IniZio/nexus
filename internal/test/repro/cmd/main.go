// Command repro runs a phase of the buildkit 32 MiB truncation reproduction harness.
//
// Usage:
//
//	repro [--repro-dir <path>] [--phase <phase>]
//
// Phases:
//
//	baseline     (default) one sequential build; verifies ext4 export
//	hostmem      host memory pressure: hog RAM to target MemAvailable (3 GiB, 2 GiB axes)
//	guestmem     guest memory: reduce BuilderMemoryMiB (1024, 768 axes)
//	diskpressure host disk pressure: fill / toward 24, 21, 18 GiB free
//	cpu          saturate host CPU cores during the build
//	concurrency  N=3 concurrent builds per wave; 2 waves; stagger 2s
//
// Environment variables:
//
//	NEXUS3            path to nexus3 binary (default: nexus3 on PATH)
//	NEXUS3_STATE_DIR  nexus3 state directory (default: ~/.local/state/nexus3)
//
// Exit codes:
//
//	0  TruncationReproduced   — the bug was observed
//	2  HarnessIntegrityFailure — harness could not validly observe
//	3  NoTruncationObserved   — harness ran validly, saw nothing
//	1  infrastructure error
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	repro "github.com/IniZio/nexus3/internal/test/repro"
)

func main() {
	var reproDir string
	var phase string
	flag.StringVar(&reproDir, "repro-dir", "", "path to internal/test/repro/ (default: auto-detect from CWD)")
	flag.StringVar(&phase, "phase", "baseline", "phase to run: baseline|hostmem|guestmem|diskpressure|cpu|concurrency")
	flag.Parse()

	if reproDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fatalf("cannot get CWD: %v", err)
		}
		reproDir = findReproDir(cwd)
		if reproDir == "" {
			fatalf("cannot find internal/test/repro/ from %s; pass --repro-dir", cwd)
		}
	}

	stateDir := os.Getenv("NEXUS3_STATE_DIR")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatalf("cannot determine home dir: %v", err)
		}
		stateDir = filepath.Join(home, ".local", "state", "nexus3")
	}

	agentBin, _ := exec.LookPath("nexus3-agent")

	// baseBuild is the shared BuildConfig used by all phases.
	// Each phase overrides SandboxName and (where applicable) BuilderMemoryMiB.
	baseBuild := repro.BuildConfig{
		Nexus3:      os.Getenv("NEXUS3"),
		Workspace:   filepath.Join(reproDir, "workspace"),
		Project:     "repro",
		SandboxName: phase, // overridden per-run inside each phase
		ImageStore:  filepath.Join(stateDir, "images", "sha256"),
		AgentBin:    agentBin,
		ElfSize:     0,
		ExpectedHashes: nil,
		BuildTimeout:     25 * time.Minute,
		LogsDir:          filepath.Join(reproDir, "logs"),
		// Host swap is fully exhausted (8/8 GiB). 2 GiB builder reduces
		// FreePageReporting balloon pressure during large COPY layers.
		BuilderMemoryMiB: 2048,
	}

	ctx := context.Background()

	switch phase {
	case "baseline":
		runBaseline(ctx, reproDir, baseBuild)

	case "hostmem":
		cfg := repro.HostMemPhaseConfig{
			ReproDir:        reproDir,
			Build:           baseBuild,
			ExpectedBackend: repro.PersistentExt4,
		}
		results, err := repro.RunHostMemPhase(ctx, cfg)
		if err != nil {
			fatalf("hostmem phase: %v", err)
		}
		os.Exit(multiVerdict(results))

	case "guestmem":
		cfg := repro.GuestMemPhaseConfig{
			ReproDir:        reproDir,
			Build:           baseBuild,
			ExpectedBackend: repro.PersistentExt4,
		}
		results, err := repro.RunGuestMemPhase(ctx, cfg)
		if err != nil {
			fatalf("guestmem phase: %v", err)
		}
		os.Exit(multiVerdict(results))

	case "diskpressure":
		cfg := repro.DiskPressurePhaseConfig{
			ReproDir:        reproDir,
			Build:           baseBuild,
			ExpectedBackend: repro.PersistentExt4,
		}
		results, err := repro.RunDiskPressurePhase(ctx, cfg)
		if err != nil {
			fatalf("diskpressure phase: %v", err)
		}
		os.Exit(multiVerdict(results))

	case "cpu":
		cfg := repro.CPUPhaseConfig{
			ReproDir:        reproDir,
			Build:           baseBuild,
			ExpectedBackend: repro.PersistentExt4,
		}
		results, err := repro.RunCPUPhase(ctx, cfg)
		if err != nil {
			fatalf("cpu phase: %v", err)
		}
		os.Exit(multiVerdict(results))

	case "concurrency":
		cfg := repro.ConcurrencyPhaseConfig{
			ReproDir:        reproDir,
			Build:           baseBuild,
			ExpectedBackend: repro.PersistentExt4,
		}
		results, err := repro.RunConcurrencyPhase(ctx, cfg)
		if err != nil {
			fatalf("concurrency phase: %v", err)
		}
		os.Exit(multiVerdict(results))

	default:
		fatalf("unknown phase %q; valid: baseline|hostmem|guestmem|diskpressure|cpu|concurrency", phase)
	}
}

// runBaseline runs the single-run baseline phase and exits.
func runBaseline(ctx context.Context, reproDir string, baseBuild repro.BuildConfig) {
	baseBuild.SandboxName = "baseline"
	cfg := repro.BaselinePhaseConfig{
		ReproDir:        reproDir,
		Build:           baseBuild,
		ExpectedBackend: repro.PersistentExt4,
	}
	result, err := repro.RunBaselinePhase(ctx, cfg)
	if err != nil {
		fatalf("baseline phase: %v", err)
	}
	switch result.FinalVerdict() {
	case repro.TruncationReproduced:
		os.Exit(0)
	case repro.HarnessIntegrityFailure:
		os.Exit(2)
	case repro.NoTruncationObserved:
		os.Exit(3)
	default:
		fatalf("unexpected verdict from FinalVerdict()")
	}
}

// multiVerdict collapses a slice of RunResults into an exit code:
//
//	0 — any result is TruncationReproduced
//	2 — any result is HarnessIntegrityFailure (and no TruncationReproduced)
//	3 — all results are NoTruncationObserved
func multiVerdict(results []repro.RunResult) int {
	hasTrunc := false
	hasHIF := false
	for _, r := range results {
		switch r.FinalVerdict() {
		case repro.TruncationReproduced:
			hasTrunc = true
		case repro.HarnessIntegrityFailure:
			hasHIF = true
		}
	}
	if hasTrunc {
		return 0
	}
	if hasHIF {
		return 2
	}
	return 3
}

// findReproDir walks up from start looking for internal/test/repro/.
func findReproDir(start string) string {
	candidates := []string{
		filepath.Join(start, "internal", "test", "repro"),
	}
	dir := start
	for i := 0; i < 5; i++ {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		candidates = append(candidates, filepath.Join(dir, "internal", "test", "repro"))
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return ""
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "repro: "+format+"\n", args...)
	os.Exit(1)
}
