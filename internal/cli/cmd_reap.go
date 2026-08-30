package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

func init() {
	Register(Command{
		Name:    "reap",
		Summary: "Report orphaned host resources; use --apply to delete them",
		Run:     runReap,
	})
}

// ── JSON data types ───────────────────────────────────────────────────────────

// reapEntryJSON is one resource in the reap.report output.
type reapEntryJSON struct {
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	OwnerID        string `json:"owner_id"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	AllocatedBytes int64  `json:"allocated_bytes"`
}

// reapReportJSON is the data payload for the reap.report event.
type reapReportJSON struct {
	Entries          []reapEntryJSON   `json:"entries"`
	ReclaimableBytes int64             `json:"reclaimable_bytes"`
	Deleted          []string          `json:"deleted"`
	Failed           []reapFailureJSON `json:"failed"`
	Apply            bool              `json:"apply"`
	// KilledPIDs are process-group leader pids that --apply killed to
	// reclaim a live netns-child orphan (no file was deleted for these).
	KilledPIDs []int `json:"killed_pids"`
}

// ── command ───────────────────────────────────────────────────────────────────

// runReap is the registered Run function for the "reap" command.
//
// Usage: nexus3 reap [--apply]
//
// Enumerates all nexus3 host resources by scanning the filesystem directly
// (never the record store). Classifies each as orphaned (no record, no live
// process) or owned/live (keep). Defaults to dry-run; --apply is required to
// delete anything.
//
// Machine-readable output lists every candidate with its status and reason.
// Reclaimable bytes are reported as allocated bytes (stat(2).Blocks * 512),
// never as apparent size, to avoid the P13 illusion where 101 GiB apparent
// masked 11.2 GiB real.
func runReap(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("reap", flag.ContinueOnError)
	applyFlag := fs.Bool("apply", false, "delete orphaned resources (default: dry-run)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "reap: " + err.Error()}
	}
	if fs.NArg() > 0 {
		return &UsageError{Msg: fmt.Sprintf("reap: unexpected argument %q; usage: nexus3 reap [--apply]", fs.Arg(0))}
	}
	return runReapWith(ctx, *applyFlag, out)
}

// runReapWith is the testable core. Tests may call it directly with injected
// stores and indexes via runReapFull.
func runReapWith(ctx context.Context, apply bool, out *Output) error {
	root, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("reap: resolve state directory: %v", err),
		}
	}
	st, err := store.NewFileStore(root)
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("reap: open state directory: %v", err),
		}
	}
	idx := service.NewResourceIndex(service.IndexConfig{}) // default dirs
	return runReapFull(ctx, st, idx, apply, out)
}

// runReapFull is the fully-injected implementation. Tests inject a fake store
// and a ResourceIndex pointed at a temp dir.
func runReapFull(ctx context.Context, st store.Store, idx *service.ResourceIndex, apply bool, out *Output) error {
	report, err := service.Reap(ctx, st, idx, apply)
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("reap: %v", err),
		}
	}

	// Build JSON-compatible output.
	entries := make([]reapEntryJSON, len(report.Entries))
	for i, e := range report.Entries {
		// Shadow disks are handle-keyed: use ShadowHandle as the owner key.
		// ULID-keyed resources use OwnerID.String().
		ownerKey := e.Resource.OwnerID.String()
		if e.Resource.Kind == service.KindDiskShadow {
			ownerKey = e.Resource.ShadowHandle // "" for legacy, safeHandle for B1
		}
		entries[i] = reapEntryJSON{
			Kind:           string(e.Resource.Kind),
			Path:           e.Resource.Path,
			OwnerID:        ownerKey,
			Status:         string(e.Status),
			Reason:         e.Reason,
			AllocatedBytes: e.AllocatedBytes,
		}
	}
	deleted := report.Deleted
	if deleted == nil {
		deleted = []string{}
	}
	failed := make([]reapFailureJSON, 0, len(report.Failed))
	for _, f := range report.Failed {
		failed = append(failed, reapFailureJSON{
			Path:   f.Path,
			Kind:   string(f.Kind),
			Reason: f.Reason,
		})
	}
	killedPIDs := report.KilledPIDs
	if killedPIDs == nil {
		killedPIDs = []int{}
	}
	data := reapReportJSON{
		Entries:          entries,
		ReclaimableBytes: report.ReclaimableBytes,
		Deleted:          deleted,
		Failed:           failed,
		Apply:            apply,
		KilledPIDs:       killedPIDs,
	}

	suspects := 0
	for _, e := range report.Entries {
		if e.Status == service.ReapStatusSuspect {
			suspects++
		}
	}

	if out.IsJSON() {
		out.EmitSuccess("reap.report", data, "")
		if len(report.Failed) > 0 || suspects > 0 {
			return &ExitCodeError{Code: 1}
		}
		return nil
	}

	// Human-readable output — out.w is the stdout writer (same package).
	orphans := 0
	for _, e := range report.Entries {
		if e.Status == service.ReapStatusOrphan {
			orphans++
		}
	}

	// A SUSPECT entry means reap could not rule out a live orphan — this must
	// never be folded into "No orphaned resources found." Silently omitting
	// what reap could not classify is the fail-open shape this ticket exists
	// to close (ticket 10 fail-closed rail): a degraded classification must
	// be the LOUD path, not indistinguishable from a clean report.
	if orphans == 0 && suspects == 0 {
		fmt.Fprintln(out.w, "No orphaned resources found.")
		return nil
	}

	mode := "dry-run"
	if apply {
		mode = "apply"
	}
	fmt.Fprintf(out.w, "Reap report (%s): %d orphan(s), %d suspect(s), %s reclaimable\n\n",
		mode, orphans, suspects, formatBytes(report.ReclaimableBytes))

	for _, e := range report.Entries {
		if e.Status != service.ReapStatusOrphan {
			continue
		}
		fmt.Fprintf(out.w, "  ORPHAN  %s  (%s)\n", e.Resource.Path, formatBytes(e.AllocatedBytes))
		fmt.Fprintf(out.w, "          %s\n", e.Reason)
	}
	for _, e := range report.Entries {
		if e.Status != service.ReapStatusSuspect {
			continue
		}
		fmt.Fprintf(out.w, "  SUSPECT %s\n", e.Resource.Path)
		fmt.Fprintf(out.w, "          %s\n", e.Reason)
	}

	if apply && (len(report.Deleted) > 0 || len(report.KilledPIDs) > 0) {
		fmt.Fprintf(out.w, "\nDeleted %d resource(s):\n", len(report.Deleted))
		for _, p := range report.Deleted {
			fmt.Fprintf(out.w, "  %s\n", p)
		}
		if len(report.KilledPIDs) > 0 {
			fmt.Fprintf(out.w, "Killed %d orphaned netns-child process group(s): %v\n", len(report.KilledPIDs), report.KilledPIDs)
		}
	} else if !apply && (orphans > 0 || suspects > 0) {
		fmt.Fprintf(out.w, "\nRun with --apply to delete.\n")
	}

	if suspects > 0 {
		fmt.Fprintf(out.Stderr(), "\n%d resource(s) could not be classified with confidence — treat as a possible orphan and inspect by hand; --apply never touches these.\n", suspects)
	}

	// Failures are printed AFTER the deleted list and to stderr, so a caller
	// piping stdout still sees them, and exit 1 so a script cannot read a
	// partial reclamation as a complete one (TBD-PD-37).
	if len(report.Failed) > 0 {
		fmt.Fprintf(out.Stderr(), "\nFAILED to reclaim %d of %d orphan(s):\n",
			len(report.Failed), orphans)
		for _, f := range report.Failed {
			fmt.Fprintf(out.Stderr(), "  %s\n          %s\n", f.Path, f.Reason)
		}
		fmt.Fprintf(out.Stderr(), "Re-run `nexus3 reap --apply`; if a path fails twice, inspect it by hand.\n")
		return &ExitCodeError{Code: 1}
	}

	if suspects > 0 {
		return &ExitCodeError{Code: 1}
	}

	return nil
}

// reapFailureJSON is the machine-readable form of service.ReapFailure.
type reapFailureJSON struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// formatBytes formats allocated bytes as a human-readable string.
func formatBytes(b int64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
	)
	switch {
	case b >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(b)/GiB)
	case b >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(b)/MiB)
	case b >= KiB:
		return fmt.Sprintf("%.1f KiB", float64(b)/KiB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
