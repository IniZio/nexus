package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/portfwd"
	"github.com/IniZio/nexus3/internal/core/store"
)

func runHerdrFocusWatchCmd(ctx context.Context, args []string, out *Output) error {
	pollEvery := 5 * time.Second
	for len(args) > 0 {
		switch args[0] {
		case "--poll-every":
			if len(args) < 2 {
				return &UsageError{Msg: "herdr focus-watch: --poll-every requires an argument"}
			}
			d, err := time.ParseDuration(args[1])
			if err != nil {
				return &UsageError{Msg: "herdr focus-watch: --poll-every: " + err.Error()}
			}
			pollEvery = d
			args = args[2:]
		default:
			return &UsageError{Msg: "herdr focus-watch: unknown flag: " + args[0]}
		}
	}
	socketPath := os.Getenv("HERDR_SOCKET_PATH")
	session := os.Getenv("HERDR_SESSION")
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "herdr focus-watch: resolve store: " + err.Error(), Err: err}
	}
	return runHerdrFocusWatch(ctx, socketPath, session, storeRoot, portfwd.FocusStatePath(), portfwd.StateDir(), pollEvery, out.w)
}

// runHerdrFocusWatch keeps focus.state current via three sources: snapshot seed at startup,
// events.subscribe stream (reconnects with 1s→10s backoff), and server log tail (250ms poll).
// The snapshot poll writes only when it returns a non-empty id differing from focus.state;
// it never overwrites a log-derived focus with "". Writes focus.state only on ID change.
func runHerdrFocusWatch(ctx context.Context, socketPath, session, storeRoot, statePath, fwdStateDir string, pollEvery time.Duration, w io.Writer) error {
	envSocketPath := socketPath
	if socketPath == "" {
		socketPath = herdrSocketPath(session)
	}

	var lastEventID string
	applyID := func(workspaceID, source string) {
		fmt.Fprintf(os.Stderr, "focus-watch: apply workspace=%q source=%s\n", workspaceID, source)
		herdrFocusChanged(ctx, workspaceID, false, storeRoot, statePath, fwdStateDir, session, socketPath, w) //nolint:errcheck
	}

	if id, err := herdrSnapshotFocusedID(ctx, socketPath); err == nil {
		lastEventID = id
		if id != "" {
			applyID(id, "snapshot")
		}
	}

	evCh := make(chan string, 8)
	logCh := make(chan string, 8)

	go func() {
		backoff := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			err := herdrSubscribeWorkspaceFocused(ctx, socketPath, evCh)
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "focus-watch: reconnect in %s: %v\n", backoff, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if id, rErr := herdrSnapshotFocusedID(ctx, socketPath); rErr == nil && id != "" {
				select {
				case evCh <- id:
				default:
				}
			}
			if backoff < 10*time.Second {
				backoff *= 2
				if backoff > 10*time.Second {
					backoff = 10 * time.Second
				}
			}
		}
	}()

	go tailHerdrServerLog(ctx, herdrServerLogPath(envSocketPath, session), logCh)

	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case id := <-evCh:
			if id != lastEventID {
				lastEventID = id
				applyID(id, "event")
			}
		case id := <-logCh:
			if id != lastEventID {
				lastEventID = id
				applyID(id, "log")
			}
		case <-ticker.C:
			snapID, err := herdrSnapshotFocusedID(ctx, socketPath)
			if err != nil {
				continue
			}
			cur, ok, err := portfwd.ReadFocusState(statePath)
			if err != nil {
				continue
			}
			diskID := ""
			if ok {
				diskID = cur.WorkspaceID
			}
			if snapID != "" && snapID != diskID {
				applyID(snapID, "snapshot")
			}
		}
	}
}

// herdrServerLogPath returns the herdr session server log path, derived from the
// socket directory when HERDR_SOCKET_PATH is set, else from the session name or default.
func herdrServerLogPath(envSocketPath, session string) string {
	if envSocketPath != "" {
		return filepath.Join(filepath.Dir(envSocketPath), "herdr-server.log")
	}
	home, _ := os.UserHomeDir()
	if session != "" {
		return filepath.Join(home, ".config", "herdr", "sessions", session, "herdr-server.log")
	}
	return filepath.Join(home, ".config", "herdr", "herdr-server.log")
}

var (
	reLogFocusEvent  = regexp.MustCompile(`event="workspace\.focus(?:ed)?"`)
	reLogOutcomeOK   = regexp.MustCompile(`outcome="ok"`)
	reLogWorkspaceID = regexp.MustCompile(`workspace_id="([^"]+)"`)
)

// parseFocusLogLine extracts a workspace id from a herdr-server.log structured log line
// that records a successful workspace focus event. Returns "" for non-matching lines.
func parseFocusLogLine(line string) string {
	if !reLogFocusEvent.MatchString(line) {
		return ""
	}
	if !reLogOutcomeOK.MatchString(line) {
		return ""
	}
	m := reLogWorkspaceID.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1]
}

// tailHerdrServerLog poll-reads logPath every 250ms, parses workspace.focus lines,
// and sends matched workspace ids to ch. Starts at EOF (history not replayed).
// Handles rotation (inode change), truncation (fd size < offset), and
// truncation+regrowth to same size (fd mtime newer than last read) by reopening from start.
// Retries every 2s on missing file, logging once.
func tailHerdrServerLog(ctx context.Context, logPath string, ch chan<- string) {
	var (
		f               *os.File
		offset          int64
		pending         []byte
		lastReadTime    time.Time
		loggedMissing   bool
		retryAfter      time.Time
		nextOpenSeekEnd = true
	)

	tryOpen := func(seekEnd bool) bool {
		if f != nil {
			_ = f.Close()
			f = nil
		}
		file, err := os.Open(logPath)
		if err != nil {
			if !loggedMissing {
				fmt.Fprintf(os.Stderr, "focus-watch: server log %s unavailable, retrying\n", logPath)
				loggedMissing = true
			}
			retryAfter = time.Now().Add(2 * time.Second)
			return false
		}
		loggedMissing = false
		if _, stErr := file.Stat(); stErr != nil {
			_ = file.Close()
			retryAfter = time.Now().Add(2 * time.Second)
			return false
		}
		if seekEnd {
			n, _ := file.Seek(0, io.SeekEnd)
			offset = n
			lastReadTime = time.Now()
		} else {
			offset = 0
		}
		pending = pending[:0]
		f = file
		return true
	}

	tryOpen(true)

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	buf := make([]byte, 32*1024)

	for {
		select {
		case <-ctx.Done():
			if f != nil {
				_ = f.Close()
			}
			return
		case <-poll.C:
			if f == nil {
				if time.Now().Before(retryAfter) {
					continue
				}
				if tryOpen(nextOpenSeekEnd) {
					nextOpenSeekEnd = true
				}
				continue
			}
			if fst, ferr := f.Stat(); ferr == nil {
				if pathSt, perr := os.Stat(logPath); perr == nil {
					var pathIno, fdIno uint64
					if sys, ok := pathSt.Sys().(*syscall.Stat_t); ok {
						pathIno = sys.Ino
					}
					if sys, ok := fst.Sys().(*syscall.Stat_t); ok {
						fdIno = sys.Ino
					}
					rotated := pathIno != fdIno || fst.Size() < offset ||
						(offset > 0 && fst.Size() == offset && fst.ModTime().After(lastReadTime))
					if rotated {
						nextOpenSeekEnd = false
						if !tryOpen(false) {
							continue
						}
						nextOpenSeekEnd = true
					}
				}
			}
			if f == nil {
				continue
			}
			for {
				n, readErr := f.Read(buf)
				if n > 0 {
					offset += int64(n)
					lastReadTime = time.Now()
					pending = append(pending, buf[:n]...)
					for {
						idx := bytes.IndexByte(pending, '\n')
						if idx < 0 {
							break
						}
						line := string(pending[:idx])
						pending = pending[idx+1:]
						if id := parseFocusLogLine(line); id != "" {
							select {
							case ch <- id:
							case <-ctx.Done():
								if f != nil {
									_ = f.Close()
								}
								return
							default:
							}
						}
					}
				}
				if readErr != nil {
					break
				}
			}
		}
	}
}

// herdrSnapshotFocusedID issues session.snapshot and returns focused_workspace_id ("" when null).
func herdrSnapshotFocusedID(ctx context.Context, socketPath string) (string, error) {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("focus-watch snapshot: dial: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	req := map[string]any{"id": "fw_snap", "method": "session.snapshot", "params": map[string]any{}}
	data, _ := json.Marshal(req)
	if _, err := fmt.Fprintf(conn, "%s\n", data); err != nil {
		return "", fmt.Errorf("focus-watch snapshot: write: %w", err)
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("focus-watch snapshot: read: %w", err)
		}
		return "", fmt.Errorf("focus-watch snapshot: connection closed before response")
	}
	var resp struct {
		Result struct {
			FocusedWorkspaceID *string `json:"focused_workspace_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("focus-watch snapshot: parse: %w", err)
	}
	if resp.Result.FocusedWorkspaceID == nil {
		return "", nil
	}
	return *resp.Result.FocusedWorkspaceID, nil
}

// herdrSubscribeWorkspaceFocused streams workspace_focused event workspace_ids to ch until EOF or ctx cancel.
func herdrSubscribeWorkspaceFocused(ctx context.Context, socketPath string, ch chan<- string) error {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("focus-watch subscribe: dial: %w", err)
	}
	defer conn.Close()
	req := map[string]any{
		"id":     "fw_sub",
		"method": "events.subscribe",
		"params": map[string]any{"subscriptions": []map[string]any{{"type": "workspace.focused"}}},
	}
	data, _ := json.Marshal(req)
	if _, err := fmt.Fprintf(conn, "%s\n", data); err != nil {
		return fmt.Errorf("focus-watch subscribe: write: %w", err)
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("focus-watch subscribe: ack: %w", err)
		}
		return fmt.Errorf("focus-watch subscribe: connection closed before ack")
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var env struct {
			Event string `json:"event"`
			Data  struct {
				WorkspaceID string `json:"workspace_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue
		}
		if env.Event != "workspace_focused" {
			continue
		}
		select {
		case ch <- env.Data.WorkspaceID:
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("focus-watch subscribe: stream: %w", err)
	}
	return fmt.Errorf("focus-watch subscribe: stream closed (EOF)")
}
