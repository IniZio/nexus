package auth

import "errors"

var (
	ErrInvalidToken = errors.New("invalid authentication token")
	ErrTokenExpired = errors.New("authentication token expired")
	ErrAccessDenied = errors.New("access denied")
)
