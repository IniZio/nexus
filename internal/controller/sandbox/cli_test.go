package sandbox

import (
	"context"
	"testing"
)

func TestCLILifecycleArgv(t *testing.T) {
	const fakeBin = "/fake/nexus"
	lc := NewCLILifecycle(CLIConfig{NexusBin: fakeBin})

	var calls [][]string
	lc.run = func(_ context.Context, argv []string) (string, error) {
		calls = append(calls, append([]string(nil), argv...))
		return "", nil
	}

	ctx := context.Background()
	const id = "sb-abc"

	if err := lc.Pause(ctx, id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := lc.Resume(ctx, id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := lc.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := lc.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}

	want := [][]string{
		{fakeBin, "herdr", "pause", id},
		{fakeBin, "herdr", "resume", id},
		{fakeBin, "sandbox", "stop", id},
		{fakeBin, "sandbox", "start", id},
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d", len(calls), len(want))
	}
	for i, w := range want {
		if len(calls[i]) != len(w) {
			t.Errorf("call[%d]: got %v, want %v", i, calls[i], w)
			continue
		}
		for j := range w {
			if calls[i][j] != w[j] {
				t.Errorf("call[%d][%d]: got %q, want %q", i, j, calls[i][j], w[j])
			}
		}
	}
}

func TestCLILifecycleRunErrorPropagated(t *testing.T) {
	lc := NewCLILifecycle(CLIConfig{NexusBin: "/fake/nexus"})
	lc.run = func(_ context.Context, _ []string) (string, error) {
		return "some output", context.DeadlineExceeded
	}
	if err := lc.Pause(context.Background(), "sb-1"); err == nil {
		t.Error("expected error, got nil")
	}
}
