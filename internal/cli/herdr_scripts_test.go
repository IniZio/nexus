package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// herdr plugin shell layer: real scripts tested against stub argv-logging shims.

// scriptEnv builds temp copy of plugin bin/ with stub shims that log argv.
type scriptEnv struct {
	dir      string
	shimLog  string
	herdrLog string
	herdrBin string
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

	shim := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + e.shimLog + "\n" +
		"if [ \"$2\" = \"shell-cwd\" ]; then\n" +
		"  if [ \"${STUB_SHELL_CWD_FAIL:-0}\" = \"1\" ]; then exit 1; fi\n" +
		"  echo \"${STUB_SHELL_CWD:-/work}\"\n" +
		"fi\n" +
		"case \"$*\" in *'command -v bash'*) echo \"${STUB_GUEST_BASH:-/usr/bin/bash}\";; esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(root, "nexus-shim.sh"), []byte(shim), 0o755); err != nil {
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

// TestOpenPaneScript_LifecycleActionsResolveByWorkspaceID pins workspace ID forwarding.
func TestOpenPaneScript_LifecycleActionsResolveByWorkspaceID(t *testing.T) {
	for _, entry := range []string{"space-pause", "space-resume", "space-remove"} {
		e := newScriptEnv(t)
		e.run(t, "open-pane.sh", []string{entry}, map[string]string{"HERDR_WORKSPACE_ID": "w42"})

		got := strings.TrimSpace(e.shimArgv(t))
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

// TestOpenPaneScript_SplitOmitsWorkspace pins split placement omits --workspace.
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
		"plugin pane open", "--plugin nexus", "--entrypoint doctor",
		"--placement split", "--focus",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("herdr argv %q missing %q", got, want)
		}
	}
}

// TestOpenPaneScript_ZoomedOmitsWorkspace pins zoomed placement guard on guard.
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
		"plugin pane open", "--plugin nexus", "--entrypoint logs",
		"--placement zoomed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("herdr argv %q missing %q", got, want)
		}
	}
}

// TestOpenPaneScript_OmitsEnvFlagWhenWorkspaceUnset guards passing empty NEXUS_WORKSPACE.
func TestOpenPaneScript_OmitsEnvFlagWhenWorkspaceUnset(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"workspaces", "overlay"}, map[string]string{"HERDR_WORKSPACE_ID": "w1"})

	if got := e.herdrArgv(t); strings.Contains(got, "--env") {
		t.Errorf("no NEXUS_WORKSPACE set, so --env must be omitted; got %q", got)
	}
}

func TestOpenPaneScript_PassesEnvFlagWhenWorkspaceSet(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"attach", "tab"}, map[string]string{
		"HERDR_WORKSPACE_ID": "w1",
		"NEXUS_WORKSPACE":    "demo/api",
	})

	if got := e.herdrArgv(t); !strings.Contains(got, "--env NEXUS_WORKSPACE=demo/api") {
		t.Errorf("expected --env NEXUS_WORKSPACE=demo/api; got %q", got)
	}
}

// TestPaneScript_ShellUsesResolvedGuestCwd pins guest shell resolves cwd before exec.
func TestPaneScript_ShellUsesResolvedGuestCwd(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "pane.sh", []string{"shell"}, map[string]string{"NEXUS_WORKSPACE": "demo/api"})

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

// TestPaneScript_ShellRefusesWithoutWorkspace pins refusal with no workspace.
func TestPaneScript_ShellRefusesWithoutWorkspace(t *testing.T) {
	e := newScriptEnv(t)
	cmd := exec.Command("sh", filepath.Join(e.dir, "pane.sh"), "shell")
	cmd.Env = append(os.Environ(), "NEXUS_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected a non-zero exit when NEXUS_WORKSPACE is unset")
	}
	if !strings.Contains(string(out), "NEXUS_WORKSPACE not set") {
		t.Errorf("error should name the missing variable; got %q", out)
	}
}

// TestPaneScript_RejectsUnknownSubcommand ensures manifest typo is surfaced.
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

// TestPaneScript_ProbesGuestNotHostForBash pins defect: tested HOST not GUEST bash.
func TestPaneScript_ProbesGuestNotHostForBash(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "pane.sh", []string{"shell"}, map[string]string{
		"NEXUS_WORKSPACE": "demo/api",
		"STUB_GUEST_BASH": "/usr/bin/bash",
	})

	got := e.shimArgv(t)
	if !strings.Contains(got, "command -v bash") {
		t.Errorf("shell pane must probe the GUEST for bash; argv was %q", got)
	}
	if !strings.Contains(got, "/usr/bin/bash -l") {
		t.Errorf("guest reported bash, so it must be used as a login shell; argv was %q", got)
	}
}

// TestPaneScript_FallsBackToShWhenGuestLacksBash pins fallback to /bin/sh.
func TestPaneScript_FallsBackToShWhenGuestLacksBash(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "pane.sh", []string{"shell"}, map[string]string{
		"NEXUS_WORKSPACE": "demo/api",
		"STUB_GUEST_BASH": "/bin/sh",
	})

	got := e.shimArgv(t)
	if !strings.Contains(got, "demo/api /bin/sh") {
		t.Errorf("guest without bash must fall back to /bin/sh; argv was %q", got)
	}
	if strings.Contains(got, "bash -l") {
		t.Errorf("must not run bash when the guest does not have it; argv was %q", got)
	}
}

// TestPaneScript_ShellCwdFailureIsVisible guards silent-failure with stale binary.
func TestPaneScript_ShellCwdFailureIsVisible(t *testing.T) {
	e := newScriptEnv(t)
	cmd := exec.Command("sh", filepath.Join(e.dir, "pane.sh"), "shell")
	cmd.Env = append(os.Environ(),
		"NEXUS_WORKSPACE=ac3/envproof2",
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

// TestPaneScript_ShellCwdLegitimateRoot checks /root exit 0 is not an error.
func TestPaneScript_ShellCwdLegitimateRoot(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "pane.sh", []string{"shell"}, map[string]string{
		"NEXUS_WORKSPACE": "ac3/vcpuctl",
		"STUB_SHELL_CWD":  "/root",
	})

	got := e.shimArgv(t)
	if !strings.Contains(got, "--cwd /root") {
		t.Errorf("legitimate /root from shell-cwd must be honoured as --cwd /root; argv was %q", got)
	}
}

// TestABIFileValue pins plugin ABI to 3 (requires herdr ≥0.9.0).
// MUTATION-PIN: reverting plugins/herdr/abi to "2" makes this RED.
func TestABIFileValue(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "plugins", "herdr", "abi"))
	if err != nil {
		t.Fatalf("read plugins/herdr/abi: %v", err)
	}
	got := strings.TrimSpace(string(b))
	const want = "3"
	if got != want {
		t.Errorf("plugins/herdr/abi = %q, want %q — bump the file and const herdrPluginABIVersion in cmd_herdr_plugin.go together", got, want)
	}
}

// TestOpenPaneScript_OverlayOmitsWorkspace pins overlay omits --workspace.
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
	for _, want := range []string{"--plugin nexus", "--entrypoint workspaces", "--placement overlay"} {
		if !strings.Contains(got, want) {
			t.Errorf("herdr argv %q missing %q", got, want)
		}
	}
}

// TestOpenPaneScript_OverlayCarriesFocus pins overlay carries --focus.
func TestOpenPaneScript_OverlayCarriesFocus(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"workspaces", "overlay"}, map[string]string{"HERDR_WORKSPACE_ID": "w8"})

	if got := e.herdrArgv(t); !strings.Contains(got, "--focus") {
		t.Errorf("overlay must still carry --focus; herdr argv = %q", got)
	}
}

// TestOpenPaneScript_TabCarriesWorkspace verifies tab is only placement with --workspace.
func TestOpenPaneScript_TabCarriesWorkspace(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"attach", "tab"}, map[string]string{"HERDR_WORKSPACE_ID": "w33"})

	got := e.herdrArgv(t)
	for _, want := range []string{
		"--plugin nexus", "--entrypoint attach", "--placement tab", "--workspace w33",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tab: herdr argv %q missing %q", got, want)
		}
	}
}

// TestOpenPaneScript_OverlayWithEnvOmitsWorkspace exercises overlay env-SET branch.
func TestOpenPaneScript_OverlayWithEnvOmitsWorkspace(t *testing.T) {
	e := newScriptEnv(t)
	e.run(t, "open-pane.sh", []string{"workspaces", "overlay"}, map[string]string{
		"HERDR_WORKSPACE_ID": "w8",
		"NEXUS_WORKSPACE":    "demo/api",
	})

	got := e.herdrArgv(t)
	if strings.Contains(got, "--workspace") {
		t.Errorf("overlay must not pass --workspace even when NEXUS_WORKSPACE is set; herdr argv = %q", got)
	}
	if strings.Contains(got, "w8") {
		t.Errorf("overlay must not pass the workspace id; herdr argv = %q", got)
	}
	if !strings.Contains(got, "--env NEXUS_WORKSPACE=demo/api") {
		t.Errorf("NEXUS_WORKSPACE must be forwarded as --env; herdr argv = %q", got)
	}
}

// TestOpenPaneScript_NewTab pins new-tab entrypoint forwards HERDR_WORKSPACE_ID.
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
	if h := e.herdrArgv(t); h != "" {
		t.Errorf("new-tab must not call herdr directly; herdr was called with %q", h)
	}
}
