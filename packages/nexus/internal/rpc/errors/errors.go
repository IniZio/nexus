package errors

import "fmt"

const (
	CodeNotFound      = 404
	CodeInvalidParams = 400
	CodeInternal      = 500
)

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

func NotFound(msg string) *RPCError {
	return &RPCError{Code: CodeNotFound, Message: msg}
}

func InvalidParams(msg string) *RPCError {
	return &RPCError{Code: CodeInvalidParams, Message: msg}
}

func Internal(msg string) *RPCError {
	return &RPCError{Code: CodeInternal, Message: msg}
}
