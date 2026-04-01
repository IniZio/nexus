package services

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestServiceManager_StartStatusStop(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := mgr.Start(ctx, "ws-1", "sleep", ".", "sleep", []string{"2"}, StartOptions{})
	if err != nil {
		t.Fatalf("start service: %v", err)
	}

	status := mgr.Status("ws-1", "sleep")
	running, _ := status["running"].(bool)
	if !running {
		t.Fatal("expected running service")
	}

	if !mgr.Stop("ws-1", "sleep") {
		t.Fatal("expected stop to succeed")
	}
}

func TestServiceManager_Restart(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := mgr.Start(ctx, "ws-1", "probe", ".", "sleep", []string{"2"}, StartOptions{})
	if err != nil {
		t.Fatalf("start service: %v", err)
	}

	proc, err := mgr.Restart(ctx, "ws-1", "probe", ".", "sleep", []string{"2"}, StartOptions{})
	if err != nil {
		t.Fatalf("restart service: %v", err)
	}

	if proc.PID == 0 {
		t.Fatal("expected restarted process with pid")
	}

	_ = mgr.Stop("ws-1", "probe")
}

func TestServiceManager_LogsBounded(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	long := strings.Repeat("x", maxLogBytes+256)
	_, err := mgr.Start(ctx, "ws-1", "logs", ".", "sh", []string{"-lc", "printf '%s' \"" + long + "\""}, StartOptions{})
	if err != nil {
		t.Fatalf("start service: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	logs := mgr.Logs("ws-1", "logs")
	stdout, _ := logs["stdout"].(string)
	if len(stdout) > maxLogBytes {
		t.Fatalf("expected bounded logs <= %d, got %d", maxLogBytes, len(stdout))
	}
}

func TestServiceManager_AutoRestart(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := mgr.Start(ctx, "ws-1", "restarter", ".", "sh", []string{"-lc", "exit 1"}, StartOptions{
		AutoRestart:  true,
		MaxRestarts:  1,
		RestartDelay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start service: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	status := mgr.Status("ws-1", "restarter")
	running, _ := status["running"].(bool)
	if running {
		t.Fatal("expected service not running after one failed restart attempt")
	}
}
