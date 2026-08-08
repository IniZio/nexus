package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ── Machine-contract error codes ─────────────────────────────────────────────
//
// These codes are a STABLE, versioned part of the CLI machine contract.
// External callers (scripts, automation, other services) are expected to
// branch on these values programmatically. They MUST NOT be renamed or
// repurposed without bumping schemaVersion.
//
// Full code table (schema_version 1):
//
//	unknown_command      — the top-level command name is not registered
//	usage_error          — the invocation is syntactically wrong (missing args, bad flags)
//	invalid_argument     — a positional argument has an invalid format (e.g. malformed handle)
//	sandbox_not_found    — the sandbox reference did not match any known sandbox
//	ambiguous_ref        — an ID prefix matched more than one sandbox; message names candidates
//	sandbox_already_exists — create was called with a handle that already exists
//	illegal_transition   — the requested lifecycle verb is not valid from the sandbox's current state
//	no_substrate         — the operation requires a hypervisor driver that is not compiled in
//	no_guest_image       — start requires a guest kernel/image that is not yet available; the guest image pipeline is not yet implemented
//	sandbox_locked       — (retired; never emitted. Reserved, do not reuse.)
//	internal_error       — unexpected failure with no more-specific code; indicates a bug
const (
	ErrCodeUnknownCommand  = "unknown_command"
	ErrCodeUsageError      = "usage_error"
	ErrCodeInvalidArgument = "invalid_argument"
	ErrCodeInternalError   = "internal_error"
)

const schemaVersion = 1

// successEnvelope is the JSON form of a successful response.
type successEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Data          any    `json:"data"`
}

// errorEnvelope is the JSON form of an error response.
type errorEnvelope struct {
	SchemaVersion int      `json:"schema_version"`
	Kind          string   `json:"kind"`
	Error         apiError `json:"error"`
}

// apiError carries the stable error code and a human-readable message.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Output dispatches CLI responses to the appropriate sink. In JSON mode all
// output is machine-readable JSON written to stdout; nothing else may be
// written to stdout. In human mode, success goes to stdout and errors go to
// stderr.
type Output struct {
	w    io.Writer // stdout
	errW io.Writer // stderr
	json bool
}

// NewOutput creates an Output. w receives stdout output; errW receives stderr
// output; jsonMode selects the machine-readable envelope format.
func NewOutput(w, errW io.Writer, jsonMode bool) *Output {
	return &Output{w: w, errW: errW, json: jsonMode}
}

// IsJSON reports whether JSON mode is active.
func (o *Output) IsJSON() bool { return o.json }

// EmitSuccess writes a successful response. In JSON mode it writes the
// schema-versioned envelope to stdout. In human mode it writes msg to stdout.
func (o *Output) EmitSuccess(kind string, data any, msg string) {
	if o.json {
		enc := json.NewEncoder(o.w)
		_ = enc.Encode(successEnvelope{
			SchemaVersion: schemaVersion,
			Kind:          kind,
			Data:          data,
		})
		return
	}
	fmt.Fprintln(o.w, msg)
}

// EmitError writes an error response. In JSON mode it writes the
// schema-versioned error envelope to stdout. In human mode it writes to
// stderr.
func (o *Output) EmitError(code, message string) {
	if o.json {
		enc := json.NewEncoder(o.w)
		_ = enc.Encode(errorEnvelope{
			SchemaVersion: schemaVersion,
			Kind:          "error",
			Error:         apiError{Code: code, Message: message},
		})
		return
	}
	fmt.Fprintln(o.errW, "error: "+message)
}

// UsageError is returned by a command's Run function to signal a usage error
// (exit code 2), optionally with a message. Code overrides the emitted error
// code; when empty it defaults to ErrCodeUsageError.
type UsageError struct {
	Code string // stable error code override; empty → ErrCodeUsageError
	Msg  string
}

func (e *UsageError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return "usage error"
}

// CodedError wraps an error with a stable machine-contract error code. Commands
// return *CodedError so that root.go can emit the exact code in the error
// envelope instead of hardcoding internal_error.
//
// The Err field is the underlying cause and participates in the errors.Is /
// errors.As chain via Unwrap. Msg carries the full human-readable message
// (including any contextual prefix such as "sandbox rm: ...").
type CodedError struct {
	Code string
	Msg  string
	Err  error // underlying cause; may be nil
}

func (e *CodedError) Error() string { return e.Msg }
func (e *CodedError) Unwrap() error { return e.Err }

// codeOf extracts the stable error code from err. If the error chain contains
// a *CodedError, that code is returned. Otherwise it falls back to
// ErrCodeInternalError — the correct behaviour for unexpected failures.
func codeOf(err error) string {
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ErrCodeInternalError
}

// ExitCodeError is returned by exec/attach commands when the guest process
// exits with a non-zero code. root.go converts this to the exact exit code
// rather than the generic exit code 1.
type ExitCodeError struct {
	Code int32
}

func (e *ExitCodeError) Error() string {
	return fmt.Sprintf("process exited with code %d", e.Code)
}
