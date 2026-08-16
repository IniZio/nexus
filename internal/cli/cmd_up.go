package cli

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"sync"

	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

func init() {
	Register(Command{
		Name:    "up",
		Summary: "Create N sandboxes in one command with disk-space preflight",
		Run:     runUp,
	})
}

// upOutcomeJSON is the per-sandbox result included in the up.completed envelope.
type upOutcomeJSON struct {
	Index   int    `json:"index"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Handle  string `json:"handle,omitempty"`
	Error   string `json:"error,omitempty"`
	Success bool   `json:"success"`
}

// upResultJSON is the data payload for the up.completed success envelope.
type upResultJSON struct {
	Count    int             `json:"count"`
	Created  int             `json:"created"`
	Failed   int             `json:"failed"`
	Outcomes []upOutcomeJSON `json:"outcomes"`
}

// upErrCodeInsufficientDisk is the stable machine-contract code emitted when
// the disk-space preflight refuses the create batch.
const upErrCodeInsufficientDisk = "insufficient_disk"

// runUp is the registered Run function for the "up" command. It resolves the
// disk directory and service instance, then delegates to runUpWithSvc.
func runUp(ctx context.Context, args []string, out *Output) error {
	diskDir, err := upDefaultDiskDir()
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("up: disk dir: %v", err),
			Err:  err,
		}
	}
	svc, err := newSandboxService()
	if err != nil {
		return errSandbox("up", err)
	}
	return runUpWithSvc(ctx, args, out, svc, diskDir, service.CheckDiskSpace)
}

// upDefaultDiskDir returns the directory where per-sandbox workspace ext4
// images reside. Mirrors the private defaultDiskDir() in create.go.
func upDefaultDiskDir() (string, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "disks"), nil
}

// runUpWithSvc is the testable core of the "up" command. checkDisk is
// injectable so tests can control free-space reporting without touching the
// real filesystem.
//
// # Flags
//
//	--count N          number of sandboxes to create (default 1)
//	--project P        project for all sandboxes (default "default")
//	--label KEY=VALUE  repeatable label stamped on every sandbox
//
// # Preflight
//
// Before creating any sandbox, runUpWithSvc projects the total allocated disk
// consumption (count × per-sandbox estimate, measured as stat(2).Blocks*512
// on existing workspace disks — NOT apparent size). The projection is compared
// against the host's available bytes. On insufficient space the command exits
// with code 2 and an actionable message.
//
// # Partial-failure tolerance
//
// Each sandbox is created in its own goroutine. A failure in one sandbox does
// not abort the others: all N attempts run to completion and per-sandbox
// outcomes are collected. The final envelope reports created and failed counts.
// The exit code is 1 when at least one sandbox failed.
func runUpWithSvc(
	ctx context.Context,
	args []string,
	out *Output,
	svc *service.Service,
	diskDir string,
	checkDisk func(diskDir string, count int) (*service.DiskPreflightResult, error),
) error {
	// Parse flags manually to support repeatable --label KEY=VALUE.
	count := 1
	project := "default"
	labels := make(map[string]string)

	i := 0
	for i < len(args) {
		switch args[i] {
		case "--count":
			if i+1 >= len(args) {
				return &UsageError{Msg: "up: --count requires an argument"}
			}
			i++
			n := 0
			if _, err := fmt.Sscanf(args[i], "%d", &n); err != nil || n <= 0 {
				return &UsageError{Msg: fmt.Sprintf("up: --count %q: must be a positive integer", args[i])}
			}
			count = n

		case "--project":
			if i+1 >= len(args) {
				return &UsageError{Msg: "up: --project requires an argument"}
			}
			i++
			project = args[i]

		case "--label":
			if i+1 >= len(args) {
				return &UsageError{Msg: "up: --label requires an argument"}
			}
			i++
			k, v, ok := strings.Cut(args[i], "=")
			if !ok || k == "" {
				return &UsageError{Msg: fmt.Sprintf("up: --label %q: must be KEY=VALUE", args[i])}
			}
			labels[k] = v

		default:
			return &UsageError{Msg: fmt.Sprintf("up: unexpected argument %q; usage: up [--count N] [--project P] [--label KEY=VALUE ...]", args[i])}
		}
		i++
	}

	// Disk preflight: projects ALLOCATED bytes, not apparent size (M3-AC2).
	// stat(2).Blocks*512 is orders of magnitude smaller than apparent size for
	// sparse ext4 images; using apparent size would cause false rejections on
	// any machine with less free space than the apparent image size.
	//
	// root.go converts *UsageError to exit code 2 and calls out.EmitError
	// automatically — do NOT call out.EmitError here to avoid double-emission.
	if _, preflightErr := checkDisk(diskDir, count); preflightErr != nil {
		return &UsageError{Code: upErrCodeInsufficientDisk, Msg: preflightErr.Error()}
	}

	// Create N sandboxes concurrently with partial-failure tolerance (M3-AC1).
	outcomes := make([]upOutcomeJSON, count)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for idx := 0; idx < count; idx++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("up-%08x-%02d", rand.Uint32(), i)
			sb, cerr := svc.Create(ctx, project, name, service.CreateOptions{Labels: labels})

			o := upOutcomeJSON{Index: i}
			if cerr != nil {
				o.Error = cerr.Error()
				o.Success = false
				fmt.Fprintf(out.Stderr(), "up: [%d/%d] failed: %v\n", i+1, count, cerr)
			} else {
				o.ID = sb.ID.String()
				o.Name = sb.Name
				o.Handle = sb.Handle()
				o.Success = true
				fmt.Fprintf(out.Stderr(), "up: [%d/%d] created %s\n", i+1, count, sb.Handle())
			}

			mu.Lock()
			outcomes[i] = o
			mu.Unlock()
		}(idx)
	}
	wg.Wait()

	created, failed := 0, 0
	for _, o := range outcomes {
		if o.Success {
			created++
		} else {
			failed++
		}
	}

	result := upResultJSON{
		Count:    count,
		Created:  created,
		Failed:   failed,
		Outcomes: outcomes,
	}

	msg := fmt.Sprintf("created %d/%d sandbox(es)", created, count)
	if failed > 0 {
		msg = fmt.Sprintf("created %d/%d sandbox(es), %d failed", created, count, failed)
	}

	out.EmitSuccess("up.completed", result, msg)

	if failed > 0 {
		// Partial or full failure: exit 1. The EmitSuccess above already
		// delivered the per-sandbox outcome table.
		return &ExitCodeError{Code: 1}
	}
	return nil
}
