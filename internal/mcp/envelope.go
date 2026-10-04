package mcp

import (
	"encoding/json"
	"errors"

	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Response is the uniform MCP tool response envelope.
// All tools return exactly one TextContent block whose text is a
// JSON-serialised Response.
//
// ok=true  → data carries the result; truncated is nil when output is complete.
// ok=false → error carries the failure; data is nil.
type Response struct {
	OK        bool       `json:"ok"`
	Data      any        `json:"data,omitempty"`
	Error     *ErrorInfo `json:"error,omitempty"`
	Truncated *Truncated `json:"truncated,omitempty"`
}

// Truncated describes output that was capped before delivery.
// BytesOmitted = TotalBytes - len(serialised data).
type Truncated struct {
	BytesOmitted int64 `json:"bytes_omitted"`
	TotalBytes   int64 `json:"total_bytes"`
}

// ErrorInfo carries a stable code and human-readable message for tool errors.
type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// successResult marshals Response{OK: true, Data: data} into a TextContent block.
// Panics if marshal fails (struct with no cycles cannot fail).
func successResult(data any) *gosdk.CallToolResult {
	r := Response{OK: true, Data: data}
	b, err := json.Marshal(r)
	if err != nil {
		panic("mcp: successResult: marshal failed: " + err.Error())
	}
	return &gosdk.CallToolResult{
		Content: []gosdk.Content{&gosdk.TextContent{Text: string(b)}},
	}
}

// errorResult returns a CallToolResult with IsError: true so MCP clients
// recognise tool errors. The envelope carries ok=false and the error message.
func errorResult(err error) *gosdk.CallToolResult {
	return codedErrorResult("error", err)
}

// hibernateErrorResult is errorResult with the hibernate/resume error codes.
func hibernateErrorResult(err error) *gosdk.CallToolResult {
	var illegalT *lifecycle.IllegalTransitionError
	code := "error"
	switch {
	case errors.Is(err, service.ErrHibernateUnsupported):
		code = "hibernate_unsupported"
	case errors.Is(err, service.ErrHibernateRefused):
		code = "hibernate_refused"
	case errors.Is(err, service.ErrSnapshotFailed):
		code = "snapshot_failed"
	case errors.As(err, &illegalT):
		code = "illegal_transition"
	}
	return codedErrorResult(code, err)
}

func codedErrorResult(code string, err error) *gosdk.CallToolResult {
	r := Response{OK: false, Error: &ErrorInfo{Code: code, Message: err.Error()}}
	b, _ := json.Marshal(r)
	return &gosdk.CallToolResult{
		IsError: true,
		Content: []gosdk.Content{&gosdk.TextContent{Text: string(b)}},
	}
}

// successWithTruncation marshals Response{OK: true, Data: data, Truncated: &t}.
func successWithTruncation(data any, t Truncated) *gosdk.CallToolResult {
	r := Response{OK: true, Data: data, Truncated: &t}
	b, err := json.Marshal(r)
	if err != nil {
		panic("mcp: successWithTruncation: marshal failed: " + err.Error())
	}
	return &gosdk.CallToolResult{
		Content: []gosdk.Content{&gosdk.TextContent{Text: string(b)}},
	}
}
