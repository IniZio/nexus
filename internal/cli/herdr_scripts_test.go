package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The herdr plugin's shell layer is real logic: open-pane.sh decides which
// sandbox an action applies to (via $HERDR_WORKSPACE_ID), which placement a
// pane gets, and whether to exec herdr or nexus3. None of it was executed by
// any test — the scripts were only ever read as text — so a wiring mistake
// there could only be found by clicking the action in herdr and watching it
// misbehave.
//
// These tests run the real scripts against a stub shim that records argv, so
// the contract between manifest, script, and CLI is checked mechanically.

// scriptEnv builds a temp copy of the plugin's bin/ directory alongside a stub
// nexus3-shim.sh and a stub herdr, both of which append their argv to files.
type scriptEnv struct {
	dir      string // contains the copied scripts
	shimLog  string
	herdrLog string
	herdrBin string // absolute path to the herdr stub; set HERDR_BIN_PATH to this value
}

func newScriptEnv(t *testing.T) *scriptEnv {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"pane.sh", "open-pane.sh"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "herdr", "bin", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(binDir, name), src, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	e := &scriptEnv{
		dir:      binDir,
		shimLog:  filepath.Join(root, "shim.argv"),
		herdrLog: filepath.Join(root, "herdr.argv"),
	}

	// The shim sits one level above bin/, exactly as in the real plugin.
	// shell-cwd must answer on stdout because pane.sh captures it.
	// The stub answers both guest round-trips pane.sh makes: shell-cwd, and
	// the `command -v bash` probe. STUB_GUEST_BASH controls what the guest is
	// pretending to have, so both branches are reachable.
	// STUB_SHELL_CWD_FAIL=1 makes shell-cwd exit non-zero (simulates stale binary).
	// STUB_SHELL_CWD overrides the directory shell-cwd reports (default /work).
	shim := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + e.shimLog + "\n" +
		"if [ \"$2\" = \"shell-cwd\" ]; then\n" +
		"  if [ \"${STUB_SHELL_CWD_FAIL:-0}\" = \"1\" ]; then exit 1; fi\n" +
		"  echo \"${STUB_SHELL_CWD:-/work}\"\n" +
		"fi\n" +
		"case \"$*\" in *'command -v bash'*) echo \"${STUB_GUEST_BASH:-/usr/bin/bash}\";; esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(root, "nexus3-shim.sh"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}

	herdrStub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + e.herdrLog + "\nexit 0\n"
	herdrPath := filepath.Join(root, "herdr-stub")
	if err := os.WriteFile(herdrPath, []byte(herdrStub), 0o755); err != nil {
		t.Fatal(err)
	}
	e.herdrBin = herdrPath
	return e
}

func (e *scriptEnv) run(t *testing.T, script string, args []string, env map[string]string) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(e.dir, script)}, args...)...)
	cmd.Env = append(os.Environ(), "HERDR_BIN_PATH="+e.herdrBin)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", script, args, err, out)
	}
}

func (e *scriptEnv) shimArgv(t *testing.T) string  { return readLog(t, e.shimLog) }
func (e *scriptEnv) herdrArgv(t *testing.T) string { return readLog(t, e.herdrLog) }

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "" // no invocation recorded
	}
	return string(b)
}

// TestOpenPaneScript_LifecycleActionsResolveByWorkspaceID pins the wiring that
// makes pause/resume/remove work without the operator typing a sandbox ref:
// the action must forward $HERDR_WORKSPACE_ID as the subcommand's argument.
// If that were dropped the subcommand would report "sandbox ref required" and
// the action would fail for every workspace.
func TestOpenPaneScript_LifecycleActionsResolveByWorkspaceID(t *testing.T) {
	for _, entry := range []string{"space-pause", "space-resume", "space-remove"} {
		e := newScriptEnv(t)
		e.run(t, "open-pane.sh", []string{entry}, map[string]string{"HERDR_WORKSPACE_ID": "w42"})

		got := strings.TrimSpace(e.shimArgv(t))
		// open-pane.sh strips the space- prefix when forwarding to `herdr`:
		// space-pause → herdr pause, space-resume → herdr resume, etc.
		verb := strings.TrimPrefix(entry, "space-")
		want := "herdr " + verb + " w42"
		if got != want {
			t.Errorf("%s: shim argv = %q, want %q", entry, got, want)
		}
		if h := e.herdrArgv(t); h != "" {
			t.Errorf("%s must act directly, not open a pane; herdr was called with %q", entry, h)
		}
	}
}

// TestOpenPaneScript_SplitOmitsWorkspace pins the server-side rule that split
// (and zoomed) placements target an existing pane, so --workspace is rejected:
// "split and zoomed plugin panes target an existing pane; use target_pane_id".
// The flags --plugin, --entrypoint, --placement, and --focus must still be present.
func TestOpenPaneScript_SplitOmitsWorkspace(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"doctor", "split"}, map[string]string{"HERDR_WORKSPACE_ID": "w7"})

	got := e.herdrArgv(t)
	if strings.Contains(got, "--workspace") {
		t.Errorf("split must not pass --workspace; herdr argv = %q", got)
	}
	if strings.Contains(got, "w7") {
		t.Errorf("split must not pass the workspace id; herdr argv = %q", got)
	}
	for _, want := range []string{
		"plugin pane open", "--plugin nexus3", "--entrypoint doctor",
		"--placement split", "--focus",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("herdr argv %q missing %q", got, want)
		}
	}
}

// TestOpenPaneScript_ZoomedOmitsWorkspace covers the zoomed case of the
// overlay|popup|split|zoomed pattern in open-pane.sh. Narrowing that pattern
// to overlay|popup|split leaves the suite green because zoomed is never
// exercised otherwise — this test is the guard on the guard.
// The server error for zoomed matches split: "split and zoomed plugin panes
// target an existing pane; use target_pane_id".
func TestOpenPaneScript_ZoomedOmitsWorkspace(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"logs", "zoomed"}, map[string]string{"HERDR_WORKSPACE_ID": "w7"})

	got := e.herdrArgv(t)
	if strings.Contains(got, "--workspace") {
		t.Errorf("zoomed must not pass --workspace; herdr argv = %q", got)
	}
	if strings.Contains(got, "w7") {
		t.Errorf("zoomed must not pass the workspace id; herdr argv = %q", got)
	}
	for _, want := range []string{
		"plugin pane open", "--plugin nexus3", "--entrypoint logs",
		"--placement zoomed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("herdr argv %q missing %q", got, want)
		}
	}
}

// TestOpenPaneScript_OmitsEnvFlagWhenWorkspaceUnset guards a real footgun:
// passing --env "NEXUS3_WORKSPACE=" would hand the pane an empty ref, which
// reads as "set but empty" rather than absent.
func TestOpenPaneScript_OmitsEnvFlagWhenWorkspaceUnset(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"workspaces", "overlay"}, map[string]string{"HERDR_WORKSPACE_ID": "w1"})

	if got := e.herdrArgv(t); strings.Contains(got, "--env") {
		t.Errorf("no NEXUS3_WORKSPACE set, so --env must be omitted; got %q", got)
	}
}

func TestOpenPaneScript_PassesEnvFlagWhenWorkspaceSet(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"attach", "tab"}, map[string]string{
		"HERDR_WORKSPACE_ID": "w1",
		"NEXUS3_WORKSPACE":   "demo/api",
	})

	if got := e.herdrArgv(t); !strings.Contains(got, "--env NEXUS3_WORKSPACE=demo/api") {
		t.Errorf("expected --env NEXUS3_WORKSPACE=demo/api; got %q", got)
	}
}

// TestPaneScript_ShellUsesResolvedGuestCwd pins that the guest shell pane asks
// nexus3 where to land and then passes that directory to exec --cwd. Without
// it every shell would open in /root regardless of what is mounted.
func TestPaneScript_ShellUsesResolvedGuestCwd(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "pane.sh", []string{"shell"}, map[string]string{"NEXUS3_WORKSPACE": "demo/api"})

	got := e.shimArgv(t)
	if !strings.Contains(got, "herdr shell-cwd demo/api") {
		t.Errorf("shell pane must resolve the guest cwd first; argv was %q", got)
	}
	if !strings.Contains(got, "--cwd /work") {
		t.Errorf("resolved cwd must be passed to exec --cwd; argv was %q", got)
	}
	if !strings.Contains(got, "--pty") {
		t.Errorf("an interactive shell needs --pty; argv was %q", got)
	}
}

// TestPaneScript_ShellRefusesWithoutWorkspace pins the failure mode: no
// workspace means no sandbox to attach to, and the pane must say so rather
// than exec a shell against an empty ref.
func TestPaneScript_ShellRefusesWithoutWorkspace(t *testing.T) {
	e := newScriptEnv(t)
	cmd := exec.Command("sh", filepath.Join(e.dir, "pane.sh"), "shell")
	cmd.Env = append(os.Environ(), "NEXUS3_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected a non-zero exit when NEXUS3_WORKSPACE is unset")
	}
	if !strings.Contains(string(out), "NEXUS3_WORKSPACE not set") {
		t.Errorf("error should name the missing variable; got %q", out)
	}
}

// TestPaneScript_RejectsUnknownSubcommand ensures a manifest typo surfaces as
// a clear error rather than a pane that opens and does nothing.
func TestPaneScript_RejectsUnknownSubcommand(t *testing.T) {
	e := newScriptEnv(t)
	cmd := exec.Command("sh", filepath.Join(e.dir, "pane.sh"), "not-a-subcommand")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected a non-zero exit for an unknown subcommand")
	}
	if !strings.Contains(string(out), "unknown subcommand") {
		t.Errorf("error should say the subcommand is unknown; got %q", out)
	}
}

// TestPaneScript_ProbesGuestNotHostForBash pins a defect that asked the wrong
// machine: the shell pane tested the HOST for /usr/bin/bash to decide which
// shell to run in the GUEST. On macOS — a platform the manifest declares —
// bash is at /bin/bash, so every guest would have been demoted to /bin/sh.
func TestPaneScript_ProbesGuestNotHostForBash(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "pane.sh", []string{"shell"}, map[string]string{
		"NEXUS3_WORKSPACE": "demo/api",
		"STUB_GUEST_BASH":  "/usr/bin/bash",
	})

	got := e.shimArgv(t)
	if !strings.Contains(got, "command -v bash") {
		t.Errorf("shell pane must probe the GUEST for bash; argv was %q", got)
	}
	if !strings.Contains(got, "/usr/bin/bash -l") {
		t.Errorf("guest reported bash, so it must be used as a login shell; argv was %q", got)
	}
}

// TestPaneScript_FallsBackToShWhenGuestLacksBash covers the other branch: a
// minimal guest image must get /bin/sh rather than an exec that fails and
// closes the pane before the error can be read.
func TestPaneScript_FallsBackToShWhenGuestLacksBash(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "pane.sh", []string{"shell"}, map[string]string{
		"NEXUS3_WORKSPACE": "demo/api",
		"STUB_GUEST_BASH":  "/bin/sh",
	})

	got := e.shimArgv(t)
	if !strings.Contains(got, "demo/api /bin/sh") {
		t.Errorf("guest without bash must fall back to /bin/sh; argv was %q", got)
	}
	if strings.Contains(got, "bash -l") {
		t.Errorf("must not run bash when the guest does not have it; argv was %q", got)
	}
}

// TestPaneScript_ShellCwdFailureIsVisible guards the silent-failure that
// originally caused the operator to land at /root with no error: if the
// nexus3 binary is stale and does not recognise "herdr shell-cwd", the
// command exits non-zero and the pane must say so loudly — not silently
// substitute /root.
func TestPaneScript_ShellCwdFailureIsVisible(t *testing.T) {
	e := newScriptEnv(t)
	cmd := exec.Command("sh", filepath.Join(e.dir, "pane.sh"), "shell")
	cmd.Env = append(os.Environ(),
		"NEXUS3_WORKSPACE=ac3/envproof2",
		"STUB_SHELL_CWD_FAIL=1",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit when shell-cwd fails")
	}
	outStr := string(out)
	if !strings.Contains(outStr, "stale") && !strings.Contains(outStr, "herdr") {
		t.Errorf("error must mention the likely cause (stale binary / herdr); got %q", outStr)
	}
	if !strings.Contains(outStr, "build.sh") {
		t.Errorf("error must tell the operator how to fix it (build.sh); got %q", outStr)
	}
}

// TestPaneScript_ShellCwdLegitimateRoot checks that a sandbox with no mount
// legitimately returning /root (exit 0) is NOT treated as an error: the pane
// must continue and open the shell at /root rather than aborting.
func TestPaneScript_ShellCwdLegitimateRoot(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "pane.sh", []string{"shell"}, map[string]string{
		"NEXUS3_WORKSPACE": "ac3/vcpuctl",
		"STUB_SHELL_CWD":   "/root",
	})

	got := e.shimArgv(t)
	if !strings.Contains(got, "--cwd /root") {
		t.Errorf("legitimate /root from shell-cwd must be honoured as --cwd /root; argv was %q", got)
	}
}

// TestABIFileValue pins the plugin's declared ABI to 2, which was bumped when
// the CLI surface moved from __herdr-plugin to the herdr command group.
// A pre-rename binary reports ABI 1 and build.sh will reject it.
// Mutation: revert plugins/herdr/abi to "1" and this test goes RED.
func TestABIFileValue(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "plugins", "herdr", "abi"))
	if err != nil {
		t.Fatalf("read plugins/herdr/abi: %v", err)
	}
	got := strings.TrimSpace(string(b))
	const want = "2"
	if got != want {
		t.Errorf("plugins/herdr/abi = %q, want %q — bump the file and const herdrPluginABIVersion in cmd_herdr_plugin.go together", got, want)
	}
}

// TestOpenPaneScript_OverlayOmitsWorkspace pins the server-side rule that
// overlay and popup placements target the active pane, so --workspace is
// rejected. If open-pane.sh passes --workspace for these placements, herdr
// responds with "overlay and popup plugin panes target the active pane".
func TestOpenPaneScript_OverlayOmitsWorkspace(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"workspaces", "overlay"}, map[string]string{"HERDR_WORKSPACE_ID": "w8"})

	got := e.herdrArgv(t)
	if strings.Contains(got, "--workspace") {
		t.Errorf("overlay must not pass --workspace; herdr argv = %q", got)
	}
	if strings.Contains(got, "w8") {
		t.Errorf("overlay must not pass the workspace id; herdr argv = %q", got)
	}
	for _, want := range []string{"--plugin nexus3", "--entrypoint workspaces", "--placement overlay"} {
		if !strings.Contains(got, want) {
			t.Errorf("herdr argv %q missing %q", got, want)
		}
	}
}

// TestOpenPaneScript_OverlayCarriesFocus pins that --focus is kept for overlay
// placements. herdr accepts --focus alongside overlay (live probe: EXIT:0,
// focused:true in the result).
func TestOpenPaneScript_OverlayCarriesFocus(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"workspaces", "overlay"}, map[string]string{"HERDR_WORKSPACE_ID": "w8"})

	if got := e.herdrArgv(t); !strings.Contains(got, "--focus") {
		t.Errorf("overlay must still carry --focus; herdr argv = %q", got)
	}
}

// TestOpenPaneScript_TabCarriesWorkspace verifies that tab placements carry
// --workspace. Tab is the only placement the server accepts it for; every other
// placement (overlay, popup, split, zoomed) targets an active or existing pane
// and the server rejects --workspace for them.
func TestOpenPaneScript_TabCarriesWorkspace(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"attach", "tab"}, map[string]string{"HERDR_WORKSPACE_ID": "w33"})

	got := e.herdrArgv(t)
	for _, want := range []string{
		"--plugin nexus3", "--entrypoint attach", "--placement tab", "--workspace w33",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tab: herdr argv %q missing %q", got, want)
		}
	}
}

// TestOpenPaneScript_OverlayWithEnvOmitsWorkspace exercises the env-SET branch
// of the overlay leg. When NEXUS3_WORKSPACE is set, --env NEXUS3_WORKSPACE=…
// must be forwarded but --workspace must still be absent — the server rejects
// --workspace for overlay regardless of whether an env var is set.
func TestOpenPaneScript_OverlayWithEnvOmitsWorkspace(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"workspaces", "overlay"}, map[string]string{
		"HERDR_WORKSPACE_ID": "w8",
		"NEXUS3_WORKSPACE":   "demo/api",
	})

	got := e.herdrArgv(t)
	if strings.Contains(got, "--workspace") {
		t.Errorf("overlay must not pass --workspace even when NEXUS3_WORKSPACE is set; herdr argv = %q", got)
	}
	if strings.Contains(got, "w8") {
		t.Errorf("overlay must not pass the workspace id; herdr argv = %q", got)
	}
	if !strings.Contains(got, "--env NEXUS3_WORKSPACE=demo/api") {
		t.Errorf("NEXUS3_WORKSPACE must be forwarded as --env; herdr argv = %q", got)
	}
}

// TestOpenPaneScript_NewTab pins that the new-tab entrypoint forwards
// HERDR_WORKSPACE_ID to `nexus3 herdr new-tab` via the shim. The context-aware
// dispatch (guest pane vs host tab) happens in Go, not in the shell script —
// the script's only job is to pass the workspace ID through correctly.
func TestOpenPaneScript_NewTab(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"new-tab"}, map[string]string{
		"HERDR_WORKSPACE_ID": "w42",
	})

	got := strings.TrimSpace(e.shimArgv(t))
	want := "herdr new-tab w42"
	if got != want {
		t.Errorf("new-tab: shim argv = %q, want %q", got, want)
	}
	// The herdr binary must NOT be called directly — the dispatch is in Go.
	if h := e.herdrArgv(t); h != "" {
		t.Errorf("new-tab must not call herdr directly; herdr was called with %q", h)
	}
}
