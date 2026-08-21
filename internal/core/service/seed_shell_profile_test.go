package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestSeedGuestShellProfile_ScriptActuallySourcesCredEnv executes the drop-in
// with a real /bin/sh instead of asserting on its text.
//
// A string-match test would pass on a script that does not parse, guards on the
// wrong path, or forgets `set -a` and therefore leaves the variables unexported
// — all three of which are the actual failure this drop-in exists to prevent.
// The only assertion that means anything is that a child process sees the
// variable, so that is what this test measures.
func TestSeedGuestShellProfile_ScriptActuallySourcesCredEnv(t *testing.T) {
	dir := t.TempDir()
	credEnv := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(credEnv, []byte("CLAUDE_CODE_OAUTH_TOKEN=placeholder-abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Retarget the script at the temp cred.env. The substitution is exactly the
	// production string, so a broken guard or a missing `set -a` still breaks.
	script := strings.ReplaceAll(guestShellProfileScript, GuestCredEnvPath, credEnv)
	profile := filepath.Join(dir, "nexus3-cred.sh")
	if err := os.WriteFile(profile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	// `sh -c '. profile; sh -c "echo $VAR"'` — the INNER shell is a separate
	// process, so it sees the variable only if `set -a` genuinely exported it
	// rather than merely assigning it in the sourcing shell.
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

// TestSeedGuestShellProfile_NoCredEnvIsHarmless pins the existence guard.
//
// GuestCredEnvPath lives on tmpfs and never appears on a sandbox with no MITM
// proxy. A drop-in that errored there would break `bash -l` — and therefore
// `nexus3 exec --pty` — for every plain sandbox.
func TestSeedGuestShellProfile_NoCredEnvIsHarmless(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "does-not-exist.env")
	script := strings.ReplaceAll(guestShellProfileScript, GuestCredEnvPath, absent)
	profile := filepath.Join(dir, "nexus3-cred.sh")
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

// TestSeedGuestShellProfile_CarriesNoCredential is the security assertion: the
// drop-in names the file to source and nothing else. If a future edit ever
// inlines a token value here it would be written to a world-readable path on
// disk in the guest, outside the tmpfs cred.env the design confines it to.
func TestSeedGuestShellProfile_CarriesNoCredential(t *testing.T) {
	var captured []byte
	seeder := func(_ context.Context, _ domain.SandboxID, payload []byte) error {
		captured = payload
		return nil
	}
	var id domain.SandboxID
	if err := SeedGuestShellProfile(context.Background(), id, seeder); err != nil {
		t.Fatalf("SeedGuestShellProfile: %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("seeder received no payload: the drop-in was never delivered to the guest")
	}
	// The only '=' assignments the payload may contain come from the sourced
	// file at runtime, never from the payload itself.
	for _, line := range strings.Split(string(captured), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		// Permitted '=' lines: shell conditionals, and the IS_SANDBOX marker.
		// IS_SANDBOX=1 is a non-secret environment marker, not a credential.
		if strings.Contains(line, "=") &&
			!strings.HasPrefix(line, "if ") &&
			!strings.HasPrefix(line, "export IS_SANDBOX=") {
			t.Errorf("drop-in payload assigns a value inline: %q\n"+
				"it must only source %s or export the IS_SANDBOX marker, never carry a credential itself", line, GuestCredEnvPath)
		}
	}
	if !strings.Contains(string(captured), GuestCredEnvPath) {
		t.Errorf("drop-in does not reference %s, so it sources nothing:\n%s",
			GuestCredEnvPath, captured)
	}
}

// TestSeedGuestShellProfile_NilSeederIsNoOp matches SeedGuest/SeedGuestAgent.
func TestSeedGuestShellProfile_NilSeederIsNoOp(t *testing.T) {
	var id domain.SandboxID
	if err := SeedGuestShellProfile(context.Background(), id, nil); err != nil {
		t.Errorf("nil seeder must be a no-op, got %v", err)
	}
}

// TestSeedGuestShellProfile_SeederErrorPropagates keeps a delivery failure from
// being swallowed into a silent success.
func TestSeedGuestShellProfile_SeederErrorPropagates(t *testing.T) {
	want := errors.New("guest copy refused")
	seeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return want }
	var id domain.SandboxID
	err := SeedGuestShellProfile(context.Background(), id, seeder)
	if err == nil {
		t.Fatal("a failed delivery must return an error, not nil")
	}
	if !errors.Is(err, want) {
		t.Errorf("error does not wrap the seeder failure: %v", err)
	}
}

// ── Slice 2: IS_SANDBOX + claude function ─────────────────────────────────────

// TestSeedGuestShellProfile_IsSandboxExported verifies that IS_SANDBOX=1 is
// exported by the drop-in so child processes see it.
//
// Mutation guard: remove `export IS_SANDBOX=1` from guestShellProfileScript → fails RED.
func TestSeedGuestShellProfile_IsSandboxExported(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "does-not-exist.env")
	script := strings.ReplaceAll(guestShellProfileScript, GuestCredEnvPath, absent)
	profile := filepath.Join(dir, "nexus3-cred.sh")
	if err := os.WriteFile(profile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	// Child shell must see IS_SANDBOX=1 because the drop-in exported it.
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

// stubClaude writes an executable shell script that records its argv, one
// argument per line, to dir/args. Returns the stub's full path.
func stubClaude(t *testing.T, dir string) string {
	t.Helper()
	stub := filepath.Join(dir, "claude")
	const script = "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$STUB_ARGS_FILE\"\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

// buildProfileForTest returns a profile script retargeted at a non-existent
// cred.env (absence is harmless) and written to dir/nexus3-cred.sh.
func buildProfileForTest(t *testing.T, dir string) string {
	t.Helper()
	absent := filepath.Join(dir, "does-not-exist.env")
	script := strings.ReplaceAll(guestShellProfileScript, GuestCredEnvPath, absent)
	profile := filepath.Join(dir, "nexus3-cred.sh")
	if err := os.WriteFile(profile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return profile
}

// runProfileCmd executes a shell command after sourcing the profile, with the
// stub claude on PATH and STUB_ARGS_FILE pointing at argsFile so the stub can
// record its argv. Env vars are passed via cmd.Env to avoid relying on
// assignment-prefix semantics (which differ between sh builtins and exec).
func runProfileCmd(t *testing.T, profile, dir, argsFile, shellCmd string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", ". "+profile+"; "+shellCmd)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"STUB_ARGS_FILE="+argsFile,
	)
	return cmd.CombinedOutput()
}

// TestSeedGuestShellProfile_ClaudeFunctionAddsFlag verifies that calling
// "claude some-task" through the shell function adds --dangerously-skip-permissions.
//
// Mutation guard: remove the "command claude --dangerously-skip-permissions" branch
// from the function → fails RED.
func TestSeedGuestShellProfile_ClaudeFunctionAddsFlag(t *testing.T) {
	dir := t.TempDir()
	profile := buildProfileForTest(t, dir)
	argsFile := filepath.Join(dir, "args")
	_ = stubClaude(t, dir) // records argv to $STUB_ARGS_FILE

	out, err := runProfileCmd(t, profile, dir, argsFile, "claude some-task")
	if err != nil {
		t.Fatalf("claude via shell function failed: %v\noutput: %s", err, out)
	}
	args, _ := os.ReadFile(argsFile)
	argStr := string(args)
	if !strings.Contains(argStr, "--dangerously-skip-permissions") {
		t.Errorf("claude function did not add --dangerously-skip-permissions; stub saw args:\n%s", argStr)
	}
	if !strings.Contains(argStr, "some-task") {
		t.Errorf("claude function dropped the user's arguments; stub saw args:\n%s", argStr)
	}
}

// TestSeedGuestShellProfile_ClaudeFunctionNoDoubleFlag verifies that the
// function does NOT add --dangerously-skip-permissions a second time when the
// caller already passed it.
//
// Mutation guard: remove the case guard from the claude function → test sees the
// flag twice and fails RED.
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

// TestSeedGuestShellProfile_CommandClaudeBypassesFunction verifies that
// "command claude" reaches the binary without the injected flag.
//
// Mutation guard: the shell function must not intercept "command claude".
// If the function wrapped "command" semantics this test would fail RED.
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
