package spotlight

import "errors"

var (
	ErrNotFound      = errors.New("forward not found")
	ErrAlreadyExists = errors.New("forward already exists")
)
