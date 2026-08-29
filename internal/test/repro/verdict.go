package repro

import "time"

// Verdict is the three-state result. Zero value is intentionally invalid.
type Verdict int

const (
	_ Verdict = iota // 0 = uninitialized; never valid
	TruncationReproduced
	HarnessIntegrityFailure
	NoTruncationObserved
)

func (v Verdict) String() string {
	switch v {
	case TruncationReproduced:
		return "TruncationReproduced"
	case HarnessIntegrityFailure:
		return "HarnessIntegrityFailure"
	case NoTruncationObserved:
		return "NoTruncationObserved"
	default:
		return "InvalidVerdict"
	}
}

// ProbeResult is a single probe's typed result.
type ProbeResult struct {
	Probe       string  // probe name e.g. "stageA.file_32m"
	Verdict     Verdict
	Detail      string  // human-readable detail
	// SkipVerdict marks informational probes whose result should appear in
	// output for observability but should NOT contribute to FinalVerdict.
	// Use only for "not-collected" stages — probes that structurally cannot
	// run in the current build path and would otherwise poison the verdict.
	SkipVerdict bool
}

// RunResult aggregates all probe results for one build run.
type RunResult struct {
	Label           string
	Probes          []ProbeResult
	Elapsed         time.Duration
	HostDiskFreeGiB float64      // available GiB on host at build-start; 0 if unavailable
	StateBackend    StateBackend // buildkit state backend detected from guest log; Unknown if not yet probed
	AgentBinPath    string       // path to nexus3-agent binary; empty if not found
	AgentBinSHA256  string       // hex SHA256 of the agent binary; empty if not available
	AgentLinkage    string       // "static", "dynamic", or "unknown"
	Nexus3BinPath   string       // path to nexus3 binary used for this run
	Nexus3SHA256    string       // hex SHA256 of the nexus3 binary; empty if not available
	RunID           string       // out-of-band run identifier injected into Containerfile
}

// FinalVerdict returns the aggregate verdict:
// - TruncationReproduced if ANY probe is TruncationReproduced
// - HarnessIntegrityFailure if ANY probe is HarnessIntegrityFailure (and no TruncationReproduced)
// - NoTruncationObserved only if ALL probes are NoTruncationObserved
func (r *RunResult) FinalVerdict() Verdict {
	hasTrunc := false
	hasHIF := false

	for _, p := range r.Probes {
		if p.SkipVerdict {
			continue // informational probe; excluded from aggregate verdict
		}
		if p.Verdict == TruncationReproduced {
			hasTrunc = true
		} else if p.Verdict == HarnessIntegrityFailure {
			hasHIF = true
		}
	}

	if hasTrunc {
		return TruncationReproduced
	}
	if hasHIF {
		return HarnessIntegrityFailure
	}
	return NoTruncationObserved
}

// IsValid returns false if v == 0 (uninitialized).
func (v Verdict) IsValid() bool {
	return v >= TruncationReproduced && v <= NoTruncationObserved
}

// probeHIF returns a HarnessIntegrityFailure ProbeResult for a named probe.
// Use this everywhere a probe can't run — never return a zero-value ProbeResult.
func probeHIF(name, detail string) ProbeResult {
	return ProbeResult{Probe: name, Verdict: HarnessIntegrityFailure, Detail: detail}
}

// probeNotCollected returns a SkipVerdict=true ProbeResult for a probe stage
// that could not collect data due to a structural limitation of the current
// build path (not a bug). It appears in the output for observability but does
// NOT affect FinalVerdict. Use ONLY when the absence of data is expected and
// documented — never to hide a real failure.
func probeNotCollected(name, reason string) ProbeResult {
	return ProbeResult{
		Probe:       name,
		Verdict:     HarnessIntegrityFailure,
		Detail:      "not_collected: " + reason,
		SkipVerdict: true,
	}
}

// probeOK returns a NoTruncationObserved ProbeResult.
func probeOK(name, detail string) ProbeResult {
	return ProbeResult{Probe: name, Verdict: NoTruncationObserved, Detail: detail}
}

// probeTrunc returns a TruncationReproduced ProbeResult.
func probeTrunc(name, detail string) ProbeResult {
	return ProbeResult{Probe: name, Verdict: TruncationReproduced, Detail: detail}
}
