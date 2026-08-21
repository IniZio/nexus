package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func init() {
	Register(Command{
		Name:    "log",
		Summary: "Print a sandbox's supervisor log",
		Run:     runLog,
	})
}

// logErrCodeNotFound is returned when a sandbox exists but has no supervisor
// log yet (e.g. it was created but never started). It sits alongside the
// sandbox_* codes in cmd_sandbox.go: not part of that const block because it
// is not a sandboxCodeFor mapping, but the same stable-code contract applies.
const logErrCodeNotFound = "log_not_found"

// logLinesJSON is the --json data payload for a completed (non-follow) log
// read. Lines is always non-nil so JSON encodes it as [] rather than null.
type logLinesJSON struct {
	SandboxID       string   `json:"sandbox_id"`
	Lines           []string `json:"lines"`
	SupervisorError string   `json:"supervisor_error,omitempty"`
}

// runLog is the registered Run function for "nexus3 log <ref> [-n N | --tail N] [-f | --follow]".
//
// The ref is documented (and expected, per the proof-it-works invocation
// "nexus3 log loop/log-cmd -n 20") to come BEFORE the flags. Go's flag.Parse
// stops consuming at the first non-flag token, so a naive fs.Parse(args)
// would treat "-n" and "20" as stray positionals once it hit the ref first.
// logArgvFlagsFirst reorders args so every flag (and, for value-taking
// flags, its value) moves ahead of the bare positional before Parse runs.
func runLog(ctx context.Context, args []string, out *Output) error {
	flagArgs, positional := logArgvFlagsFirst(args)

	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	var (
		nFlag      = fs.Int("n", 0, "print only the last N lines")
		tailFlag   = fs.Int("tail", 0, "print only the last N lines (alias of -n)")
		fFlag      = fs.Bool("f", false, "stream appended lines until interrupted")
		followFlag = fs.Bool("follow", false, "stream appended lines until interrupted (alias of -f)")
	)
	if err := fs.Parse(flagArgs); err != nil {
		return &UsageError{Msg: "log: " + err.Error()}
	}

	if len(positional) != 1 {
		return &UsageError{Msg: "log: usage: log <ref> [-n N | --tail N] [-f | --follow]"}
	}
	ref := positional[0]

	tailN := *tailFlag
	if *nFlag != 0 {
		tailN = *nFlag
	}
	follow := *fFlag || *followFlag

	if follow && out.IsJSON() {
		return &UsageError{Msg: "log: --follow is not supported with --json"}
	}

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "log: " + err.Error(), Err: err}
	}

	return runLogWithSvc(ctx, ref, tailN, follow, out, svc)
}

// logArgvValueFlags are the log-verb flags that consume the following token
// as their value, keyed by every spelling flag.FlagSet accepts for them
// (single- and double-dash).
var logArgvValueFlags = map[string]bool{
	"-n": true, "--n": true, "-tail": true, "--tail": true,
}

// logArgvFlagsFirst splits args into (flags, positionals), preserving order
// within each group and moving a value-taking flag's value token along with
// it. "--" ends flag scanning; everything after it is positional.
func logArgvFlagsFirst(args []string) (flagArgs, positional []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flagArgs = append(flagArgs, a)
			if !strings.Contains(a, "=") && logArgvValueFlags[a] && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return flagArgs, positional
}

// runLogWithSvc resolves ref, locates its supervisor log via the established
// ResolveRef -> store.DefaultRoot -> supervisor.DefaultStateDir idiom (see
// cmd_sandbox.go:931-933 and cmd_herdr_plugin.go:865 for the same three
// steps), and prints or streams it. Extracted for testability — callers pass
// a pre-built service.
func runLogWithSvc(ctx context.Context, ref string, tailN int, follow bool, out *Output, svc *service.Service) error {
	sb, err := svc.ResolveRef(ctx, ref)
	if err != nil {
		return errSandbox("log", err)
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "log: resolve state directory: " + err.Error(), Err: err}
	}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	logPath := filepath.Join(stateDir, "supervisor.log")
	errPath := filepath.Join(stateDir, "supervisor.err")

	info, statErr := os.Stat(logPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return &CodedError{
				Code: logErrCodeNotFound,
				Msg:  "log: " + noLogYetMessage(sb.Handle(), errPath),
			}
		}
		return &CodedError{Code: ErrCodeInternalError, Msg: "log: stat supervisor log: " + statErr.Error(), Err: statErr}
	}

	// An empty log carries no lines to show. Surface the sibling
	// supervisor.err failure reason when present, since that is very likely
	// why the log has nothing in it yet.
	if info.Size() == 0 && !follow {
		reason := readSupervisorErr(errPath)
		if out.IsJSON() {
			out.EmitSuccess("log.lines", logLinesJSON{
				SandboxID:       sb.ID.String(),
				Lines:           []string{},
				SupervisorError: reason,
			}, "")
			return nil
		}
		if reason != "" {
			fmt.Fprintf(out.Stdout(), "(empty log) supervisor failure reason: %s\n", reason)
		}
		return nil
	}

	if follow {
		return streamLogFollow(ctx, logPath, tailN, out)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "log: read supervisor log: " + err.Error(), Err: err}
	}

	if out.IsJSON() {
		lines := tailLines(splitLogLines(data), tailN)
		out.EmitSuccess("log.lines", logLinesJSON{SandboxID: sb.ID.String(), Lines: lines}, "")
		return nil
	}

	w := out.Stdout()
	if tailN <= 0 {
		// Whole-log human output streams the raw bytes verbatim.
		_, err := w.Write(data)
		return err
	}
	for _, line := range tailLines(splitLogLines(data), tailN) {
		fmt.Fprintln(w, line)
	}
	return nil
}

// noLogYetMessage builds the "log file missing" error message, folding in the
// supervisor's recorded failure reason (supervisor.err) when one exists.
func noLogYetMessage(handle, errPath string) string {
	reason := readSupervisorErr(errPath)
	if reason != "" {
		return fmt.Sprintf("no supervisor log yet for %s; supervisor failure reason: %s", handle, reason)
	}
	return fmt.Sprintf("no supervisor log yet for %s (sandbox may not have been started)", handle)
}

// readSupervisorErr returns the trimmed contents of the sibling supervisor.err
// file, or "" if it does not exist, is unreadable, or is empty.
func readSupervisorErr(errPath string) string {
	data, err := os.ReadFile(errPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// splitLogLines splits raw log bytes into lines, dropping a single trailing
// newline so a well-formed log file does not produce a spurious empty final
// element.
func splitLogLines(data []byte) []string {
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// tailLines returns the last n lines of lines, or all of them when n <= 0.
func tailLines(lines []string, n int) []string {
	if n <= 0 || len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// streamLogFollow prints the log (whole, or its last tailN lines) and then
// polls for appended bytes, writing them raw to out.Stdout() until ctx is
// cancelled (SIGINT/SIGTERM, wired by root.go's signal.NotifyContext).
func streamLogFollow(ctx context.Context, logPath string, tailN int, out *Output) error {
	f, err := os.Open(logPath)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "log: open supervisor log: " + err.Error(), Err: err}
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "log: read supervisor log: " + err.Error(), Err: err}
	}

	w := out.Stdout()
	if tailN <= 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
	} else {
		for _, line := range tailLines(splitLogLines(data), tailN) {
			fmt.Fprintln(w, line)
		}
	}

	offset := int64(len(data))
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			info, statErr := os.Stat(logPath)
			if statErr != nil || info.Size() <= offset {
				continue
			}
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				continue
			}
			buf := make([]byte, info.Size()-offset)
			n, _ := io.ReadFull(f, buf)
			if n > 0 {
				if _, err := w.Write(buf[:n]); err != nil {
					return err
				}
				offset += int64(n)
			}
		}
	}
}
