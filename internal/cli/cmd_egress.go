package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func init() {
	Register(Command{
		Name:    "egress",
		Summary: "Egress perimeter management (subcommands: allow, log)",
		Run:     runEgress,
	})
}

// runEgress dispatches egress subcommands.
func runEgress(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "egress: usage: egress <subcommand> [args]\nSubcommands: allow, log"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "allow":
		return runEgressAllow(ctx, rest, out)
	case "log":
		return runEgressLog(ctx, rest, out)
	default:
		return &UsageError{Msg: fmt.Sprintf("egress: unknown subcommand %q; available: allow, log", sub)}
	}
}

// runEgressLog parses flags for "egress log <sandbox> [--follow]".
func runEgressLog(ctx context.Context, args []string, out *Output) error {
	var follow bool
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--follow" || arg == "-f":
			follow = true
		case arg == "--":
			positionals = append(positionals, args[i+1:]...)
			i = len(args)
		case len(arg) > 1 && arg[0] == '-':
			return &UsageError{Msg: fmt.Sprintf("egress log: unknown flag %q", arg)}
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return &UsageError{Msg: "egress log: usage: egress log <sandbox> [--follow]"}
	}
	ref := positionals[0]

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "egress log: " + err.Error(), Err: err}
	}
	return runEgressLogWith(ctx, ref, follow, out, svc)
}

// runEgressAllow is the top-level entry for "egress allow <sandbox> <host>".
func runEgressAllow(ctx context.Context, args []string, out *Output) error {
	if len(args) != 2 {
		return &UsageError{Msg: "egress allow: usage: egress allow <sandbox> <host>"}
	}
	ref, host := args[0], args[1]
	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "egress allow: " + err.Error(), Err: err}
	}
	return runEgressAllowWith(ctx, ref, host, out, svc, supervisor.RequestEgressAllow)
}

// egressAllowSockNotFoundCode is returned when the sandbox has no live supervisor.
const egressAllowSockNotFoundCode = "egress_allow_no_supervisor"

// runEgressAllowWith is the testable entry for "egress allow". It accepts an
// injected requestEgressAllow so tests can replace the real IPC call.
//
// Persistence mechanism (Case 1 — seeded from persisted Envelope.AllowedHosts):
// service.go:startSupervisor seeds netfilter.NewAllowList from sb.Envelope.AllowedHosts
// on every supervisor start (service.go:792). Appending the host here means a
// supervisor bounce re-admits it. A full sandbox recreate rebuilds the Envelope
// from base-ref, so the runtime host is dropped automatically.
func runEgressAllowWith(
	ctx context.Context,
	ref, host string,
	out *Output,
	svc *service.Service,
	requestEgressAllow func(context.Context, string, string) error,
) error {
	if host == "" {
		return &UsageError{Msg: "egress allow: host must not be empty"}
	}

	sb, err := svc.ResolveRef(ctx, ref)
	if err != nil {
		return errSandbox("egress allow", err)
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "egress allow: resolve state directory: " + err.Error(), Err: err}
	}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	sockPath := supervisor.SockPath(stateDir)

	// AC-D4 / T6-AC3: if the supervisor socket is absent the sandbox is not
	// running. Return a clear error without touching the store.
	if _, statErr := os.Stat(sockPath); statErr != nil {
		return &CodedError{
			Code: egressAllowSockNotFoundCode,
			Msg:  fmt.Sprintf("egress allow: sandbox %s has no live supervisor (is it running?)", sb.ID),
		}
	}

	// Mutate the live perimeter via the supervisor IPC verb (T5).
	if ipcErr := requestEgressAllow(ctx, sockPath, host); ipcErr != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "egress allow: ipc: " + ipcErr.Error(), Err: ipcErr}
	}

	// Persist: append host to AllowedHosts (dedup) so a supervisor bounce
	// re-seeds the netfilter allowlist with this host included.
	st, stErr := store.NewFileStore(storeRoot)
	if stErr != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "egress allow: open store: " + stErr.Error(), Err: stErr}
	}
	if persistErr := st.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		for _, h := range rec.Envelope.AllowedHosts {
			if h == host {
				return nil // already present; idempotent
			}
		}
		rec.Envelope.AllowedHosts = append(rec.Envelope.AllowedHosts, host)
		return nil
	}); persistErr != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "egress allow: persist: " + persistErr.Error(), Err: persistErr}
	}

	out.EmitSuccess("egress.allow",
		map[string]string{"sandbox": sb.ID.String(), "host": host},
		fmt.Sprintf("host %q admitted to sandbox %s", host, sb.ID))
	return nil
}

const egressDecisionsNotFoundCode = "egress_decisions_not_found"

// runEgressLogWith is the testable entry point; callers pass a pre-built svc.
func runEgressLogWith(ctx context.Context, ref string, follow bool, out *Output, svc *service.Service) error {
	sb, err := svc.ResolveRef(ctx, ref)
	if err != nil {
		return errSandbox("egress log", err)
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "egress log: resolve state directory: " + err.Error(), Err: err}
	}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	decisionsPath := filepath.Join(stateDir, "egress-decisions.jsonl")

	if follow {
		return streamEgressFollow(ctx, decisionsPath, out)
	}
	return printEgressDecisions(decisionsPath, out)
}

// printEgressDecisions reads all records from decisionsPath and prints them.
func printEgressDecisions(decisionsPath string, out *Output) error {
	f, err := os.Open(decisionsPath)
	if os.IsNotExist(err) {
		return &CodedError{
			Code: egressDecisionsNotFoundCode,
			Msg:  "egress log: no egress decisions yet (sandbox may not be running with egress configured)",
		}
	}
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "egress log: open decisions log: " + err.Error(), Err: err}
	}
	defer f.Close()
	return decodeAndPrintEgressEvents(f, out)
}

// decodeAndPrintEgressEvents decodes JSON-line EgressEvent records from r and
// prints each one. Malformed lines are skipped.
func decodeAndPrintEgressEvents(r io.Reader, out *Output) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev perimeter.EgressEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip malformed lines
		}
		printEgressEvent(ev, out)
	}
	return scanner.Err()
}

// printEgressEvent writes one egress event to out.
func printEgressEvent(ev perimeter.EgressEvent, out *Output) {
	if out.IsJSON() {
		out.EmitSuccess("egress.decision", ev, "")
		return
	}
	verdict := ev.Verdict.String()
	fmt.Fprintf(out.Stdout(), "%s  %-5s  %s  %s\n",
		ev.Timestamp.Format(time.RFC3339),
		strings.ToUpper(verdict),
		ev.Host,
		ev.Reason,
	)
}

// streamEgressFollow prints existing records then polls for new ones until ctx
// is cancelled (SIGINT/SIGTERM, wired by root.go's signal.NotifyContext).
func streamEgressFollow(ctx context.Context, decisionsPath string, out *Output) error {
	f, openErr := os.Open(decisionsPath)
	if openErr != nil && !os.IsNotExist(openErr) {
		return &CodedError{Code: ErrCodeInternalError, Msg: "egress log: open decisions log: " + openErr.Error(), Err: openErr}
	}

	var offset int64
	if f != nil {
		defer f.Close()
		if err := decodeAndPrintEgressEvents(f, out); err != nil {
			return err
		}
		cur, _ := f.Seek(0, io.SeekCurrent)
		offset = cur
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// If file did not exist at start, try opening it now.
			if f == nil {
				f2, err2 := os.Open(decisionsPath)
				if err2 != nil {
					continue
				}
				f = f2
				defer f.Close() //nolint:gocritic // intentional: deferred at outer scope
			}
			info, statErr := os.Stat(decisionsPath)
			if statErr != nil || info.Size() <= offset {
				continue
			}
			if _, seekErr := f.Seek(offset, io.SeekStart); seekErr != nil {
				continue
			}
			buf := make([]byte, info.Size()-offset)
			n, _ := io.ReadFull(f, buf)
			if n > 0 {
				if err := decodeAndPrintEgressEvents(bytes.NewReader(buf[:n]), out); err != nil {
					return err
				}
				offset += int64(n)
			}
		}
	}
}
