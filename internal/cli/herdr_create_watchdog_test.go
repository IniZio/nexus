package cli

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func watchedSh(ctx context.Context, wrap func(io.Writer) io.Writer, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Stdout = wrap(io.Discard)
	cmd.Stderr = wrap(io.Discard)
	return cmd
}

func TestHerdrRunCreateWatched_ContinuousOutputNotKilled(t *testing.T) {
	// ~600ms of output every 10ms, idle limit 200ms: total >> idle, never idle.
	script := `i=0; while [ $i -lt 30 ]; do echo tick; sleep 0.02; i=$((i+1)); done`
	err := herdrRunCreateWatched(context.Background(), 200*time.Millisecond, time.Hour, nil,
		func(ctx context.Context, wrap func(io.Writer) io.Writer) *exec.Cmd { return watchedSh(ctx, wrap, script) })
	if err != nil {
		t.Fatalf("progressing child killed: %v", err)
	}
}

func TestHerdrRunCreateWatched_SilentKilledWithNoProgressError(t *testing.T) {
	start := time.Now()
	err := herdrRunCreateWatched(context.Background(), 150*time.Millisecond, time.Hour, nil,
		func(ctx context.Context, wrap func(io.Writer) io.Writer) *exec.Cmd { return watchedSh(ctx, wrap, "exec sleep 30") })
	if err == nil || !strings.Contains(err.Error(), "no progress for 150ms") {
		t.Fatalf("want no-progress error, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("kill took %v", time.Since(start))
	}
}

func TestHerdrRunCreateWatched_ProbeChangeResetsIdle(t *testing.T) {
	n := 0
	probe := func() string { n++; return string(rune('a' + n%26)) } // changes every call
	script := `exec sleep 0.6`
	err := herdrRunCreateWatched(context.Background(), 200*time.Millisecond, 20*time.Millisecond, probe,
		func(ctx context.Context, wrap func(io.Writer) io.Writer) *exec.Cmd { return watchedSh(ctx, wrap, script) })
	if err != nil {
		t.Fatalf("silent child with disk activity killed: %v", err)
	}
}

func TestHerdrRunCreateWatched_HardCapEnforcedDespiteOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	script := `while true; do echo tick; sleep 0.01; done`
	err := herdrRunCreateWatched(ctx, 200*time.Millisecond, time.Hour, nil,
		func(ctx context.Context, wrap func(io.Writer) io.Writer) *exec.Cmd { return watchedSh(ctx, wrap, script) })
	if err == nil {
		t.Fatal("hard cap did not kill chatty child")
	}
	if strings.Contains(err.Error(), "no progress") {
		t.Fatalf("hard-cap kill misreported as idle: %v", err)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("ctx err = %v", ctx.Err())
	}
}

func TestHerdrWorktreeCreateLockTimeout_AboveHardCap(t *testing.T) {
	if herdrWorktreeCreateLockTimeout <= herdrWorktreeCreateTimeout {
		t.Fatalf("lock %v must exceed hard cap %v", herdrWorktreeCreateLockTimeout, herdrWorktreeCreateTimeout)
	}
	if herdrWorktreeCreateIdleTimeout >= herdrWorktreeCreateTimeout {
		t.Fatalf("idle %v must be below hard cap %v", herdrWorktreeCreateIdleTimeout, herdrWorktreeCreateTimeout)
	}
}
