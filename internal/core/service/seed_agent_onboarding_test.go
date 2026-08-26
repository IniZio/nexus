package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// spyExecer is a test double for GuestExecer that captures argv and stdin,
// runs the provided script with the provided stdin via /bin/sh on the host,
// and returns the exit code. This exercises the actual script logic without
// a live VM: the host /bin/sh is sufficient because the script is POSIX sh.
//
// captureDir is a directory the spy operates in; the test supplies t.TempDir().
func newSpyExecer(t *testing.T, captureDir string) (GuestExecer, *spyExecRecord) {
	t.Helper()
	rec := &spyExecRecord{}
	spy := func(ctx context.Context, _ domain.SandboxID, argv []string, stdin io.Reader) (int32, error) {
		rec.Called = true
		rec.Argv = argv
		if stdin != nil {
			data, err := io.ReadAll(stdin)
			if err != nil {
				return 0, err
			}
			rec.StdinBytes = data
		}

		// Execute the script on the host /bin/sh to validate its logic.
		// The test overrides GuestAgentOnboardingPath via an env var set in the
		// script text (see individual tests) so the host filesystem is used
		// as the sandbox.
		if len(argv) < 3 || argv[0] != "/bin/sh" || argv[1] != "-c" {
			t.Errorf("spyExecer: expected argv [/bin/sh -c <script>], got %v", argv)
			return 1, nil
		}
		script := argv[2]
		// Redirect the destination path inside the script to captureDir.
		script = strings.ReplaceAll(script, GuestAgentOnboardingPath, filepath.Join(captureDir, ".claude.json"))

		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
		cmd.Stdin = bytes.NewReader(rec.StdinBytes)
		cmd.Stdout = os.Stderr // visible on test failure
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return int32(ee.ExitCode()), nil
			}
			return 1, err
		}
		return 0, nil
	}
	return spy, rec
}

type spyExecRecord struct {
	Called     bool
	Argv       []string
	StdinBytes []byte
}

// TestSeedGuestAgentOnboarding_NilExecerIsNoOp matches the nil-seeder
// convention established by SeedGuestShellProfile and SeedGuest.
func TestSeedGuestAgentOnboarding_NilExecerIsNoOp(t *testing.T) {
	var id domain.SandboxID
	if err := SeedGuestAgentOnboarding(context.Background(), id, "/work", nil, nil); err != nil {
		t.Errorf("nil execer must be a no-op, got %v", err)
	}
}

// TestSeedGuestAgentOnboarding_WritesValidJSON verifies that when the target
// file is absent the script writes valid JSON containing the three required keys.
func TestSeedGuestAgentOnboarding_WritesValidJSON(t *testing.T) {
	dir := t.TempDir()
	spy, rec := newSpyExecer(t, dir)

	var id domain.SandboxID
	if err := SeedGuestAgentOnboarding(context.Background(), id, "/work", nil, spy); err != nil {
		t.Fatalf("SeedGuestAgentOnboarding: %v", err)
	}
	if !rec.Called {
		t.Fatal("execer was not called")
	}

	outPath := filepath.Join(dir, ".claude.json")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, data)
	}

	if v, ok := cfg["hasCompletedOnboarding"]; !ok || v != true {
		t.Errorf("hasCompletedOnboarding missing or wrong: %v", cfg["hasCompletedOnboarding"])
	}
	if v, ok := cfg["theme"]; !ok || v != "dark" {
		t.Errorf("theme missing or wrong: %v", cfg["theme"])
	}
	projects, ok := cfg["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects key missing or wrong type: %T", cfg["projects"])
	}
	proj, ok := projects["/work"].(map[string]any)
	if !ok {
		t.Fatalf("projects[\"/work\"] missing or wrong type: %T", projects["/work"])
	}
	if v, _ := proj["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("hasTrustDialogAccepted missing or false")
	}
	if v, _ := proj["hasCompletedProjectOnboarding"].(bool); !v {
		t.Errorf("hasCompletedProjectOnboarding missing or false")
	}
}

// TestSeedGuestAgentOnboarding_ExistingFileIsUntouched verifies that the
// script is a no-op when GuestAgentOnboardingPath already exists (idempotency
// / clobber guard). The guard must be implemented inside the guest script, not
// on the host side before the exec call.
func TestSeedGuestAgentOnboarding_ExistingFileIsUntouched(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, ".claude.json")
	original := []byte(`{"userID":"real-user","hasCompletedOnboarding":true}`)
	if err := os.WriteFile(outPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	spy, _ := newSpyExecer(t, dir)
	var id domain.SandboxID
	if err := SeedGuestAgentOnboarding(context.Background(), id, "/work", nil, spy); err != nil {
		t.Fatalf("SeedGuestAgentOnboarding: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(data, original) {
		t.Errorf("existing file was modified\nbefore: %s\nafter:  %s", original, data)
	}
}

// TestSeedGuestAgentOnboarding_EmptyProjectDir seeds with no projects entry.
func TestSeedGuestAgentOnboarding_EmptyProjectDir(t *testing.T) {
	dir := t.TempDir()
	spy, rec := newSpyExecer(t, dir)

	var id domain.SandboxID
	if err := SeedGuestAgentOnboarding(context.Background(), id, "", nil, spy); err != nil {
		t.Fatalf("SeedGuestAgentOnboarding: %v", err)
	}
	if !rec.Called {
		t.Fatal("execer was not called")
	}

	outPath := filepath.Join(dir, ".claude.json")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, data)
	}
	if _, ok := cfg["projects"]; ok {
		t.Errorf("projects key must be absent when projectDir is empty, got: %v", cfg["projects"])
	}
	if v, ok := cfg["hasCompletedOnboarding"]; !ok || v != true {
		t.Errorf("hasCompletedOnboarding missing or wrong: %v", cfg["hasCompletedOnboarding"])
	}
}

// TestSeedGuestAgentOnboarding_SpecialCharsInProjectDir verifies that a
// projectDir containing a double quote, backslash, or $(...) does not break
// out of the JSON or the shell. encoding/json handles JSON escaping; piping
// via stdin keeps the JSON out of shell syntax entirely.
func TestSeedGuestAgentOnboarding_SpecialCharsInProjectDir(t *testing.T) {
	cases := []string{
		`/work/foo"bar`,
		`/work/foo\bar`,
		`/work/$(rm -rf /)`,
		`/work/foo` + "\n" + `bar`,
	}
	for _, projectDir := range cases {
		t.Run(projectDir, func(t *testing.T) {
			dir := t.TempDir()
			spy, rec := newSpyExecer(t, dir)
			var id domain.SandboxID
			if err := SeedGuestAgentOnboarding(context.Background(), id, projectDir, nil, spy); err != nil {
				t.Fatalf("SeedGuestAgentOnboarding(%q): %v", projectDir, err)
			}
			if !rec.Called {
				t.Fatal("execer was not called")
			}

			outPath := filepath.Join(dir, ".claude.json")
			data, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("output file not written for projectDir %q: %v", projectDir, err)
			}

			var cfg map[string]any
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("output is not valid JSON for projectDir %q: %v\nraw: %s", projectDir, err, data)
			}

			projects, ok := cfg["projects"].(map[string]any)
			if !ok {
				t.Fatalf("projects key missing for projectDir %q", projectDir)
			}
			if _, ok := projects[projectDir]; !ok {
				t.Errorf("projects[%q] missing; got keys: %v", projectDir, projects)
			}
		})
	}
}
