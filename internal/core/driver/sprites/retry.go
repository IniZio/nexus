package sprites

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/superfly/sprites-go"
)

// retryDelays is a var so tests can shrink it. Attempts = len+1.
var retryDelays = []time.Duration{1 * time.Second, 3 * time.Second, 7 * time.Second}

var transientMarkers = []string{
	"bad handshake",
	"control_plane_unavailable",
	"connection reset",
	" 502",
	" 503",
	" 504",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
}

// IsTransient reports whether err looks like a Sprites gateway flake.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var exit *sdk.ExitError
	if errors.As(err, &exit) {
		return false
	}
	if api := sdk.IsAPIError(err); api != nil {
		switch api.StatusCode {
		case 502, 503, 504:
			return true
		}
		if api.StatusCode >= 400 && api.StatusCode < 500 {
			return false
		}
	}
	msg := strings.ToLower(err.Error())
	for _, m := range transientMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

func withRetry(ctx context.Context, op func() error) error {
	attempts := len(retryDelays) + 1
	var err error
	for i := 0; i < attempts; i++ {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		err = op()
		if err == nil || !IsTransient(err) {
			return err
		}
		if i == attempts-1 {
			break
		}
		t := time.NewTimer(retryDelays[i])
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return fmt.Errorf("after %d attempts: %w", attempts, err)
}
