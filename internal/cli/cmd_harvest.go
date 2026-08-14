package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/service"
)

// harvestErrCodePartialFailure is emitted when HarvestMotive succeeds for some
// sandboxes but fails for others. The harvest.done event is always emitted
// first so callers can inspect per-sandbox outcomes; this code signals the
// overall operation was not fully successful.
const harvestErrCodePartialFailure = "harvest_partial_failure"

func init() {
	Register(Command{
		Name:    "harvest",
		Summary: "Copy a guest path from every sandbox in a motive to the host",
		Run:     runHarvest,
	})
}

// ── JSON data types ───────────────────────────────────────────────────────────

// harvestOutcomeJSON is the per-sandbox result entry within a harvest.done event.
type harvestOutcomeJSON struct {
	SandboxID string `json:"sandbox_id"`
	// HostPath is the host-side file path the archive was written to.
	// Present only when the pull for this sandbox succeeded.
	HostPath string `json:"host_path,omitempty"`
	// Error is the failure reason for this sandbox.
	// Present only when the pull for this sandbox failed.
	Error string `json:"error,omitempty"`
}

// harvestDoneJSON is the data payload for the harvest.done event.
type harvestDoneJSON struct {
	MotiveID string               `json:"motive_id"`
	Outcomes []harvestOutcomeJSON `json:"outcomes"`
}

// ── command ───────────────────────────────────────────────────────────────────

// runHarvest is the registered Run function for the "harvest" command.
//
// Usage: nexus3 harvest <motive-id> <guest-src-path> <host-dest-dir>
//
// Copies <guest-src-path> from every sandbox belonging to <motive-id> into
// <host-dest-dir>. Each sandbox's output is placed in a per-sandbox
// subdirectory named by the sandbox ID.
func runHarvest(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("harvest", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "harvest: " + err.Error()}
	}
	pos := fs.Args()
	if len(pos) != 3 {
		return &UsageError{Msg: "harvest: usage: harvest <motive-id> <guest-src-path> <host-dest-dir>"}
	}
	motiveID := pos[0]
	guestSrcPath := pos[1]
	hostDestDir := pos[2]

	if motiveID == "" {
		return &UsageError{Msg: "harvest: motive-id must not be empty"}
	}

	svc, err := newSandboxService()
	if err != nil {
		return errSandbox("harvest", err)
	}
	return runHarvestWith(ctx, motiveID, guestSrcPath, hostDestDir, out, svc)
}

// runHarvestWith is the testable core of the harvest command. Tests inject
// a pre-built *service.Service to avoid substrate requirements.
//
// Partial failure: when HarvestMotive returns a non-nil error, the harvest.done
// event is still emitted (so callers can inspect per-sandbox outcomes), and
// this function returns a *CodedError with code "harvest_partial_failure" so
// root.go exits non-zero. Silently returning success when work was lost would
// be the worst possible outcome for a write-back command.
func runHarvestWith(ctx context.Context, motiveID, guestSrcPath, hostDestDir string, out *Output, svc *service.Service) error {
	result, harvestErr := svc.HarvestMotive(ctx, motiveID, guestSrcPath, hostDestDir)

	// Build the per-sandbox outcome list regardless of overall success or
	// failure so the caller always has a complete picture.
	outcomes := make([]harvestOutcomeJSON, 0, len(result.Outcomes))
	var successCount, failCount int
	for _, o := range result.Outcomes {
		oj := harvestOutcomeJSON{SandboxID: o.SandboxID.String()}
		if o.Err != nil {
			oj.Error = o.Err.Error()
			failCount++
		} else {
			oj.HostPath = o.HostPath
			successCount++
		}
		outcomes = append(outcomes, oj)
	}

	total := len(result.Outcomes)

	// Emit the outcome list first — even on partial failure — so the caller
	// can inspect per-sandbox results before reading the error envelope.
	var msg string
	if harvestErr == nil {
		msg = fmt.Sprintf("harvested %d/%d sandbox(es) to %s", successCount, total, hostDestDir)
	} else {
		msg = fmt.Sprintf("harvest: %d/%d sandbox(es) succeeded, %d failed; check outcomes for details", successCount, total, failCount)
	}
	out.EmitSuccess("harvest.done", harvestDoneJSON{
		MotiveID: motiveID,
		Outcomes: outcomes,
	}, msg)

	if harvestErr != nil {
		// Return a CodedError so root.go emits an error envelope and exits 1.
		// The harvest.done envelope above already holds per-sandbox detail;
		// this error carries the aggregate summary.
		return &CodedError{
			Code: harvestErrCodePartialFailure,
			Msg:  fmt.Sprintf("harvest motive %q: %d of %d sandbox(es) failed", motiveID, failCount, total),
			Err:  harvestErr,
		}
	}
	return nil
}
