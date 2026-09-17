package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
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

// runHerdrFocusWatch keeps focus.state current: seeds from session.snapshot,
// streams workspace_focused events (reconnects with 1s→10s backoff), and polls
// session.snapshot every pollEvery; writes focus.state only on ID change (mtime stable on no-op).
func runHerdrFocusWatch(ctx context.Context, socketPath, session, storeRoot, statePath, fwdStateDir string, pollEvery time.Duration, w io.Writer) error {
	if socketPath == "" {
		socketPath = herdrSocketPath(session)
	}
	var lastEventID string // dedup: skip consecutive equal event-stream IDs
	applyID := func(workspaceID string) {
		herdrFocusChanged(ctx, workspaceID, false, storeRoot, statePath, fwdStateDir, session, socketPath, w) //nolint:errcheck
	}
	if id, err := herdrSnapshotFocusedID(ctx, socketPath); err == nil {
		lastEventID = id
		if id != "" {
			applyID(id)
		}
	}
	evCh := make(chan string, 8)
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
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case id := <-evCh:
			if id != lastEventID {
				lastEventID = id
				applyID(id)
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
			if snapID != diskID {
				applyID(snapID)
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
