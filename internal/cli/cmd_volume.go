package cli

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

func init() {
	Register(Command{
		Name:    "volume",
		Summary: "Manage named volumes (create|ls|rm|prune)",
		Run:     runVolume,
	})
}

func runVolume(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "volume: missing subcommand; usage: volume <create|ls|rm|prune>"}
	}

	verb := args[0]
	verbArgs := args[1:]

	vs, err := newVolumeStore()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: fmt.Sprintf("volume: open store: %v", err)}
	}

	switch verb {
	case "create":
		return runVolumeCreate(ctx, verbArgs, out, vs)
	case "ls":
		return runVolumeLs(ctx, verbArgs, out, vs)
	case "rm":
		return runVolumeRm(ctx, verbArgs, out, vs)
	case "prune":
		return runVolumePrune(ctx, verbArgs, out, vs)
	default:
		return &UsageError{Msg: fmt.Sprintf("volume: unknown subcommand %q; valid: create ls rm prune", verb)}
	}
}

// create

func runVolumeCreate(ctx context.Context, args []string, out *Output, vs *volumestore.VolumeStore) error {
	return runVolumeCreateWith(ctx, args, out, vs)
}

// runVolumeCreateWith is the testable core; tests inject vs directly.
//
// No disk-space preflight is applied here. For kind=dir the operation is a
// mkdir — zero disk allocation. For kind=disk, preallocateFile uses
// ftruncate (sparse file: no blocks until guest writes) and formatExt4 (mke2fs)
// writes only filesystem metadata (~5% of sizeBytes); neither cost is
// projectable from the CLI layer at create time because there is no source
// artifact to measure against. The disk fills at guest-write time, not here.
//
// The TBD-PD-26 preflight (commit 48d1b82, M3-AC2) covers CreateAndBoot and
// Service.Fork, where a real OCI artifact is copied — an immediate, measurable
// allocation. Volume create has no such source; applying the same check would
// either over-charge sizeBytes (~10–30× the actual mke2fs footprint) or use a
// fixed metadata estimate that passes unconditionally. Both outcomes are
// misleading. Volume create is intentionally outside the TBD-PD-26 scope.
func runVolumeCreateWith(ctx context.Context, args []string, out *Output, vs *volumestore.VolumeStore) error {
	fs := flag.NewFlagSet("volume create", flag.ContinueOnError)
	kindFlag := fs.String("kind", "disk", "volume kind: dir or disk")
	sizeFlag := fs.Int64("size", 0, "size in bytes for kind=disk (default: 10 GiB)")
	pathFlag := fs.String("path", "", "host directory path for kind=dir (default: managed)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "volume create: " + err.Error()}
	}
	if fs.NArg() == 0 {
		return &UsageError{Msg: "volume create: missing <name>; usage: volume create <name> [--kind=dir|disk]"}
	}
	if fs.NArg() > 1 {
		return &UsageError{Msg: fmt.Sprintf("volume create: unexpected argument %q", fs.Arg(1))}
	}
	name := fs.Arg(0)

	var kind volumestore.VolumeKind
	switch *kindFlag {
	case "disk":
		kind = volumestore.KindDisk
	case "dir":
		kind = volumestore.KindDir
	default:
		return &UsageError{Msg: fmt.Sprintf("volume create: invalid --kind %q; must be dir or disk", *kindFlag)}
	}

	rec, err := vs.Create(ctx, name, kind, *sizeFlag, *pathFlag)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: fmt.Sprintf("volume create %s: %v", name, err)}
	}

	type createResult struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		SizeBytes int64  `json:"size_bytes,omitempty"`
		HostPath  string `json:"host_path,omitempty"`
	}
	data := createResult{
		Name:      rec.Name,
		Kind:      string(rec.Kind),
		SizeBytes: rec.SizeBytes,
		HostPath:  rec.HostPath,
	}
	out.EmitSuccess("volume.created", data, fmt.Sprintf("volume %s created (kind=%s)", rec.Name, rec.Kind))
	return nil
}

// ls

func runVolumeLs(ctx context.Context, args []string, out *Output, vs *volumestore.VolumeStore) error {
	fs := flag.NewFlagSet("volume ls", flag.ContinueOnError)
	sandboxFlag := fs.String("sandbox", "", "filter volumes attached to this sandbox ID")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "volume ls: " + err.Error()}
	}
	if fs.NArg() > 0 {
		return &UsageError{Msg: fmt.Sprintf("volume ls: unexpected argument %q", fs.Arg(0))}
	}

	records, err := vs.List()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: fmt.Sprintf("volume ls: %v", err)}
	}

	// Filter by sandbox if requested.
	filterSandbox := *sandboxFlag
	if filterSandbox != "" {
		var filtered []*volumestore.VolumeRecord
		for _, r := range records {
			for _, a := range r.Attachments {
				if a.SandboxID == filterSandbox {
					filtered = append(filtered, r)
					break
				}
			}
		}
		records = filtered
	}

	type lsEntry struct {
		Name        string `json:"name"`
		Kind        string `json:"kind"`
		SizeBytes   int64  `json:"size_bytes,omitempty"`
		HostPath    string `json:"host_path,omitempty"`
		Attachments int    `json:"attachments"`
		CreatedAt   string `json:"created_at"`
	}
	entries := make([]lsEntry, len(records))
	for i, r := range records {
		entries[i] = lsEntry{
			Name:        r.Name,
			Kind:        string(r.Kind),
			SizeBytes:   r.SizeBytes,
			HostPath:    r.HostPath,
			Attachments: len(r.Attachments),
			CreatedAt:   r.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	if out.IsJSON() {
		out.EmitSuccess("volume.list", entries, "")
		return nil
	}

	if len(records) == 0 {
		fmt.Fprintln(out.Stdout(), "no volumes")
		return nil
	}

	tw := tabwriter.NewWriter(out.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tSIZE\tATTACHED\tCREATED")
	for _, r := range records {
		size := "-"
		if r.Kind == volumestore.KindDisk {
			size = formatBytes(r.SizeBytes)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			r.Name,
			string(r.Kind),
			size,
			len(r.Attachments),
			r.CreatedAt.Format("2006-01-02"),
		)
	}
	tw.Flush()
	return nil
}

// rm

func runVolumeRm(ctx context.Context, args []string, out *Output, vs *volumestore.VolumeStore) error {
	fs := flag.NewFlagSet("volume rm", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "volume rm: " + err.Error()}
	}
	if fs.NArg() == 0 {
		return &UsageError{Msg: "volume rm: missing <name>; usage: volume rm <name>"}
	}
	if fs.NArg() > 1 {
		return &UsageError{Msg: fmt.Sprintf("volume rm: unexpected argument %q", fs.Arg(1))}
	}
	name := fs.Arg(0)

	// Bound the per-volume flock acquisition (RISK-SD2-1): the root CLI ctx
	// carries no deadline, so a contended lock would spin forever without this.
	rmCtx, rmCancel := context.WithTimeout(ctx, 10*time.Second)
	defer rmCancel()
	if err := vs.Rm(rmCtx, name); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: fmt.Sprintf("volume rm %s: %v", name, err)}
	}

	out.EmitSuccess("volume.removed", map[string]string{"name": name}, fmt.Sprintf("volume %s removed", name))
	return nil
}

// prune

func runVolumePrune(ctx context.Context, args []string, out *Output, vs *volumestore.VolumeStore) error {
	fs := flag.NewFlagSet("volume prune", flag.ContinueOnError)
	applyFlag := fs.Bool("apply", false, "perform deletions (default: dry-run)")
	includeDetachedFlag := fs.Bool("include-detached", false, "also delete detached volumes (requires --apply)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "volume prune: " + err.Error()}
	}
	if fs.NArg() > 0 {
		return &UsageError{Msg: fmt.Sprintf("volume prune: unexpected argument %q", fs.Arg(0))}
	}

	root, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: fmt.Sprintf("volume prune: resolve state dir: %v", err)}
	}
	st, err := store.NewFileStore(root)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: fmt.Sprintf("volume prune: open state dir: %v", err)}
	}

	return runVolumePruneWith(ctx, out, vs, st, volumestore.PruneOptions{
		Apply:           *applyFlag,
		IncludeDetached: *includeDetachedFlag,
	})
}

// runVolumePruneWith is the testable core; tests inject vs and sandboxes.
func runVolumePruneWith(ctx context.Context, out *Output, vs *volumestore.VolumeStore, sandboxes volumestore.SandboxLister, opts volumestore.PruneOptions) error {
	res, err := vs.Prune(ctx, sandboxes, opts)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: fmt.Sprintf("volume prune: %v", err)}
	}

	type pruneReport struct {
		StubsDeleted         []string `json:"stubs_deleted"`
		OrphanedFilesDeleted []string `json:"orphaned_files_deleted"`
		DetachedCandidates   []string `json:"detached_candidates"`
		DetachedDeleted      []string `json:"detached_deleted"`
		Apply                bool     `json:"apply"`
		IncludeDetached      bool     `json:"include_detached"`
	}

	// Normalise nil slices to empty arrays for stable JSON output.
	normalize := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}

	data := pruneReport{
		StubsDeleted:         normalize(res.StubsDeleted),
		OrphanedFilesDeleted: normalize(res.OrphanedFilesDeleted),
		DetachedCandidates:   normalize(res.DetachedCandidates),
		DetachedDeleted:      normalize(res.DetachedDeleted),
		Apply:                opts.Apply,
		IncludeDetached:      opts.IncludeDetached,
	}

	if out.IsJSON() {
		out.EmitSuccess("volume.prune", data, "")
		return nil
	}

	// Human-readable summary.
	var sb strings.Builder
	if !opts.Apply {
		fmt.Fprintln(&sb, "(dry-run — pass --apply to delete)")
	}

	if len(res.StubsDeleted) > 0 {
		label := "would remove stub records"
		if opts.Apply {
			label = "removed stub records"
		}
		fmt.Fprintf(&sb, "%s: %s\n", label, strings.Join(res.StubsDeleted, ", "))
	}
	if len(res.OrphanedFilesDeleted) > 0 {
		label := "would remove orphaned files"
		if opts.Apply {
			label = "removed orphaned files"
		}
		fmt.Fprintf(&sb, "%s: %s\n", label, strings.Join(res.OrphanedFilesDeleted, ", "))
	}
	if len(res.DetachedCandidates) > 0 {
		fmt.Fprintf(&sb, "detached (run with --include-detached --apply to delete): %s\n",
			strings.Join(res.DetachedCandidates, ", "))
	}
	if len(res.DetachedDeleted) > 0 {
		fmt.Fprintf(&sb, "removed detached volumes: %s\n", strings.Join(res.DetachedDeleted, ", "))
	}

	if sb.Len() == 0 {
		fmt.Fprintln(&sb, "nothing to prune")
	}

	fmt.Fprint(out.Stdout(), sb.String())
	return nil
}

// helpers

// newVolumeStore returns a VolumeStore rooted at the default state directory.
func newVolumeStore() (*volumestore.VolumeStore, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return volumestore.New(filepath.Join(root, "volumes")), nil
}
