package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestSeedGuestShellProfile_ScriptActuallySourcesCredEnv(t *testing.T) {
	dir := t.TempDir()
	credEnv := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(credEnv, []byte("CLAUDE_CODE_OAUTH_TOKEN=placeholder-abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	script := strings.ReplaceAll(buildGuestShellProfileScript(0, 0), GuestCredEnvPath, credEnv)
	profile := filepath.Join(dir, "nexus-cred.sh")
	if err := os.WriteFile(profile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	// INNER shell is a separate process; only sees variable if `set -a` exported it.
	out, err := exec.Command("/bin/sh", "-c",
		". "+profile+"; /bin/sh -c 'echo $CLAUDE_CODE_OAUTH_TOKEN'").CombinedOutput()
	if err != nil {
		t.Fatalf("sourcing the drop-in failed: %v\noutput: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "placeholder-abc" {
		t.Errorf("a child process did not inherit the credential: got %q, want %q\n"+
			"the drop-in must EXPORT what it sources (set -a), or an interactively "+
			"started agent still gets no credential\nscript:\n%s", got, "placeholder-abc", script)
	}
}

func TestSeedGuestShellProfile_NoCredEnvIsHarmless(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "does-not-exist.env")
	script := strings.ReplaceAll(buildGuestShellProfileScript(0, 0), GuestCredEnvPath, absent)
	profile := filepath.Join(dir, "nexus-cred.sh")
	if err := os.WriteFile(profile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("/bin/sh", "-c", ". "+profile+"; echo STILL-ALIVE").CombinedOutput()
	if err != nil {
		t.Fatalf("the drop-in must be a no-op when cred.env is absent, but sourcing "+
			"it failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "STILL-ALIVE") {
		t.Errorf("shell did not survive sourcing the drop-in without cred.env\noutput: %s", out)
	}
}

func TestSeedGuestShellProfile_CarriesNoCredential(t *testing.T) {
	var captured []byte
	seeder := func(_ context.Context, _ domain.SandboxID, payload []byte) error {
		captured = payload
		return nil
	}
	var id domain.SandboxID
	if err := SeedGuestShellProfile(context.Background(), id, 0, 0, seeder); err != nil {
		t.Fatalf("SeedGuestShellProfile: %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("seeder received no payload: the drop-in was never delivered to the guest")
	}
	// Only '=' from sourced file at runtime, never from payload itself.
	for _, line := range strings.Split(string(captured), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if strings.Contains(line, "=") &&
			!strings.HasPrefix(line, "if ") &&
			!strings.HasPrefix(line, "export IS_SANDBOX=") &&
			!strings.HasPrefix(line, "export GIT_SSH_COMMAND=") &&
			!strings.HasPrefix(line, "export NEXUS_HOST_UID=") &&
			!strings.HasPrefix(line, "export NEXUS_HOST_GID=") {
			t.Errorf("drop-in payload assigns a value inline: %q\n"+
				"it must only source %s, export IS_SANDBOX, or export GIT_SSH_COMMAND, never carry a credential itself", line, GuestCredEnvPath)
		}
	}
	if !strings.Contains(string(captured), GuestCredEnvPath) {
		t.Errorf("drop-in does not reference %s, so it sources nothing:\n%s",
			GuestCredEnvPath, captured)
	}
}

func TestSeedGuestShellProfile_NilSeederIsNoOp(t *testing.T) {
	var id domain.SandboxID
	if err := SeedGuestShellProfile(context.Background(), id, 0, 0, nil); err != nil {
		t.Errorf("nil seeder must be a no-op, got %v", err)
	}
}

func TestSeedGuestShellProfile_SeederErrorPropagates(t *testing.T) {
	want := errors.New("guest copy refused")
	seeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return want }
	var id domain.SandboxID
	err := SeedGuestShellProfile(context.Background(), id, 0, 0, seeder)
	if err == nil {
		t.Fatal("a failed delivery must return an error, not nil")
	}
	if !errors.Is(err, want) {
		t.Errorf("error does not wrap the seeder failure: %v", err)
	}
}

// ── Slice 2: IS_SANDBOX + claude function ─────────────────────────────────────

func TestSeedGuestShellProfile_IsSandboxExported(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "does-not-exist.env")
	script := strings.ReplaceAll(buildGuestShellProfileScript(0, 0), GuestCredEnvPath, absent)
	profile := filepath.Join(dir, "nexus-cred.sh")
	if err := os.WriteFile(profile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("/bin/sh", "-c",
		". "+profile+"; /bin/sh -c 'echo $IS_SANDBOX'").CombinedOutput()
	if err != nil {
		t.Fatalf("sourcing drop-in failed: %v\noutput: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "1" {
		t.Errorf("IS_SANDBOX not exported to child process: got %q, want \"1\"\n"+
			"The drop-in must export IS_SANDBOX=1 so claude can run as root with "+
			"--dangerously-skip-permissions", got)
	}
}

// stubClaude writes executable shell script that records argv to dir/args.
func stubClaude(t *testing.T, dir string) string {
	t.Helper()
	stub := filepath.Join(dir, "claude")
	const script = "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$STUB_ARGS_FILE\"\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

// buildProfileForTest returns profile script retargeted at non-existent cred.env.
func buildProfileForTest(t *testing.T, dir string) string {
	t.Helper()
	absent := filepath.Join(dir, "does-not-exist.env")
	script := strings.ReplaceAll(buildGuestShellProfileScript(0, 0), GuestCredEnvPath, absent)
	profile := filepath.Join(dir, "nexus-cred.sh")
	if err := os.WriteFile(profile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return profile
}

// runProfileCmd executes shell command after sourcing profile with stub claude
// on PATH and STUB_ARGS_FILE set. Uses cmd.Env to avoid assignment-prefix semantics.
func runProfileCmd(t *testing.T, profile, dir, argsFile, shellCmd string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", ". "+profile+"; "+shellCmd)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"STUB_ARGS_FILE="+argsFile,
	)
	return cmd.CombinedOutput()
}

func TestSeedGuestShellProfile_ClaudeFunctionNoDoubleFlag(t *testing.T) {
	dir := t.TempDir()
	profile := buildProfileForTest(t, dir)
	argsFile := filepath.Join(dir, "args")
	_ = stubClaude(t, dir)

	out, err := runProfileCmd(t, profile, dir, argsFile, "claude --dangerously-skip-permissions foo")
	if err != nil {
		t.Fatalf("claude via shell function failed: %v\noutput: %s", err, out)
	}
	args, _ := os.ReadFile(argsFile)
	argStr := string(args)
	count := strings.Count(argStr, "--dangerously-skip-permissions")
	if count != 1 {
		t.Errorf("expected --dangerously-skip-permissions exactly once, got %d occurrences; stub saw args:\n%s",
			count, argStr)
	}
}

func TestSeedGuestShellProfile_CommandClaudeBypassesFunction(t *testing.T) {
	dir := t.TempDir()
	profile := buildProfileForTest(t, dir)
	argsFile := filepath.Join(dir, "args")
	_ = stubClaude(t, dir)

	out, err := runProfileCmd(t, profile, dir, argsFile, "command claude foo")
	if err != nil {
		t.Fatalf("command claude failed: %v\noutput: %s", err, out)
	}
	args, _ := os.ReadFile(argsFile)
	argStr := string(args)
	if strings.Contains(argStr, "--dangerously-skip-permissions") {
		t.Errorf("\"command claude\" must bypass the wrapper function and not inject the flag; stub saw args:\n%s", argStr)
	}
	if !strings.Contains(argStr, "foo") {
		t.Errorf("\"command claude foo\" must pass 'foo' through; stub saw args:\n%s", argStr)
	}
}
