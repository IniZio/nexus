package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/supervisor"
)

const (
	hibernateErrCodeUnsupported = "hibernate_unsupported"
	hibernateErrCodeRefused     = "hibernate_refused"
	hibernateErrCodeSnapshot    = "snapshot_failed"
)

func init() {
	Register(Command{
		Name:    "hibernate",
		Summary: "Snapshot a running sandbox to disk and free its RAM (CH only; resume restores it)",
		Run:     runHibernate,
	})
	Register(Command{
		Name:    "resume",
		Summary: "Resume a paused or hibernated sandbox (--restore-mode copy|ondemand, --no-cold-fallback)",
		Run:     runResume,
	})
	Register(Command{
		Name:    "pause",
		Summary: "Freeze a running sandbox in memory (RAM stays allocated; `resume` thaws it)",
		Run:     runPause,
	})
}

// Seams for tests.
var (
	requestHibernateFn = supervisor.RequestHibernate
	resumeHibernatedFn = resumeHibernated
	supervisorLiveFn   = func(sb domain.Sandbox) bool {
		if sb.SupervisorSock == "" {
			return false
		}
		alive, _ := supervisor.CheckAndReconcile(sb.SupervisorPID, sb.SupervisorSock)
		return alive
	}
)

// hibernateCodeFor extends sandboxCodeFor with the hibernate error codes.
func hibernateCodeFor(err error) string {
	switch {
	case errors.Is(err, service.ErrHibernateUnsupported):
		return hibernateErrCodeUnsupported
	case errors.Is(err, service.ErrHibernateRefused):
		return hibernateErrCodeRefused
	case errors.Is(err, service.ErrSnapshotFailed):
		return hibernateErrCodeSnapshot
	}
	return sandboxCodeFor(err)
}

func errHibernate(prefix string, cause error) *CodedError {
	return &CodedError{Code: hibernateCodeFor(cause), Msg: prefix + ": " + cause.Error(), Err: cause}
}

type hibernateJSON struct {
	ID                  string `json:"id"`
	State               string `json:"state"`
	Already             bool   `json:"already"`
	PauseMs             int64  `json:"pause_ms"`
	SnapshotMs          int64  `json:"snapshot_ms"`
	TotalMs             int64  `json:"total_ms"`
	SnapshotBytes       int64  `json:"snapshot_bytes"`
	SnapshotBytesOnDisk int64  `json:"snapshot_bytes_on_disk"`
	SnapshotDir         string `json:"snapshot_dir"`
}

func runHibernate(ctx context.Context, args []string, out *Output) error {
	if len(args) != 1 {
		return &UsageError{Msg: "hibernate: usage: hibernate <id|prefix|project/name> [--json]"}
	}
	svc, err := newSandboxService()
	if err != nil {
		return errSandbox("hibernate", err)
	}
	return runHibernateWithSvc(ctx, args[0], out, svc)
}

// hibernateSandbox is the shared hibernate routing used by the CLI verb and
// the MCP sandbox_hibernate tool: a live supervisor owns the VM (RequestHibernate),
// otherwise the service hibernates in-process.
func hibernateSandbox(ctx context.Context, svc *service.Service, ref string) (domain.Sandbox, hibernateJSON, error) {
	sb, err := svc.ResolveRef(ctx, ref)
	if err != nil {
		return sb, hibernateJSON{}, err
	}
	var res service.HibernateOutcome
	switch {
	case sb.State == domain.Hibernated:
		res = service.HibernateOutcome{Already: true}
	case (sb.State == domain.Running || sb.State == domain.Paused) && supervisorLiveFn(sb):
		root, rerr := store.DefaultRoot()
		if rerr != nil {
			return sb, hibernateJSON{}, rerr
		}
		res, err = requestHibernateFn(ctx, supervisor.DefaultStateDir(root, sb.ID))
		if err != nil {
			return sb, hibernateJSON{}, err
		}
	default:
		// No live supervisor owns the VM: in-process (rejects Stopped etc.
		// with illegal_transition).
		res, err = svc.Hibernate(ctx, ref)
		if err != nil {
			return sb, hibernateJSON{}, err
		}
	}
	snapDir := sb.HibernateDir
	if fresh, gerr := svc.GetSandboxByID(ctx, sb.ID); gerr == nil && fresh.HibernateDir != "" {
		snapDir = fresh.HibernateDir
	}
	r := res.Result
	return sb, hibernateJSON{
		ID: sb.ID.String(), State: domain.Hibernated.String(), Already: res.Already,
		PauseMs: r.PauseMs, SnapshotMs: r.SnapshotMs, TotalMs: r.TotalMs,
		SnapshotBytes: r.SnapshotBytes, SnapshotBytesOnDisk: r.SnapshotBytesOnDisk,
		SnapshotDir: snapDir,
	}, nil
}

func runHibernateWithSvc(ctx context.Context, ref string, out *Output, svc *service.Service) error {
	sb, data, err := hibernateSandbox(ctx, svc, ref)
	if err != nil {
		return errHibernate("hibernate", err)
	}
	msg := fmt.Sprintf("hibernated sandbox %s (%s): snapshot %s in %dms", sb.Handle(), sb.ID,
		humanBytes(data.SnapshotBytes), data.TotalMs)
	if data.Already {
		msg = fmt.Sprintf("sandbox %s (%s) is already hibernated", sb.Handle(), sb.ID)
	}
	out.EmitSuccess("sandbox.hibernated", data, msg)
	return nil
}

type resumeJSON struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	Already        bool   `json:"already"`
	ResumedFrom    string `json:"resumed_from,omitempty"`
	RestoreMode    string `json:"restore_mode,omitempty"`
	RestoreMs      int64  `json:"restore_ms"`
	AgentReadyMs   int64  `json:"agent_ready_ms"`
	TotalMs        int64  `json:"total_ms"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	ClockSkewMs    int64  `json:"clock_skew_ms,omitempty"`
}

// resumeData maps a resumeResult to the resume JSON contract shared by the CLI
// verb and the MCP sandbox_resume tool.
func resumeData(res resumeResult) resumeJSON {
	data := resumeJSON{ID: res.Sandbox.ID.String(), State: domain.Running.String(), Already: res.Already}
	switch {
	case res.Already:
	case res.Report == nil:
		data.ResumedFrom = "memory"
	default:
		rep := res.Report
		data.ResumedFrom = rep.ResumedFrom
		data.RestoreMode = rep.RestoreMode
		data.RestoreMs = rep.RestoreMs
		data.AgentReadyMs = rep.AgentReadyMs
		data.TotalMs = rep.TotalMs
		data.FallbackReason = rep.FallbackReason
		data.ClockSkewMs = rep.ClockSkewMs
	}
	return data
}

// parseResumeArgs accepts flags before or after the ref (Go's flag package
// stops at the first positional).
func parseResumeArgs(args []string) (string, resumeOptions, error) {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mode := fs.String("restore-mode", "", "snapshot restore mode: copy (default) or ondemand")
	noCold := fs.Bool("no-cold-fallback", false, "fail instead of cold-starting when snapshot restore fails")
	var pos []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return "", resumeOptions{}, &UsageError{Msg: "resume: " + err.Error()}
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(pos) != 1 {
		return "", resumeOptions{}, &UsageError{Msg: "resume: usage: resume <id|prefix|project/name> [--restore-mode copy|ondemand] [--no-cold-fallback] [--json]"}
	}
	if *mode != "" && *mode != "copy" && *mode != "ondemand" {
		return "", resumeOptions{}, &UsageError{Code: sandboxErrCodeInvalidArgument, Msg: fmt.Sprintf("resume: --restore-mode %q: want copy or ondemand", *mode)}
	}
	return pos[0], resumeOptions{Mode: *mode, NoColdFallback: *noCold}, nil
}

func runResume(ctx context.Context, args []string, out *Output) error {
	ref, opts, err := parseResumeArgs(args)
	if err != nil {
		return err
	}
	svc, err := newSandboxService()
	if err != nil {
		return errSandbox("resume", err)
	}
	return runResumeWithSvc(ctx, ref, opts, out, svc)
}

func runResumeWithSvc(ctx context.Context, ref string, opts resumeOptions, out *Output, svc *service.Service) error {
	res, err := resumeHibernatedFn(ctx, svc, ref, opts)
	if err != nil {
		return errHibernate("resume", err)
	}
	sb := res.Sandbox
	data := resumeData(res)
	msg := fmt.Sprintf("resumed sandbox %s (%s)", sb.Handle(), sb.ID)
	switch {
	case res.Already:
		msg = fmt.Sprintf("sandbox %s (%s) is already running", sb.Handle(), sb.ID)
	case res.Report != nil:
		rep := res.Report
		msg = fmt.Sprintf("resumed sandbox %s (%s) from %s in %dms", sb.Handle(), sb.ID, rep.ResumedFrom, rep.TotalMs)
		if rep.FallbackReason != "" {
			fmt.Fprintf(out.Stderr(), "warning: snapshot restore failed (%s); cold-started\n", rep.FallbackReason)
		}
	}
	out.EmitSuccess("sandbox.resumed", data, msg)
	return nil
}

func runPause(ctx context.Context, args []string, out *Output) error {
	if len(args) != 1 {
		return &UsageError{Msg: "pause: usage: pause <id|prefix|project/name> [--json]"}
	}
	svc, err := newSandboxService()
	if err != nil {
		return errSandbox("pause", err)
	}
	return runPauseWithSvc(ctx, args[0], out, svc)
}

func runPauseWithSvc(ctx context.Context, ref string, out *Output, svc *service.Service) error {
	sb, err := svc.Pause(ctx, ref)
	if err != nil {
		return errHibernate("pause", err)
	}
	out.EmitSuccess("sandbox.paused", toSandboxInfoJSON(sb),
		fmt.Sprintf("paused sandbox %s (%s)", sb.Handle(), sb.ID))
	return nil
}
