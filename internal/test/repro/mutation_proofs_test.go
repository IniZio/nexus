// mutation_proofs_test.go — Part 2 mutation-proof tests for the repro harness.
//
// Each test demonstrates that a specific production-code guard is load-bearing:
// removing or weakening the guard causes the test to fail, proving the guard is
// the mechanism that enforces the invariant.
//
// Mutation procedure (per-test):
//  1. Run test — must PASS (guard intact).
//  2. Apply documented mutation to production code.
//  3. Run test — must FAIL; record the first mutation_proofs_test.go:NN error line.
//  4. Restore production code.
//  5. Confirm PASS.
package repro

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// manifestDataLine builds a bare-form rootfs-size-manifest data line.
// The parser requires 3 spaces after the colon before the relative path.
func manifestDataLine(relPath string, size int64) string {
	return fmt.Sprintf("2026/08/29 10:01:00 in-guest build: rootfs-size-manifest:   %-60s %d", relPath, size)
}

// ── Test 1 ───────────────────────────────────────────────────────────────────

// MUTATION: In probes.go ParseManifestStageA, in the `} else if size == TruncationSentinel {`
// branch for non-file_elf test files, comment out the
//
//	results = append(results, probeTrunc("stageA."+tf.name, "TRUNCATED_AT_32MiB"))
//
// line (leaving the branch a no-op) → test fails:
//
//	mutation_proofs_test.go:NN: FinalVerdict got HarnessIntegrityFailure, want TruncationReproduced
//
// RESTORE: put probeTrunc back → PASS.
func TestMutation_SyntheticTruncation(t *testing.T) {
	// Log: backend line + sentinel + one manifest entry at TruncationSentinel.
	logPath := writeLogFile(t, strings.Join([]string{
		logLineExt4,
		logLineSentinel,
		manifestDataLine("testfiles/file_64m", TruncationSentinel),
	}, "\n")+"\n")

	runner := func(ctx context.Context, cfg BuildConfig, label string) (BuildResult, *ProbeResult, error) {
		return BuildResult{Label: label, Elapsed: 60 * time.Second, BuildLog: logPath}, nil, nil
	}
	cfg := BaselinePhaseConfig{
		Build:    BuildConfig{LogsDir: t.TempDir()},
		ReproDir: t.TempDir(),
		Runner:   runner,
	}
	result, err := RunBaselinePhase(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunBaselinePhase: %v", err)
	}
	got := result.FinalVerdict()
	if got != TruncationReproduced {
		t.Errorf("FinalVerdict got %v, want TruncationReproduced", got)
	}
}

// ── Test 2 ───────────────────────────────────────────────────────────────────

// MUTATION: In state_backend.go ParseStateBackend, replace the final
//
//	return Unknown, probeHIF("builder.state_backend", "no backend line in guest log")
//
// with:
//
//	return Unknown, probeOK("builder.state_backend", "faked")
//
// → test fails:
//
//	mutation_proofs_test.go:NN: probe.Verdict got NoTruncationObserved, want HarnessIntegrityFailure
//
// RESTORE: put probeHIF back → PASS.
func TestMutation_ProbeDisabled_StateBackend(t *testing.T) {
	// Log with only the sentinel — no backend line.
	logPath := writeLogFile(t, logLineSentinel+"\n")

	_, probe := ParseStateBackend(logPath)
	if probe.Verdict != HarnessIntegrityFailure {
		t.Errorf("probe.Verdict got %v, want HarnessIntegrityFailure", probe.Verdict)
	}
	if probe.SkipVerdict {
		t.Errorf("probe.SkipVerdict=true; no-backend-line probe must be non-skip HIF to affect FinalVerdict")
	}

	// FinalVerdict of this probe alone must be HIF.
	r := RunResult{Probes: []ProbeResult{probe}}
	if got := r.FinalVerdict(); got != HarnessIntegrityFailure {
		t.Errorf("FinalVerdict got %v, want HarnessIntegrityFailure", got)
	}
}

// ── Test 3 ───────────────────────────────────────────────────────────────────

// MUTATION: In probes.go ParseManifestStageA, in the `if sawSentinel {` branch
// (len(manifest)==0 && sawSentinel), change
//
//	probeHIF("stageA.manifest", "manifest-channel active but no manifest data entries found; ...")
//
// to:
//
//	probeNotCollected("stageA.manifest", "manifest-channel active but no manifest data entries found; ...")
//
// → test fails:
//
//	mutation_proofs_test.go:NN: stageA.manifest SkipVerdict=true; sentinel present → must be non-skip HIF
//
// RESTORE: put probeHIF back → PASS.
func TestMutation_ProbeDisabled_StageA(t *testing.T) {
	// Sentinel present, no manifest data lines.
	logPath := writeLogFile(t, strings.Join([]string{
		logLineExt4,
		logLineSentinel,
		// no rootfs-size-manifest data lines
	}, "\n")+"\n")

	results := ParseManifestStageA(logPath, 0, 0)
	if len(results) == 0 {
		t.Fatal("ParseManifestStageA returned no probes")
	}

	var found bool
	for _, r := range results {
		if r.Probe == "stageA.manifest" {
			found = true
			if r.SkipVerdict {
				t.Errorf("stageA.manifest SkipVerdict=true; sentinel was present → must be non-skip HIF")
			}
			if r.Verdict != HarnessIntegrityFailure {
				t.Errorf("stageA.manifest Verdict got %v, want HarnessIntegrityFailure", r.Verdict)
			}
		}
	}
	if !found {
		t.Errorf("stageA.manifest probe not in results: %v", results)
	}

	// FinalVerdict of these probes must be HIF (not NTO).
	rr := RunResult{Probes: results}
	if got := rr.FinalVerdict(); got != HarnessIntegrityFailure {
		t.Errorf("FinalVerdict got %v, want HarnessIntegrityFailure", got)
	}
}

// ── Test 4 ───────────────────────────────────────────────────────────────────

// StageBSizeProbes on a nonexistent image must return non-SkipVerdict HIF probes.
// A dead debugfs must never silently clear the verdict.
//
// Key invariant demonstrated: if ALL probes were SkipVerdict (probeNotCollected),
// FinalVerdict degrades to NoTruncationObserved — proving that probeHIF (not
// probeNotCollected) is mandatory for debugfs failures.
//
// MUTATION: In StageBSizeProbes, change ALL debugfs-error returns from probeHIF to
// probeNotCollected → every probe becomes SkipVerdict=true →
// FinalVerdict degrades to NTO (the dangerous path demonstrated below is now the real path).
// Single-file mutation (just file_8m) is NOT a single-point-of-failure since other
// file probes still HIF; the all-file mutation is the effective one.
func TestMutation_ProbeDisabled_StageBDebugfs(t *testing.T) {
	probes := StageBSizeProbes("/nonexistent/image.ext4", 0, 0)
	if len(probes) == 0 {
		t.Fatal("StageBSizeProbes returned no probes for nonexistent image")
	}

	// Every probe must be HIF with SkipVerdict=false.
	for _, p := range probes {
		if p.SkipVerdict {
			t.Errorf("probe %q SkipVerdict=true; debugfs failure must yield non-skip HIF", p.Probe)
		}
		if p.Verdict != HarnessIntegrityFailure {
			t.Errorf("probe %q Verdict=%v, want HarnessIntegrityFailure", p.Probe, p.Verdict)
		}
	}

	// Safe path: HIF probes → FinalVerdict HIF.
	rr := RunResult{Probes: probes}
	if got := rr.FinalVerdict(); got != HarnessIntegrityFailure {
		t.Errorf("FinalVerdict with real HIF probes got %v, want HarnessIntegrityFailure", got)
	}

	// Dangerous path demonstration: if all probes were SkipVerdict, FinalVerdict
	// degrades to NTO — the wrong answer for a broken debugfs.
	// This proves the mutation (probeHIF → probeNotCollected) breaks the harness.
	skipProbes := make([]ProbeResult, len(probes))
	for i, p := range probes {
		skipProbes[i] = ProbeResult{Probe: p.Probe, Verdict: HarnessIntegrityFailure, SkipVerdict: true}
	}
	rs := RunResult{Probes: skipProbes}
	if got := rs.FinalVerdict(); got != NoTruncationObserved {
		t.Errorf("all-SkipVerdict probes FinalVerdict got %v, want NoTruncationObserved (dangerous path proof)", got)
	}
}

// ── Tests 5–7 ────────────────────────────────────────────────────────────────

// All three tests target the same guard in RunBaselinePhase:
//
//	if hif != nil {
//	    result.Probes = []ProbeResult{*hif}
//	    printRunResult(result)
//	    return result, nil
//	}
//
// MUTATION: comment out that block → the HIF probe from the runner is discarded;
// RunBaselinePhase continues and ParseStateBackend runs on empty BuildLog path →
// result.Probes[0].Probe becomes "builder.state_backend", not the expected probe name
// → test fails:
//
//	mutation_proofs_test.go:NN: Probes[0].Probe got "builder.state_backend", want "builder.cache_miss_gate"
//
// RESTORE: put the block back → PASS.
// (Tests 6 and 7 fail with their respective probe names.)

func TestMutation_ProbeDisabled_CacheMissGate(t *testing.T) {
	runHIFGateTest(t, "builder.cache_miss_gate", "CACHE_HIT_SUSPECTED")
}

func TestMutation_ProbeDisabled_DiskPrecondition(t *testing.T) {
	runHIFGateTest(t, "precondition.disk_space", "INSUFFICIENT_DISK_SPACE")
}

func TestMutation_ProbeDisabled_BuilderSharing(t *testing.T) {
	runHIFGateTest(t, "precondition.builder_busy", "BUILDER_BUSY")
}

// runHIFGateTest is the shared body for Tests 5–7.
// Injects a HIF probe from the fake runner and asserts RunBaselinePhase
// preserves it as the first (and only non-discarded) probe in the result.
func runHIFGateTest(t *testing.T, probeName, detail string) {
	t.Helper()
	hifProbe := ProbeResult{Probe: probeName, Verdict: HarnessIntegrityFailure, Detail: detail}
	runner := func(ctx context.Context, cfg BuildConfig, label string) (BuildResult, *ProbeResult, error) {
		return BuildResult{Label: label, Elapsed: 5 * time.Second}, &hifProbe, nil
	}
	cfg := BaselinePhaseConfig{
		Build:    BuildConfig{LogsDir: t.TempDir()},
		ReproDir: t.TempDir(),
		Runner:   runner,
	}
	result, err := RunBaselinePhase(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunBaselinePhase: %v", err)
	}

	// Guard must preserve the HIF probe as result.Probes[0].
	if len(result.Probes) == 0 {
		t.Fatalf("Probes empty; guard may have been removed")
	}
	if result.Probes[0].Probe != probeName {
		t.Errorf("Probes[0].Probe got %q, want %q; guard may have been removed", result.Probes[0].Probe, probeName)
	}
	if got := result.FinalVerdict(); got != HarnessIntegrityFailure {
		t.Errorf("FinalVerdict got %v, want HarnessIntegrityFailure", got)
	}
}

// ── Test 8 ───────────────────────────────────────────────────────────────────

// TestMutation_CacheHitNeverNTO proves the `if hif != nil { ... return result, nil }` guard
// in RunBaselinePhase (phase_baseline.go) is load-bearing.
//
// Isolation strategy: when the guard is intact, result.Probes contains exactly ONE probe
// (the cache-miss HIF). When the guard is removed, RunBaselinePhase falls through to
// ParseStateBackend + Stage A + Stage B, which populate many probes.
// We detect removal by asserting len(non-skip probes) == 1.
//
// MUTATION: comment out the `if hif != nil { result.Probes = []ProbeResult{*hif}; printRunResult(result); return result, nil }` block in phase_baseline.go
//
//	→ test fails:
//	mutation_proofs_test.go:310: expected exactly 1 non-skip probe (cache-miss HIF guard); got 12 — MUTATION PROOF FAIL: if hif!=nil guard was removed
//
// RESTORE → PASS.
func TestMutation_CacheHitNeverNTO(t *testing.T) {
	logPath := writeLogFile(t, "no content\n")
	cacheHitHIF := &ProbeResult{
		Probe:   "builder.cache_miss_gate",
		Verdict: HarnessIntegrityFailure,
		Detail:  "CACHE_HIT_SUSPECTED: build in 5s",
	}
	fakeRunner := func(ctx context.Context, cfg BuildConfig, label string) (BuildResult, *ProbeResult, error) {
		return BuildResult{Label: label, Elapsed: 5 * time.Second, BuildLog: logPath}, cacheHitHIF, nil
	}
	cfg := BaselinePhaseConfig{
		ReproDir:        t.TempDir(),
		Build:           BuildConfig{LogsDir: t.TempDir()},
		ExpectedBackend: PersistentExt4,
		Runner:          fakeRunner,
	}
	result, err := RunBaselinePhase(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunBaselinePhase: %v", err)
	}

	// Count non-skip probes: guard intact → 1; guard removed → many (Stage B HIFs).
	nonSkip := 0
	for _, p := range result.Probes {
		if !p.SkipVerdict {
			nonSkip++
		}
	}
	if nonSkip != 1 {
		t.Errorf("expected exactly 1 non-skip probe (cache-miss HIF guard); got %d — "+
			"MUTATION PROOF FAIL: if hif!=nil guard was removed", nonSkip)
	}
	found := false
	for _, p := range result.Probes {
		if p.Probe == "builder.cache_miss_gate" && p.Verdict == HarnessIntegrityFailure {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("builder.cache_miss_gate HIF probe missing from result.Probes")
	}
	if v := result.FinalVerdict(); v != HarnessIntegrityFailure {
		t.Errorf("FinalVerdict got %v, want HarnessIntegrityFailure", v)
	}
}

// ── Test 9 ───────────────────────────────────────────────────────────────────

// MUTATION: In ParseManifestStageA, in the non-sentinel-mismatch branch for non-file_elf files:
//
//	results = append(results, probeHIF("stageA."+tf.name,
//	    fmt.Sprintf("unexpected_size_not_sentinel: got %d, expected %d", size, tf.expected)))
//
// change probeHIF to probeTrunc → test fails:
//
//	mutation_proofs_test.go:NN: stageA.file_64m Verdict=TruncationReproduced for size=0; must be HIF
//
// RESTORE: put probeHIF back → PASS.
func TestMutation_NonSentinelMismatch_NeverTruncationReproduced(t *testing.T) {
	for _, tc := range []struct {
		size int64
		desc string
	}{
		{0, "zero"},
		{12345, "arbitrary-nonzero"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			logPath := writeLogFile(t, strings.Join([]string{
				logLineExt4,
				logLineSentinel,
				manifestDataLine("testfiles/file_64m", tc.size),
			}, "\n")+"\n")

			results := ParseManifestStageA(logPath, 0, 0)
			probeMap := make(map[string]ProbeResult)
			for _, r := range results {
				probeMap[r.Probe] = r
			}

			r, ok := probeMap["stageA.file_64m"]
			if !ok {
				t.Fatalf("stageA.file_64m probe missing; all probes: %v", results)
			}
			if r.Verdict == TruncationReproduced {
				t.Errorf("stageA.file_64m Verdict=TruncationReproduced for size=%d; must be HIF", tc.size)
			}
			if r.Verdict != HarnessIntegrityFailure {
				t.Errorf("stageA.file_64m Verdict got %v, want HarnessIntegrityFailure; detail=%q", r.Verdict, r.Detail)
			}
			if !strings.Contains(r.Detail, "unexpected_size_not_sentinel") {
				t.Errorf("stageA.file_64m Detail %q missing unexpected_size_not_sentinel", r.Detail)
			}
			wantSub := fmt.Sprintf("got %d, expected %d", tc.size, int64(67108864))
			if !strings.Contains(r.Detail, wantSub) {
				t.Errorf("stageA.file_64m Detail %q missing %q", r.Detail, wantSub)
			}
		})
	}
}

// ── Test 10 ──────────────────────────────────────────────────────────────────

// TestMutation_HashProbes_Unconfigured_SkipVerdict proves that hash probes with
// nil expectedHashes are always SkipVerdict=true and never affect FinalVerdict.
func TestMutation_HashProbes_Unconfigured_SkipVerdict(t *testing.T) {
	probes := StageBHashProbes("/nonexistent/image.ext4", nil)
	if len(probes) == 0 {
		t.Fatal("StageBHashProbes returned no probes for nil expectedHashes")
	}
	for _, p := range probes {
		if !p.SkipVerdict {
			t.Errorf("probe %q SkipVerdict=false; unconfigured hash probe must be SkipVerdict=true", p.Probe)
		}
	}

	// All probes are SkipVerdict → FinalVerdict must be NTO (hash probes contribute nothing).
	r := RunResult{Probes: probes}
	if got := r.FinalVerdict(); got != NoTruncationObserved {
		t.Errorf("all-SkipVerdict probes FinalVerdict got %v, want NoTruncationObserved", got)
	}
}

// ── Test 11 ──────────────────────────────────────────────────────────────────

// MUTATION: In StageBHashProbes, change the dump-failure path for file_32m from
//
//	probeHIF("stageB.file_32m.hash", fmt.Sprintf("dump failed: %v", err))
//
// to:
//
//	probeNotCollected("stageB.file_32m.hash", fmt.Sprintf("dump failed: %v", err))
//
// → test fails:
//
//	mutation_proofs_test.go:NN: probe "stageB.file_32m.hash" SkipVerdict=true; configured+failed probe must be non-skip HIF
//
// RESTORE: put probeHIF back → PASS.
func TestMutation_HashProbes_Configured_Mismatch_NotSkip(t *testing.T) {
	// Configured hash but nonexistent image → DebugfsDump fails → must yield non-skip HIF.
	configured := map[string]string{
		"file_32m": "aabbccdd" + strings.Repeat("00", 28), // 8 + 56 = 64 hex chars
	}
	probes := StageBHashProbes("/nonexistent/image.ext4", configured)

	var found bool
	for _, p := range probes {
		if p.Probe == "stageB.file_32m.hash" {
			found = true
			if p.SkipVerdict {
				t.Errorf("probe %q SkipVerdict=true; configured+failed probe must be non-skip HIF", p.Probe)
			}
			if p.Verdict != HarnessIntegrityFailure {
				t.Errorf("probe %q Verdict got %v, want HarnessIntegrityFailure", p.Probe, p.Verdict)
			}
		}
	}
	if !found {
		t.Errorf("stageB.file_32m.hash probe not in results: %v", probes)
	}

	r := RunResult{Probes: probes}
	if got := r.FinalVerdict(); got != HarnessIntegrityFailure {
		t.Errorf("FinalVerdict got %v, want HarnessIntegrityFailure", got)
	}
}

// ── Test B ───────────────────────────────────────────────────────────────────

// TestMutation_ClassifySizeAllSites covers all 11+ Stage A and Stage B classifySize call sites.
// Sub-cases per site:
//
//	sentinel: got == TruncationSentinel → want TruncationReproduced
//	zero:     got == 0                  → want HarnessIntegrityFailure
//	over:     got == expected + 1       → want HarnessIntegrityFailure (when expected > 0)
//	correct:  got == expected           → want NoTruncationObserved   (when expected > 0)
//	          got == plausible_nz       → want NoTruncationObserved   (when expected <= 0)
//
// MUTATION A: change `got == TruncationSentinel` to `got != expected` in classifySize
//
//	→ expected+1 sub-case for all sites with expected>0 flips to TruncationReproduced.
//	FIRST FAIL: mutation_proofs_test.go:524: got Verdict=TruncationReproduced, want HarnessIntegrityFailure; detail="TRUNCATED_AT_32MiB"
//
// MUTATION B: change `if got == 0 { return probeHIF(...) }` to `if got == 0 { return probeOK(probe, "size=0") }` in classifySize
//
//	→ zero sub-case flips to NoTruncationObserved.
//	FIRST FAIL: mutation_proofs_test.go:516: got Verdict=NoTruncationObserved, want HarnessIntegrityFailure; detail="size=0" — size=0 is always a corrupt export
func TestMutation_ClassifySizeAllSites(t *testing.T) {
	type siteSpec struct {
		probe    string
		expected int64 // <= 0 means "no fixed expected size"
	}
	sites := []siteSpec{
		// Stage A
		{"stageA.file_8m", 8388608},
		{"stageA.file_31m", 32505856},
		{"stageA.file_32m", 33554433}, // 32 MiB + 1 — distinct from TruncationSentinel
		{"stageA.file_33m", 34603008},
		{"stageA.file_40m", 41943040},
		{"stageA.file_64m", 67108864},
		{"stageA.file_200m", 209715200},
		{"stageA.file_elf", 0},           // no fixed expected
		{"stageA.run-produced-40m", ExpRunProduced40m},
		{"stageA.docker-compose", 0},     // no fixed expected
		{"stageA.nexus3-agent", 36329665}, // plausible agent size
		// Stage B
		{"stageB.file_8m", 8388608},
		{"stageB.file_31m", 32505856},
		{"stageB.file_32m", 33554433},
		{"stageB.file_33m", 34603008},
		{"stageB.file_40m", 41943040},
		{"stageB.file_64m", 67108864},
		{"stageB.file_200m", 209715200},
		{"stageB.file_elf", 0},
		{"stageB.run-produced-40m", ExpRunProduced40m},
		{"stageB.docker-compose", 0},
		{"stageB.nexus3-agent", 36329665},
	}

	for _, site := range sites {
		site := site
		t.Run(site.probe, func(t *testing.T) {
			// Sub-case: sentinel
			t.Run("sentinel", func(t *testing.T) {
				r := classifySize(site.probe, TruncationSentinel, site.expected)
				if r.Verdict != TruncationReproduced {
					t.Errorf("got Verdict=%v, want TruncationReproduced; detail=%q", r.Verdict, r.Detail)
				}
			})
			// Sub-case: zero
			t.Run("got=0", func(t *testing.T) {
				r := classifySize(site.probe, 0, site.expected)
				if r.Verdict != HarnessIntegrityFailure {
					t.Errorf("got Verdict=%v, want HarnessIntegrityFailure; detail=%q — size=0 is always a corrupt export", r.Verdict, r.Detail)
				}
			})
			// Sub-case: expected+1 (only when expected > 0)
			if site.expected > 0 {
				t.Run("expected+1", func(t *testing.T) {
					r := classifySize(site.probe, site.expected+1, site.expected)
					if r.Verdict != HarnessIntegrityFailure {
						t.Errorf("got Verdict=%v, want HarnessIntegrityFailure; detail=%q", r.Verdict, r.Detail)
					}
					if !strings.Contains(r.Detail, "unexpected_size_not_sentinel") {
						t.Errorf("Detail %q missing 'unexpected_size_not_sentinel'", r.Detail)
					}
				})
				// Sub-case: correct
				t.Run("correct", func(t *testing.T) {
					r := classifySize(site.probe, site.expected, site.expected)
					if r.Verdict != NoTruncationObserved {
						t.Errorf("got Verdict=%v, want NoTruncationObserved; detail=%q", r.Verdict, r.Detail)
					}
				})
			} else {
				// No fixed expected: use a plausible non-sentinel size
				t.Run("plausible_nz", func(t *testing.T) {
					const plausible = int64(66503008) // docker-compose typical size
					r := classifySize(site.probe, plausible, site.expected)
					if r.Verdict != NoTruncationObserved {
						t.Errorf("got Verdict=%v, want NoTruncationObserved for plausible non-sentinel size; detail=%q", r.Verdict, r.Detail)
					}
				})
			}
		})
	}
}

// ── Test 12 ──────────────────────────────────────────────────────────────────

// MUTATION: In verdict.go FinalVerdict(), comment out the
//
//	if hasTrunc { return TruncationReproduced }
//
// block → test fails:
//
//	mutation_proofs_test.go:NN: [trunc_wins_over_hif_and_nto] FinalVerdict got HarnessIntegrityFailure, want TruncationReproduced
//
// RESTORE: put hasTrunc block back → PASS.
func TestMutation_VerdictPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		probes []ProbeResult
		want   Verdict
	}{
		{
			// TruncationReproduced beats both HIF and NTO.
			"trunc_wins_over_hif_and_nto",
			[]ProbeResult{
				{Probe: "a", Verdict: TruncationReproduced},
				{Probe: "b", Verdict: HarnessIntegrityFailure},
				{Probe: "c", Verdict: NoTruncationObserved},
			},
			TruncationReproduced,
		},
		{
			// HIF beats NTO when no Trunc probe exists.
			"hif_wins_over_nto",
			[]ProbeResult{
				{Probe: "a", Verdict: HarnessIntegrityFailure},
				{Probe: "b", Verdict: NoTruncationObserved},
			},
			HarnessIntegrityFailure,
		},
		{
			// SkipVerdict HIF is excluded; only non-skip NTO remains → NTO.
			"skip_hif_excluded_nto_wins",
			[]ProbeResult{
				{Probe: "a", Verdict: HarnessIntegrityFailure, SkipVerdict: true},
				{Probe: "b", Verdict: NoTruncationObserved},
			},
			NoTruncationObserved,
		},
		{
			// All probes SkipVerdict → zero non-skip probes → NTO (zero-probe path).
			"all_skip_is_nto",
			[]ProbeResult{
				{Probe: "a", Verdict: HarnessIntegrityFailure, SkipVerdict: true},
			},
			NoTruncationObserved,
		},
		{
			// TruncationReproduced wins even when the only HIF is SkipVerdict.
			"trunc_over_skip_hif",
			[]ProbeResult{
				{Probe: "a", Verdict: TruncationReproduced},
				{Probe: "b", Verdict: HarnessIntegrityFailure, SkipVerdict: true},
			},
			TruncationReproduced,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := RunResult{Probes: tc.probes}
			got := r.FinalVerdict()
			if got != tc.want {
				t.Errorf("[%s] FinalVerdict got %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
