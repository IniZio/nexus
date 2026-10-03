package hubclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/IniZio/nexus/internal/core/hostbin"
)

// ErrProtocolMismatch is returned when nexus-hub rejects --protocol.
var ErrProtocolMismatch = errors.New("hubclient: nexus-hub protocol mismatch")

// Emitter is the seam other packages mock.
type Emitter interface {
	// EmitBestEffort appends ev and never fails the caller (C1).
	EmitBestEffort(ctx context.Context, ev Event)
	// Last returns the latest event for subject, or nil when none exists.
	Last(ctx context.Context, subject string) (*Event, error)
}

// Client execs the embedded nexus-hub binary. It implements Emitter.
type Client struct {
	// Resolve returns the nexus-hub path; nil uses hostbin.ResolveEmbeddedTool.
	Resolve func() (string, error)
	// Env is appended to the child environment.
	Env []string
}

var _ Emitter = (*Client)(nil)

// New returns a Client that resolves the embedded nexus-hub.
func New() *Client { return &Client{} }

func (c *Client) cmd(ctx context.Context, verb string, args ...string) (*exec.Cmd, error) {
	resolve := c.Resolve
	if resolve == nil {
		resolve = func() (string, error) { return hostbin.ResolveEmbeddedTool("nexus-hub") }
	}
	path, err := resolve()
	if err != nil {
		return nil, fmt.Errorf("hubclient: resolve nexus-hub: %w", err)
	}
	full := append([]string{"--protocol", strconv.Itoa(ProtocolVersion), verb}, args...)
	cmd := exec.CommandContext(ctx, path, full...)
	if len(c.Env) > 0 {
		cmd.Env = append(cmd.Environ(), c.Env...)
	}
	return cmd, nil
}

func wrapExit(err error, stderr string) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == ExitProtocolMismatch {
		return fmt.Errorf("%w: %s", ErrProtocolMismatch, strings.TrimSpace(stderr))
	}
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("hubclient: nexus-hub: %w: %s", err, s)
	}
	return fmt.Errorf("hubclient: nexus-hub: %w", err)
}

func (c *Client) run(ctx context.Context, stdin []byte, verb string, args ...string) ([]byte, error) {
	cmd, err := c.cmd(ctx, verb, args...)
	if err != nil {
		return nil, err
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		return nil, wrapExit(err, errb.String())
	}
	return out.Bytes(), nil
}

// Append stores ev and returns its assigned seq.
func (c *Client) Append(ctx context.Context, ev Event) (int64, error) {
	if ev.Actor == "" {
		if ev.Actor = os.Getenv(EnvSession); ev.Actor == "" {
			ev.Actor = ActorAnonymous
		}
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	out, err := c.run(ctx, body, VerbAppend)
	if err != nil {
		return 0, err
	}
	var res AppendResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &res); err != nil {
		return 0, fmt.Errorf("hubclient: parse append result %q: %w", out, err)
	}
	return res.Seq, nil
}

// EmitBestEffort appends ev, logging a WARN on any failure. It never errors.
func (c *Client) EmitBestEffort(ctx context.Context, ev Event) {
	if _, err := c.Append(ctx, ev); err != nil {
		slog.Warn("hub emit failed", "type", ev.Type, "subject", ev.Subject, "err", err)
	}
}

// Last returns the latest event for subject, or nil when none exists.
func (c *Client) Last(ctx context.Context, subject string) (*Event, error) {
	out, err := c.run(ctx, nil, VerbLast, "--subject", subject)
	if err != nil {
		return nil, err
	}
	evs, err := decodeEvents(out)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	return &evs[0], nil
}

// LastAll returns the last event of every subject.
func (c *Client) LastAll(ctx context.Context) ([]Event, error) {
	out, err := c.run(ctx, nil, VerbLastAll)
	if err != nil {
		return nil, err
	}
	return decodeEvents(out)
}

// decodeEvents accepts JSONL, a JSON array, or null.
func decodeEvents(b []byte) ([]Event, error) {
	var evs []Event
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			return evs, nil
		} else if err != nil {
			return nil, fmt.Errorf("hubclient: decode events: %w", err)
		}
		raw = bytes.TrimSpace(raw)
		switch {
		case bytes.Equal(raw, []byte("null")):
		case len(raw) > 0 && raw[0] == '[':
			var arr []Event
			if err := json.Unmarshal(raw, &arr); err != nil {
				return nil, fmt.Errorf("hubclient: decode events: %w", err)
			}
			evs = append(evs, arr...)
		default:
			var ev Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				return nil, fmt.Errorf("hubclient: decode event: %w", err)
			}
			evs = append(evs, ev)
		}
	}
}

// Watcher streams decoded watch lines. Lines closes when the process exits or
// ctx is cancelled; then Wait returns the terminal error (nil on clean exit or
// cancellation).
type Watcher struct {
	Lines <-chan WatchLine

	cmd   *exec.Cmd
	stdin io.WriteCloser
	errb  *bytes.Buffer
	done  chan struct{}
	err   error
}

// Watch starts `watch --topic T [--cursor C] [--seat S] [--ack]`. A negative
// cursor omits --cursor so a non-empty seat resumes from its stored cursor.
func (c *Client) Watch(ctx context.Context, topic string, cursor int64, ack bool, seat string) (*Watcher, error) {
	args := []string{"--topic", topic}
	if cursor >= 0 {
		args = append(args, "--cursor", strconv.FormatInt(cursor, 10))
	}
	if seat != "" {
		args = append(args, "--seat", seat)
	}
	if ack {
		args = append(args, "--ack")
	}
	cmd, err := c.cmd(ctx, VerbWatch, args...)
	if err != nil {
		return nil, err
	}
	w := &Watcher{cmd: cmd, errb: &bytes.Buffer{}, done: make(chan struct{})}
	cmd.Stderr = w.errb
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if ack {
		if w.stdin, err = cmd.StdinPipe(); err != nil {
			return nil, err
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("hubclient: start nexus-hub watch: %w", err)
	}
	lines := make(chan WatchLine)
	w.Lines = lines
	go func() {
		defer close(w.done)
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		var decodeErr error
		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) == 0 {
				continue
			}
			var wl WatchLine
			if err := json.Unmarshal(sc.Bytes(), &wl); err != nil {
				decodeErr = fmt.Errorf("hubclient: decode watch line: %w", err)
				break
			}
			select {
			case lines <- wl:
			case <-ctx.Done():
				decodeErr = ctx.Err()
			}
			if decodeErr != nil {
				break
			}
		}
		if decodeErr != nil {
			_ = cmd.Process.Kill()
		}
		werr := cmd.Wait()
		switch {
		case ctx.Err() != nil:
			w.err = nil
		case decodeErr != nil:
			w.err = decodeErr
		case werr != nil:
			w.err = wrapExit(werr, w.errb.String())
		}
	}()
	return w, nil
}

// Ack tells nexus-hub the consumer accepted events up to seq (watch --ack).
// Wire form: one `{"ack":seq}` line on stdin.
func (w *Watcher) Ack(seq int64) error {
	if w.stdin == nil {
		return errors.New("hubclient: watch started without ack")
	}
	_, err := fmt.Fprintf(w.stdin, "{\"ack\":%d}\n", seq)
	return err
}

// Wait blocks until the stream ends and returns its terminal error.
func (w *Watcher) Wait() error {
	<-w.done
	return w.err
}
