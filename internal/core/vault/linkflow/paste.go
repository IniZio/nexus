package linkflow

import (
	"errors"
	"fmt"
	"net/url"
)

var ErrMissingCode = errors.New("linkflow: no code parameter in redirect URL")

// ExtractCode parses a pasted OAuth redirect URL, validates the state, and returns the code.
func ExtractCode(rawURL, expectedState string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("linkflow: parse redirect URL: %w", err)
	}
	q := u.Query()
	if state := q.Get("state"); state != expectedState {
		return "", ErrStateMismatch
	}
	code := q.Get("code")
	if code == "" {
		return "", ErrMissingCode
	}
	return code, nil
}
