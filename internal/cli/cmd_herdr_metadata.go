package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/portfwd"
	"github.com/IniZio/nexus/internal/core/store"
)

func herdrSocketPath(session string) string {
	home, _ := os.UserHomeDir()
	if session != "" {
		return filepath.Join(home, ".config", "herdr", "sessions", session, "herdr.sock")
	}
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

// reportForwardStatusToSocket sends one workspace.report_metadata JSON-RPC call.
// entries must be sorted by Port and deduplicated; empty slice sets port_forward_status to null.
// Token format: "3000→41234,3001→41235" (guest→host); entries with HostPort 0 render as "3000" (unbound).
func reportForwardStatusToSocket(socketPath, workspaceID string, entries []portfwd.Entry) error {
	var portVal any
	if len(entries) > 0 {
		strs := make([]string, len(entries))
		for i, e := range entries {
			if e.HostPort != 0 {
				strs[i] = fmt.Sprintf("%d→%d", e.Port, e.HostPort)
			} else {
				strs[i] = strconv.Itoa(int(e.Port))
			}
		}
		portVal = strings.Join(strs, ",")
	}

	req := map[string]any{
		"id":     "1",
		"method": "workspace.report_metadata",
		"params": map[string]any{
			"workspace_id": workspaceID,
			"source":       "plugin:nexus",
			"tokens":       map[string]any{"port_forward_status": portVal},
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("report-forward-status: marshal: %w", err)
	}

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("report-forward-status: dial %s: %w", socketPath, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck

	if _, err := fmt.Fprintf(conn, "%s\n", data); err != nil {
		return fmt.Errorf("report-forward-status: write: %w", err)
	}
	scanner := bufio.NewScanner(conn)
	scanner.Scan() //nolint:errcheck
	return nil
}

func loadPortsForSandbox(stateDir, sandboxHandle, sandboxID string) ([]portfwd.Entry, error) {
	merged, err := portfwd.Merge(stateDir, time.Now())
	if err != nil {
		return nil, fmt.Errorf("load ports: merge: %w", err)
	}
	seen := map[uint16]portfwd.Entry{}
	for _, e := range merged.Forwards {
		if e.Sandbox == sandboxHandle || (sandboxID != "" && e.Sandbox == sandboxID) {
			seen[e.Port] = e
		}
	}
	entries := make([]portfwd.Entry, 0, len(seen))
	for _, e := range seen {
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b portfwd.Entry) int {
		if a.Port < b.Port {
			return -1
		}
		if a.Port > b.Port {
			return 1
		}
		return 0
	})
	return entries, nil
}

// herdrReportForwardStatus resolves the sandbox binding for workspaceID, reads its
// current portfwd state, and sends one workspace.report_metadata call.
// socketPath overrides the default (empty = resolve from session).
func herdrReportForwardStatus(
	ctx context.Context,
	workspaceID, session, stateDir, storeRoot, socketPath string,
	w io.Writer,
) error {
	b, err := herdrSpaceResolve(ctx, storeRoot, workspaceID)
	if err != nil {
		return fmt.Errorf("report-forward-status: resolve workspace %s: %w", workspaceID, err)
	}

	entries, err := loadPortsForSandbox(stateDir, b.SandboxHandle, b.SandboxID)
	if err != nil {
		return fmt.Errorf("report-forward-status: %w", err)
	}

	if socketPath == "" {
		socketPath = herdrSocketPath(session)
	}

	if err := reportForwardStatusToSocket(socketPath, workspaceID, entries); err != nil {
		return err
	}

	if len(entries) > 0 {
		strs := make([]string, len(entries))
		for i, e := range entries {
			if e.HostPort != 0 {
				strs[i] = fmt.Sprintf("%d→%d", e.Port, e.HostPort)
			} else {
				strs[i] = strconv.Itoa(int(e.Port))
			}
		}
		fmt.Fprintf(w, "reported: workspace=%s ports=%s\n", workspaceID, strings.Join(strs, ",")) //nolint:errcheck
	} else {
		fmt.Fprintf(w, "reported: workspace=%s ports=null\n", workspaceID) //nolint:errcheck
	}
	return nil
}

func runHerdrReportForwardStatus(ctx context.Context, args []string, out *Output) error {
	var workspaceID, session string

	for len(args) > 0 {
		switch args[0] {
		case "--workspace":
			if len(args) < 2 {
				return &UsageError{Msg: "herdr report-forward-status: --workspace requires an argument"}
			}
			workspaceID = args[1]
			args = args[2:]
		case "--session":
			if len(args) < 2 {
				return &UsageError{Msg: "herdr report-forward-status: --session requires an argument"}
			}
			session = args[1]
			args = args[2:]
		default:
			return &UsageError{Msg: "herdr report-forward-status: unknown flag: " + args[0]}
		}
	}

	if workspaceID == "" {
		return &UsageError{Msg: "herdr report-forward-status: --workspace <id> required"}
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "report-forward-status: resolve store: " + err.Error(), Err: err}
	}

	if session == "" {
		session = os.Getenv("HERDR_SESSION")
	}

	return herdrReportForwardStatus(ctx, workspaceID, session, portfwd.StateDir(), storeRoot, "", out.w)
}
