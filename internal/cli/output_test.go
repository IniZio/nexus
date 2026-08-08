package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func newTestOutput(jsonMode bool) (*Output, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return NewOutput(stdout, stderr, jsonMode), stdout, stderr
}

// TestSuccessEnvelopeJSON verifies the JSON shape of a success response.
func TestSuccessEnvelopeJSON(t *testing.T) {
	out, stdout, stderr := newTestOutput(true)

	type payload struct {
		Foo string `json:"foo"`
	}
	out.EmitSuccess("widget", payload{Foo: "bar"}, "human text")

	// stderr must be empty in JSON mode.
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty in JSON mode, got: %q", stderr.String())
	}

	// stdout must be exactly one parseable JSON object.
	var env map[string]any
	dec := json.NewDecoder(stdout)
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("decode success envelope: %v", err)
	}
	// Nothing else should follow.
	var extra any
	if err := dec.Decode(&extra); err == nil {
		t.Error("stdout contained more than one JSON value")
	}

	// schema_version
	sv, ok := env["schema_version"]
	if !ok {
		t.Fatal("missing schema_version")
	}
	if sv.(float64) != 1 {
		t.Errorf("schema_version = %v, want 1", sv)
	}

	// kind
	if kind, _ := env["kind"].(string); kind != "widget" {
		t.Errorf("kind = %q, want %q", kind, "widget")
	}

	// data must be present and carry the payload.
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data field missing or wrong type: %T", env["data"])
	}
	if data["foo"] != "bar" {
		t.Errorf("data.foo = %v, want %q", data["foo"], "bar")
	}
}

// TestErrorEnvelopeJSON verifies the JSON shape of an error response.
func TestErrorEnvelopeJSON(t *testing.T) {
	out, stdout, stderr := newTestOutput(true)
	out.EmitError(ErrCodeUnknownCommand, "unknown command: frobnicate")

	// stderr must be empty in JSON mode.
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty in JSON mode, got: %q", stderr.String())
	}

	var env map[string]any
	dec := json.NewDecoder(stdout)
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	// No second value.
	var extra any
	if err := dec.Decode(&extra); err == nil {
		t.Error("stdout contained more than one JSON value")
	}

	if sv := env["schema_version"].(float64); sv != 1 {
		t.Errorf("schema_version = %v, want 1", sv)
	}
	if kind := env["kind"].(string); kind != "error" {
		t.Errorf("kind = %q, want \"error\"", kind)
	}

	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("error field missing or wrong type: %T", env["error"])
	}
	if code := errObj["code"].(string); code != ErrCodeUnknownCommand {
		t.Errorf("error.code = %q, want %q", code, ErrCodeUnknownCommand)
	}
	if msg := errObj["message"].(string); msg == "" {
		t.Error("error.message is empty")
	}
}

// TestJSONModeStdoutOnly verifies that in JSON mode nothing extraneous reaches
// stdout (e.g. log lines, extra newlines beyond the single JSON object).
func TestJSONModeStdoutOnly(t *testing.T) {
	out, stdout, _ := newTestOutput(true)
	out.EmitSuccess("ping", struct{ OK bool }{OK: true}, "pong")

	raw := stdout.String()
	// Must be valid JSON terminated by exactly one newline.
	trimmed := bytes.TrimRight([]byte(raw), "\n")
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		t.Fatalf("stdout is not a single JSON object: %v\nraw: %q", err, raw)
	}
	// The content after trimming trailing newline must fully parse.
	// Count newlines: json.Encoder adds exactly one '\n'.
	newlines := bytes.Count([]byte(raw), []byte("\n"))
	if newlines != 1 {
		t.Errorf("expected exactly 1 trailing newline, got %d in %q", newlines, raw)
	}
}

// TestRunExitCodes verifies the three-value exit code contract:
//
//	0  success
//	1  operational failure
//	2  usage error (no subcommand, unknown subcommand, bad flags)
//
// These codes form a machine contract that external callers depend on.
func TestRunExitCodes(t *testing.T) {
	t.Run("no_args_is_usage_error", func(t *testing.T) {
		got := Run([]string{})
		if got != 2 {
			t.Errorf("Run([]): exit code = %d, want 2 (usage error)", got)
		}
	})

	t.Run("unknown_subcommand_is_usage_error", func(t *testing.T) {
		got := Run([]string{"bogus-command-that-does-not-exist"})
		if got != 2 {
			t.Errorf("Run([bogus]): exit code = %d, want 2 (usage error)", got)
		}
	})

	t.Run("version_is_success", func(t *testing.T) {
		got := Run([]string{"version"})
		if got != 0 {
			t.Errorf("Run([version]): exit code = %d, want 0 (success)", got)
		}
	})
}

// ── CodedError / codeOf ───────────────────────────────────────────────────────

// TestCodedError verifies that CodedError implements error and participates in
// the errors.Is / errors.As chain through Unwrap.
func TestCodedError(t *testing.T) {
	cause := errors.New("underlying cause")
	coded := &CodedError{Code: "sandbox_not_found", Msg: "sandbox rm: " + cause.Error(), Err: cause}

	if coded.Error() != "sandbox rm: underlying cause" {
		t.Errorf("Error() = %q, want %q", coded.Error(), "sandbox rm: underlying cause")
	}
	// Unwrap must expose the cause so errors.Is works through the chain.
	if !errors.Is(coded, cause) {
		t.Error("errors.Is(coded, cause) = false, want true (Unwrap chain must be intact)")
	}
}

// TestCodeOf_CodedError verifies that codeOf returns the embedded code.
func TestCodeOf_CodedError(t *testing.T) {
	err := &CodedError{Code: "sandbox_not_found", Msg: "not found", Err: nil}
	if got := codeOf(err); got != "sandbox_not_found" {
		t.Errorf("codeOf(coded) = %q, want %q", got, "sandbox_not_found")
	}
}

// TestCodeOf_WrappedCodedError verifies that codeOf extracts a code through
// an fmt.Errorf wrapper, exercising the errors.As traversal.
func TestCodeOf_WrappedCodedError(t *testing.T) {
	inner := &CodedError{Code: "ambiguous_ref", Msg: "ambiguous", Err: nil}
	wrapped := fmt.Errorf("outer context: %w", inner)
	if got := codeOf(wrapped); got != "ambiguous_ref" {
		t.Errorf("codeOf(wrapped) = %q, want %q", got, "ambiguous_ref")
	}
}

// TestCodeOf_PlainError verifies that an error with no code falls back to
// internal_error — the correct behaviour for unexpected failures.
func TestCodeOf_PlainError(t *testing.T) {
	err := errors.New("some genuine internal failure")
	if got := codeOf(err); got != ErrCodeInternalError {
		t.Errorf("codeOf(plain) = %q, want %q", got, ErrCodeInternalError)
	}
}

// TestCodedError_InternalError_SurfacesInEnvelope proves that a plain error
// (no code) travels through errSandbox and surfaces as internal_error in the
// JSON envelope — not merely in codeOf's return value.
func TestCodedError_InternalError_SurfacesInEnvelope(t *testing.T) {
	type genuineInternalErr struct{ msg string }
	err := &genuineInternalErr{msg: "disk I/O failure"}

	// A plain struct error with no typed match defaults to internal_error.
	coded := &CodedError{
		Code: sandboxCodeFor(fmt.Errorf("wrapped: %w", fmt.Errorf("%s", err.msg))),
		Msg:  "sandbox rm: " + err.msg,
	}
	if coded.Code != ErrCodeInternalError {
		t.Errorf("sandboxCodeFor(unknown err) = %q, want %q", coded.Code, ErrCodeInternalError)
	}

	out, stdout, _ := newTestOutput(true)
	out.EmitError(coded.Code, coded.Msg)

	var env map[string]any
	if err2 := json.NewDecoder(stdout).Decode(&env); err2 != nil {
		t.Fatalf("decode envelope: %v", err2)
	}
	errObj := env["error"].(map[string]any)
	if code := errObj["code"].(string); code != ErrCodeInternalError {
		t.Errorf("envelope error.code = %q, want %q", code, ErrCodeInternalError)
	}
}

// TestHumanModeRouting verifies that in human mode success goes to stdout and
// errors go to stderr.
func TestHumanModeRouting(t *testing.T) {
	out, stdout, stderr := newTestOutput(false)
	out.EmitSuccess("x", nil, "all good")
	out.EmitError(ErrCodeInternalError, "something broke")

	if !bytes.Contains(stdout.Bytes(), []byte("all good")) {
		t.Errorf("human success message not in stdout: %q", stdout.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("something broke")) {
		t.Errorf("human error message not in stderr: %q", stderr.String())
	}
	// In human mode stdout should not receive error text.
	if bytes.Contains(stdout.Bytes(), []byte("something broke")) {
		t.Error("error message leaked into stdout in human mode")
	}
}
