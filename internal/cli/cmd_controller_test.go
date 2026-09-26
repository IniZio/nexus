package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeScript writes an executable shell script to dir/name and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("writeScript: %v", err)
	}
	return p
}

func TestControllerShimExecs(t *testing.T) {
	out := NewOutput(io.Discard, io.Discard, false)

	t.Run("sibling_wins", func(t *testing.T) {
		tmp := t.TempDir()
		argsFile := filepath.Join(tmp, "recorded-args")
		writeScript(t, tmp, "nexus-controller",
			`printf '%s\n' "$@" > `+argsFile)

		// Point the override var at a fake binary in tmp so sibling lookup fires.
		orig := controllerExecutable
		t.Cleanup(func() { controllerExecutable = orig })
		controllerExecutable = func() (string, error) {
			return filepath.Join(tmp, "fake-nexus"), nil
		}

		if err := runController(context.Background(), []string{"serve", "--flag", "v"}, out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		data, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatalf("recorded-args not written: %v", err)
		}
		got := strings.TrimSpace(string(data))
		want := "serve\n--flag\nv"
		if got != want {
			t.Errorf("args = %q, want %q", got, want)
		}
	})

	t.Run("path_fallback", func(t *testing.T) {
		tmp := t.TempDir()
		argsFile := filepath.Join(tmp, "recorded-args")
		writeScript(t, tmp, "nexus-controller",
			`printf '%s\n' "$@" > `+argsFile)

		// Override exe to a dir that has no nexus-controller sibling.
		emptyDir := t.TempDir()
		orig := controllerExecutable
		t.Cleanup(func() { controllerExecutable = orig })
		controllerExecutable = func() (string, error) {
			return filepath.Join(emptyDir, "fake-nexus"), nil
		}
		t.Setenv("PATH", tmp)

		if err := runController(context.Background(), []string{"ping"}, out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		data, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatalf("recorded-args not written: %v", err)
		}
		if strings.TrimSpace(string(data)) != "ping" {
			t.Errorf("args = %q, want %q", strings.TrimSpace(string(data)), "ping")
		}
	})

	t.Run("exit_code_propagation", func(t *testing.T) {
		tmp := t.TempDir()
		writeScript(t, tmp, "nexus-controller", "exit 3")

		orig := controllerExecutable
		t.Cleanup(func() { controllerExecutable = orig })
		controllerExecutable = func() (string, error) {
			return filepath.Join(tmp, "fake-nexus"), nil
		}

		err := runController(context.Background(), []string{"any"}, out)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var exitErr *ExitCodeError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected *ExitCodeError, got %T: %v", err, err)
		}
		if exitErr.Code != 3 {
			t.Errorf("exit code = %d, want 3", exitErr.Code)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		emptyDir := t.TempDir()

		orig := controllerExecutable
		t.Cleanup(func() { controllerExecutable = orig })
		controllerExecutable = func() (string, error) {
			return filepath.Join(emptyDir, "fake-nexus"), nil
		}
		t.Setenv("PATH", emptyDir)

		err := runController(context.Background(), nil, out)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var coded *CodedError
		if !errors.As(err, &coded) {
			t.Fatalf("expected *CodedError, got %T: %v", err, err)
		}
		if !strings.Contains(coded.Msg, "nexus-controller not found") {
			t.Errorf("msg = %q, want 'nexus-controller not found' substring", coded.Msg)
		}
	})
}
