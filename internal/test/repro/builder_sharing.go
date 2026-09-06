package repro

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"context"
)

// sandboxListOutput is the JSON shape of `nexus3 --json sandbox list`.
type sandboxListOutput struct {
	Data struct {
		Sandboxes []struct {
			Handle string `json:"handle"`
			State  string `json:"state"`
		} `json:"sandboxes"`
	} `json:"data"`
}

// waitForBuilderFree blocks until no other repro/* sandbox is live and no
// cloud-hypervisor process belonging to a repro sandbox is running.
// It retries every 60 s for up to 15 minutes, then returns a HIF probe.
//
// allowedHandles is the set of sandbox refs that are allowed to coexist.
// Call this before every RunBuild invocation.
func waitForBuilderFree(ctx context.Context, nexus3Bin string, allowedHandles map[string]struct{}) *ProbeResult {
	deadline := time.Now().Add(15 * time.Minute)
	for {
		blocked, reason, checkErr := checkBuilderBusy(nexus3Bin, allowedHandles)
		if checkErr != nil {
			// Check failure is non-fatal: log and proceed — the build's own
			// preconditions will surface environment problems.
			fmt.Printf("[repro] WARN: builder-sharing check error: %v; proceeding\n", checkErr)
			return nil
		}
		if !blocked {
			return nil
		}
		if time.Now().After(deadline) {
			hif := probeHIF("precondition.builder_busy",
				fmt.Sprintf("BUILDER_BUSY after 15 min: %s", reason))
			return &hif
		}
		fmt.Printf("[repro] builder busy (%s); retrying in 60s (deadline %s)\n",
			reason, deadline.Format("15:04:05"))
		select {
		case <-ctx.Done():
			hif := probeHIF("precondition.builder_busy", "context cancelled while waiting for builder")
			return &hif
		case <-time.After(60 * time.Second):
		}
	}
}

// checkBuilderBusy returns (true, reason, nil) if another repro/* sandbox is
// running. It checks via `nexus3 --json sandbox list` (authoritative) and
// then cross-checks with pgrep for cloud-hypervisor processes that have a
// repro sandbox handle in their command line (belt-and-suspenders).
func checkBuilderBusy(nexus3Bin string, allowedHandles map[string]struct{}) (busy bool, reason string, err error) {
	if nexus3Bin == "" {
		nexus3Bin = "nexus3"
	}

	// Primary check: nexus3 sandbox list JSON.
	out, err := exec.Command(nexus3Bin, "--json", "sandbox", "list").Output()
	if err != nil {
		return false, "", fmt.Errorf("nexus3 sandbox list: %w", err)
	}
	var parsed sandboxListOutput
	if jsonErr := json.Unmarshal(out, &parsed); jsonErr != nil {
		return false, "", fmt.Errorf("parse sandbox list JSON: %w", jsonErr)
	}
	for _, sb := range parsed.Data.Sandboxes {
		if strings.HasPrefix(sb.Handle, "repro/") {
			if _, ok := allowedHandles[sb.Handle]; !ok {
				return true, fmt.Sprintf("sandbox %s state=%s", sb.Handle, sb.State), nil
			}
		}
	}

	// Secondary check: pgrep for cloud-hypervisor processes whose supervisor
	// cmdline mentions a repro/* sandbox handle (catches zombies the list missed).
	pgOut, pgErr := exec.Command("pgrep", "-fa", "cloud-hypervisor").Output()
	if pgErr != nil {
		// pgrep exits 1 when no processes match — that is not an error.
		return false, "", nil
	}
	for _, line := range strings.Split(string(pgOut), "\n") {
		if strings.Contains(line, "repro/") || strings.Contains(line, "repro-") {
			// Skip lines that belong to any of our allowed handles.
			isOurs := false
			for h := range allowedHandles {
				if strings.Contains(line, h) {
					isOurs = true
					break
				}
			}
			if !isOurs {
				return true, fmt.Sprintf("cloud-hypervisor with repro cmdline: %s", truncate(line, 120)), nil
			}
		}
	}

	return false, "", nil
}

// truncate returns s capped at n runes.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
