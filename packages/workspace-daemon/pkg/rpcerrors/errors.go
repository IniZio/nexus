package errors

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var (
	ErrInvalidToken      = &RPCError{Code: -32001, Message: "Invalid authentication token"}
	ErrUnauthorized      = &RPCError{Code: -32002, Message: "Unauthorized"}
	ErrAuthRelayInvalid  = &RPCError{Code: -32008, Message: "Invalid auth relay token"}
	ErrAuthBindingAbsent = &RPCError{Code: -32009, Message: "Auth binding not found"}
	ErrMethodNotFound    = &RPCError{Code: -32601, Message: "Method not found"}
	ErrInvalidParams     = &RPCError{Code: -32602, Message: "Invalid params"}
	ErrInternalError     = &RPCError{Code: -32603, Message: "Internal error"}
	ErrFileNotFound      = &RPCError{Code: -32003, Message: "File not found"}
	ErrPermissionDenied  = &RPCError{Code: -32004, Message: "Permission denied"}
	ErrTimeout           = &RPCError{Code: -32005, Message: "Command timeout"}
	ErrInvalidPath       = &RPCError{Code: -32006, Message: "Invalid path"}
	ErrWorkspaceNotFound = &RPCError{Code: -32007, Message: "Workspace not found"}
)
