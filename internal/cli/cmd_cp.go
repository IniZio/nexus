package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
)

const guestPrefix = "guest:"

func init() {
	Register(Command{
		Name:    "cp",
		Summary: "Copy files between host and guest (guest:<path> prefix marks the guest side)",
		Run:     runCp,
	})
}

// cpDoneJSON is the --json data payload emitted on successful cp.
type cpDoneJSON struct {
	Direction string `json:"direction"`
	GuestPath string `json:"guest_path"`
	LocalPath string `json:"local_path"`
}

// runCp is the registered Run function for the "cp" command.
func runCp(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("cp", flag.ContinueOnError)
	dirFlag := fs.Bool("dir", false, "treat the guest path as a directory (archive transfer)")

	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "cp: " + err.Error()}
	}

	positional := fs.Args()
	if len(positional) != 3 {
		return &UsageError{Msg: "cp: usage: cp <sandbox-ref> <src> <dst>  (prefix guest side with guest:)"}
	}

	ref := positional[0]
	src := positional[1]
	dst := positional[2]

	direction, guestPath, localPath, err := parseCpArgs(src, dst)
	if err != nil {
		return &UsageError{Msg: "cp: " + err.Error()}
	}

	svc, err2 := newSandboxService()
	if err2 != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "cp: " + err2.Error(), Err: err2}
	}

	return runCpWithSvc(ctx, ref, direction, guestPath, localPath, *dirFlag, out, svc)
}

// parseCpArgs determines the copy direction and paths from src/dst arguments.
// Exactly one of src or dst must carry the "guest:" prefix.
func parseCpArgs(src, dst string) (direction agentpb.CopyDirection, guestPath, localPath string, err error) {
	srcIsGuest := strings.HasPrefix(src, guestPrefix)
	dstIsGuest := strings.HasPrefix(dst, guestPrefix)

	switch {
	case srcIsGuest && dstIsGuest:
		return 0, "", "", fmt.Errorf("both src and dst carry the guest: prefix; exactly one is required")
	case !srcIsGuest && !dstIsGuest:
		return 0, "", "", fmt.Errorf("neither src nor dst carries the guest: prefix; prefix the guest side with guest:")
	case srcIsGuest:
		// Pull: guest → host
		return agentpb.CopyDirection_COPY_DIRECTION_PULL,
			strings.TrimPrefix(src, guestPrefix),
			dst,
			nil
	default:
		// Push: host → guest
		return agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			strings.TrimPrefix(dst, guestPrefix),
			src,
			nil
	}
}

// cpService is the subset of [service.Service] required by [runCpWithSvc].
// Defined as an interface so tests can inject a fake without a real store or driver.
type cpService interface {
	Copy(ctx context.Context, ref string, opts agent.CopyOptions) error
}

// runCpWithSvc performs the file transfer.
// Extracted for testability.
func runCpWithSvc(ctx context.Context, ref string, direction agentpb.CopyDirection, guestPath, localPath string, isDir bool, out *Output, svc cpService) error {
	var opts agent.CopyOptions
	opts.Direction = direction
	opts.GuestPath = guestPath
	opts.IsDirectory = isDir

	switch direction {
	case agentpb.CopyDirection_COPY_DIRECTION_PULL:
		// Guest → host: open local file for writing.
		f, err := os.Create(localPath)
		if err != nil {
			return &CodedError{
				Code: ErrCodeInternalError,
				Msg:  fmt.Sprintf("cp: open local path %q: %v", localPath, err),
				Err:  err,
			}
		}
		defer f.Close()
		opts.Dst = f

	case agentpb.CopyDirection_COPY_DIRECTION_PUSH:
		// For single-file pushes, stat before opening so ExpectedBytes can be
		// declared. The guest's pushFile guard rejects the transfer if the
		// received byte count differs (fail-closed: nil → immediate rejection;
		// &0 → valid empty file). Directory pushes leave ExpectedBytes nil;
		// the tar header provides per-entry integrity in pushDir (symmetric
		// with pull, where declaredBytes is nil for dirs and validateTarStream
		// is the guard on the host side).
		if !isDir {
			fi, err := os.Stat(localPath)
			if err != nil {
				return &CodedError{
					Code: ErrCodeInternalError,
					Msg:  fmt.Sprintf("cp: stat local path %q: %v", localPath, err),
					Err:  err,
				}
			}
			sz := fi.Size()
			opts.ExpectedBytes = &sz
		}
		// NewPushReader opens a plain file or walks a directory into a tar
		// stream. The previous os.Open path caused EISDIR when the guest tried
		// to Read from a directory fd; NewPushReader returns a pipe-backed tar
		// reader for directories, matching how pull uses pullDir.
		src, err := agent.NewPushReader(localPath, isDir)
		if err != nil {
			return &CodedError{
				Code: ErrCodeInternalError,
				Msg:  fmt.Sprintf("cp: open local path %q: %v", localPath, err),
				Err:  err,
			}
		}
		defer src.Close()
		opts.Src = src
	}

	if err := svc.Copy(ctx, ref, opts); err != nil {
		return &CodedError{
			Code: agentCodeFor(err),
			Msg:  fmt.Sprintf("cp: %v", err),
			Err:  err,
		}
	}

	dirStr := "pull"
	if direction == agentpb.CopyDirection_COPY_DIRECTION_PUSH {
		dirStr = "push"
	}

	out.EmitSuccess("cp.done", cpDoneJSON{
		Direction: dirStr,
		GuestPath: guestPath,
		LocalPath: localPath,
	}, fmt.Sprintf("cp %s %s ↔ %s", dirStr, guestPath, localPath))
	return nil
}
