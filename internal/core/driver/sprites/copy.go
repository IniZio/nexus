package sprites

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/driver"
)

const copyStderrCap = 2 << 10

// copyViaExec follows the guest-agent copy contract: a single file travels as
// raw bytes, a directory as a tar of entries relative to it. Paths reach the
// remote shell as positional args, never interpolated.
func copyViaExec(ctx context.Context, api API, name string, opts driver.CopyOptions) error {
	if opts.GuestPath == "" {
		return errors.New("sprites copy: guest path required")
	}
	for _, seg := range strings.Split(opts.GuestPath, "/") {
		if seg == ".." {
			return fmt.Errorf("sprites copy: guest path %q contains '..'", opts.GuestPath)
		}
	}
	var req ExecRequest
	var counted *countReader
	switch opts.Direction {
	case agentpb.CopyDirection_COPY_DIRECTION_PUSH:
		if opts.Src == nil {
			return errors.New("sprites copy push: src reader is nil")
		}
		if opts.IsDirectory {
			req.Argv = []string{"sh", "-c", `mkdir -p -- "$1" && tar -x -C "$1"`, "sh", opts.GuestPath}
			req.Stdin = opts.Src
		} else {
			if opts.ExpectedBytes == nil {
				return errors.New("sprites copy push file: ExpectedBytes absent; single-file PUSH must declare size")
			}
			req.Argv = []string{"sh", "-c", `mkdir -p -- "$(dirname -- "$1")" && cat > "$1"`, "sh", opts.GuestPath}
			counted = &countReader{r: opts.Src}
			req.Stdin = counted
		}
	case agentpb.CopyDirection_COPY_DIRECTION_PULL:
		if opts.Dst == nil {
			return errors.New("sprites copy pull: dst writer is nil")
		}
		if opts.IsDirectory {
			req.Argv = []string{"tar", "-c", "-C", opts.GuestPath, "."}
		} else {
			req.Argv = []string{"cat", "--", opts.GuestPath}
		}
		req.Stdout = opts.Dst
	default:
		return fmt.Errorf("sprites copy: unsupported direction %v", opts.Direction)
	}
	stderr := &tailBuffer{max: copyStderrCap}
	req.Stderr = stderr
	code, err := api.Exec(ctx, name, req)
	if err != nil {
		return fmt.Errorf("sprites copy: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("sprites copy: remote exit %d: %s", code, bytes.TrimSpace(stderr.buf))
	}
	if counted != nil && counted.n != *opts.ExpectedBytes {
		return fmt.Errorf("sprites copy push file: sent %d bytes, expected %d; transfer truncated", counted.n, *opts.ExpectedBytes)
	}
	return nil
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// tailBuffer keeps only the last max bytes written.
type tailBuffer struct {
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}
