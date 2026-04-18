package workspace

import "errors"

var (
	ErrNotFound          = errors.New("workspace not found")
	ErrAlreadyExists     = errors.New("workspace already exists")
	ErrInvalidTransition = errors.New("invalid state transition")
	ErrInvalidState      = errors.New("invalid workspace state")
)
