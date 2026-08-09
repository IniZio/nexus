package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// SandboxHarvestOutcome records the result of harvesting one sandbox.
type SandboxHarvestOutcome struct {
	// SandboxID is the sandbox that was harvested.
	SandboxID domain.SandboxID
	// HostPath is the local file path to which the pulled archive was written.
	// Empty when Err is non-nil.
	HostPath string
	// Err is nil on success and non-nil when the pull failed for this sandbox.
	Err error
}

// HarvestResult aggregates per-sandbox outcomes from [Service.HarvestMotive].
type HarvestResult struct {
	// Outcomes is one entry per sandbox belonging to the motive, in the order
	// returned by the store. Entries are present regardless of success or failure.
	Outcomes []SandboxHarvestOutcome
}

// harvestCopyFn is a function that copies guestSrcPath out of sb, writing
// the received archive bytes to destFile (which the caller has already created).
// It exists so tests can inject a fake without a real agent protocol.
type harvestCopyFn func(ctx context.Context, sb domain.Sandbox, guestSrcPath, destFile string) error

// HarvestMotive copies guestSrcPath from every sandbox in motiveID into
// hostDestDir, placing each sandbox's output under a per-sandbox subdirectory
// named by the sandbox ID.
//
// Partial failure: when one sandbox's pull fails the error is recorded in that
// sandbox's Outcome and harvesting continues for the remaining sandboxes. The
// method returns both the aggregated HarvestResult (including any successful
// pulls) and a non-nil aggregate error when at least one sandbox failed,
// so callers can detect partial failure while still accessing the successes.
func (s *Service) HarvestMotive(ctx context.Context, motiveID, guestSrcPath, hostDestDir string) (HarvestResult, error) {
	return s.harvestMotive(ctx, motiveID, guestSrcPath, hostDestDir, s.copyOneForHarvest)
}

// harvestMotive is the testable core. copyFn is called once per sandbox and
// must write the pulled bytes to destFile.
func (s *Service) harvestMotive(ctx context.Context, motiveID, guestSrcPath, hostDestDir string, copyFn harvestCopyFn) (HarvestResult, error) {
	sandboxes, err := s.GetByMotive(ctx, motiveID)
	if err != nil {
		return HarvestResult{}, fmt.Errorf("service: harvest motive %q: list sandboxes: %w", motiveID, err)
	}

	result := HarvestResult{
		Outcomes: make([]SandboxHarvestOutcome, 0, len(sandboxes)),
	}

	for _, sb := range sandboxes {
		outcome := s.harvestOne(ctx, sb, guestSrcPath, hostDestDir, copyFn)
		result.Outcomes = append(result.Outcomes, outcome)
	}

	// Collect a non-nil aggregate error if any sandbox failed, so callers can
	// detect partial failure while still accessing the successful outcomes.
	var errs []error
	for _, o := range result.Outcomes {
		if o.Err != nil {
			errs = append(errs, fmt.Errorf("sandbox %s: %w", o.SandboxID, o.Err))
		}
	}
	if len(errs) > 0 {
		return result, fmt.Errorf("service: harvest motive %q: %d sandbox(es) failed: %w",
			motiveID, len(errs), errors.Join(errs...))
	}
	return result, nil
}

// harvestOne performs the pull for a single sandbox and returns its outcome.
// It creates the per-sandbox subdirectory and output file before calling copyFn.
func (s *Service) harvestOne(ctx context.Context, sb domain.Sandbox, guestSrcPath, hostDestDir string, copyFn harvestCopyFn) SandboxHarvestOutcome {
	sbDir := filepath.Join(hostDestDir, sb.ID.String())
	if err := os.MkdirAll(sbDir, 0o755); err != nil {
		return SandboxHarvestOutcome{
			SandboxID: sb.ID,
			Err:       fmt.Errorf("mkdir %s: %w", sbDir, err),
		}
	}

	destFile := filepath.Join(sbDir, filepath.Base(guestSrcPath))
	if err := copyFn(ctx, sb, guestSrcPath, destFile); err != nil {
		return SandboxHarvestOutcome{
			SandboxID: sb.ID,
			Err:       err,
		}
	}

	return SandboxHarvestOutcome{
		SandboxID: sb.ID,
		HostPath:  destFile,
	}
}

// copyOneForHarvest is the production implementation of harvestCopyFn.
// It constructs an agent client for sb, creates the destination file, and
// issues a PULL copy to receive the tar archive from guestSrcPath.
func (s *Service) copyOneForHarvest(ctx context.Context, sb domain.Sandbox, guestSrcPath, destFile string) error {
	c, err := s.agentClientFor(sb.ID.String(), sb)
	if err != nil {
		return err
	}

	f, err := os.Create(destFile)
	if err != nil {
		return fmt.Errorf("create dest file %s: %w", destFile, err)
	}
	defer f.Close()

	return c.Copy(ctx, agent.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PULL,
		GuestPath: guestSrcPath,
		Dst:       f,
	})
}
