package sprites

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	sdk "github.com/superfly/sprites-go"
)

func shrinkDelays(t *testing.T) {
	t.Helper()
	old := retryDelays
	retryDelays = []time.Duration{time.Microsecond, time.Microsecond, time.Microsecond}
	t.Cleanup(func() { retryDelays = old })
}

func TestIsTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bad handshake", errors.New("websocket: Bad Handshake"), true},
		{"control plane", errors.New("CONTROL_PLANE_UNAVAILABLE"), true},
		{"conn reset", errors.New("read: connection reset by peer"), true},
		{"unexpected eof not included", errors.New("unexpected EOF"), false},
		{"text 502", errors.New("status 502 from gateway"), true},
		{"text 503", errors.New("got 503"), true},
		{"text 504", errors.New("got 504"), true},
		{"bad gateway", errors.New("Bad Gateway"), true},
		{"service unavailable", errors.New("Service Unavailable"), true},
		{"gateway timeout", errors.New("Gateway Timeout"), true},
		{"api 502", &sdk.APIError{StatusCode: 502}, true},
		{"api 503", fmt.Errorf("wrap: %w", &sdk.APIError{StatusCode: 503}), true},
		{"api 504", &sdk.APIError{StatusCode: 504}, true},
		{"api 401", &sdk.APIError{StatusCode: 401, Message: "bad handshake"}, false},
		{"api 403", &sdk.APIError{StatusCode: 403}, false},
		{"api 404", &sdk.APIError{StatusCode: 404}, false},
		{"api 409", &sdk.APIError{StatusCode: 409}, false},
		{"api 429", &sdk.APIError{StatusCode: 429}, false},
		{"canceled", context.Canceled, false},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), false},
		{"exit error", &sdk.ExitError{Code: 1}, false},
		{"plain", errors.New("boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsTransient(c.err); got != c.want {
				t.Fatalf("IsTransient(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestRetrySucceedsOnThirdAttempt(t *testing.T) {
	shrinkDelays(t)
	n := 0
	err := withRetry(context.Background(), func() error {
		n++
		if n < 3 {
			return errors.New("bad handshake")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("err=%v attempts=%d, want nil/3", err, n)
	}
}

func TestRetryGivesUpAfterFourAttempts(t *testing.T) {
	shrinkDelays(t)
	base := errors.New("connection reset")
	n := 0
	err := withRetry(context.Background(), func() error { n++; return base })
	if n != 4 {
		t.Fatalf("attempts=%d, want 4", n)
	}
	if !errors.Is(err, base) {
		t.Fatalf("err %v does not wrap base", err)
	}
}

func TestRetryNonTransientReturnsImmediately(t *testing.T) {
	shrinkDelays(t)
	base := &sdk.APIError{StatusCode: 404}
	n := 0
	err := withRetry(context.Background(), func() error { n++; return base })
	if n != 1 || err != error(base) {
		t.Fatalf("attempts=%d err=%v, want 1 and identical error", n, err)
	}
}

func TestRetryContextCancelAborts(t *testing.T) {
	old := retryDelays
	retryDelays = []time.Duration{time.Hour, time.Hour, time.Hour}
	t.Cleanup(func() { retryDelays = old })

	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	err := withRetry(ctx, func() error {
		n++
		cancel()
		return errors.New("bad handshake")
	})
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("err=%v attempts=%d, want Canceled/1", err, n)
	}

	n = 0
	err = withRetry(ctx, func() error { n++; return nil })
	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("pre-canceled: err=%v attempts=%d", err, n)
	}
}
