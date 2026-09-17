package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/IniZio/nexus/internal/core/portfwd"
	"github.com/IniZio/nexus/internal/core/store"
)

func runHerdrFocusChanged(ctx context.Context, args []string, out *Output) error {
	var workspaceID string
	onlyIfFocused := false

	for len(args) > 0 {
		switch args[0] {
		case "--workspace":
			if len(args) < 2 {
				return &UsageError{Msg: "herdr focus-changed: --workspace requires an argument"}
			}
			workspaceID = args[1]
			args = args[2:]
		case "--only-if-focused":
			onlyIfFocused = true
			args = args[1:]
		default:
			return &UsageError{Msg: "herdr focus-changed: unknown flag: " + args[0]}
		}
	}

	if workspaceID == "" {
		return &UsageError{Msg: "herdr focus-changed: --workspace <id> required"}
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "herdr focus-changed: resolve store: " + err.Error(), Err: err}
	}

	return herdrFocusChanged(ctx, workspaceID, onlyIfFocused, storeRoot,
		portfwd.FocusStatePath(), portfwd.StateDir(), os.Getenv("HERDR_SESSION"), "", out.w)
}

func herdrFocusChanged(ctx context.Context, workspaceID string, onlyIfFocused bool, storeRoot, statePath, fwdStateDir, session, socketPath string, w io.Writer) error {
	if onlyIfFocused {
		current, ok, err := portfwd.ReadFocusState(statePath)
		if err != nil || !ok || current.WorkspaceID != workspaceID {
			return nil
		}
	}

	var sandboxID string
	if b, err := herdrSpaceResolve(ctx, storeRoot, workspaceID); err == nil {
		sandboxID = b.SandboxID
	}

	s := portfwd.FocusState{
		WorkspaceID: workspaceID,
		SandboxID:   sandboxID,
		Session:     session,
		UpdatedAt:   time.Now().UTC(),
	}
	if err := portfwd.WriteFocusState(statePath, s); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "herdr focus-changed: write: " + err.Error(), Err: err}
	}
	fmt.Fprintf(w, "focus-changed: workspace=%s sandbox=%s\n", workspaceID, sandboxID)
	rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = herdrReportForwardStatus(rctx, workspaceID, session, fwdStateDir, storeRoot, socketPath, io.Discard)
	return nil
}
