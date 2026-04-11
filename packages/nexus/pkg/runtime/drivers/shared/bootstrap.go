package shared

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type BootstrapOnceGuard struct {
	mu           sync.Mutex
	bootstrapped map[string]bool
}

func NewBootstrapOnceGuard() *BootstrapOnceGuard {
	return &BootstrapOnceGuard{bootstrapped: make(map[string]bool)}
}

func (g *BootstrapOnceGuard) Ensure(instance string, fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.bootstrapped[instance] {
		return nil
	}
	if fn == nil {
		g.bootstrapped[instance] = true
		return nil
	}
	if err := fn(); err != nil {
		return err
	}
	g.bootstrapped[instance] = true
	return nil
}

type LimactlBootstrapOptions struct {
	EnsureBeforeCandidate   func(context.Context, string) error
	MaxAttemptsPerCandidate int
	RetryDelay              time.Duration
	RetryIf                 func(string) bool
	ErrNoCandidates         string
	FormatFailure           func(candidate, trimmedOut string) error
}

func RunLimactlBootstrapScript(ctx context.Context, candidates []string, script string, opts LimactlBootstrapOptions) error {
	max := opts.MaxAttemptsPerCandidate
	if max < 1 {
		max = 1
	}
	delay := opts.RetryDelay
	if delay == 0 {
		delay = 500 * time.Millisecond
	}
	var lastErr error
	for _, candidate := range candidates {
		if opts.EnsureBeforeCandidate != nil {
			if err := opts.EnsureBeforeCandidate(ctx, candidate); err != nil {
				lastErr = err
				continue
			}
		}
		for attempt := 1; attempt <= max; attempt++ {
			out, err := LimactlShellScript(ctx, candidate, script)
			if err == nil {
				return nil
			}
			trimmed := strings.TrimSpace(string(out))
			if opts.FormatFailure != nil {
				lastErr = opts.FormatFailure(candidate, trimmed)
			} else {
				lastErr = fmt.Errorf("limactl shell script failed in %s: %s", candidate, trimmed)
			}
			if opts.RetryIf == nil || !opts.RetryIf(trimmed) || attempt == max {
				break
			}
			time.Sleep(delay)
		}
	}
	if lastErr != nil {
		return lastErr
	}
	if opts.ErrNoCandidates != "" {
		return fmt.Errorf("%s", opts.ErrNoCandidates)
	}
	return fmt.Errorf("bootstrap failed: no lima instance candidates")
}
