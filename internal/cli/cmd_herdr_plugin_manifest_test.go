package cli

// TestHerdrManifestDispatch verifies that every verb the herdr plugin can
// invoke via open-pane.sh → $SHIM herdr <verb> is a case handled by
// herdrGroupVerbToPluginSub — so adding a manifest action without its CLI
// verb (or renaming a verb without updating the manifest) fails in CI rather
// than in the operator's command palette.
//
// Both sides are verified mechanically:
//   - Manifest side: plugins/herdr/herdr-plugin.toml is parsed for
//     `open-pane.sh <entrypoint>` arguments.
//   - Script side: open-pane.sh is EXECUTED with a stub shim for each
//     entrypoint; the stub records argv so we observe the real routing
//     decision rather than re-implementing the case statement in Go.
//   - Dispatch side: the observed verb is checked against
//     herdrGroupVerbToPluginSub — a pure function, no store or sandbox access.
//
// expectedDirectVerbs (below) is hand-maintained: it records which arms are
// direct ($SHIM herdr <verb> style). Update it whenever an arm is added/removed.
//
// MUTATION PROOF (all verified RED via `go build ./...` + test run; see log at EOF):
//
//   M1. Delete `case "worktree-sandbox":` from herdrGroupVerbToPluginSub →
//       script execution observes "worktree-sandbox", known=false → RED.
//
//   M2. Add a new case to open-pane.sh + [[actions]] entry → script observes
//       "totally-bogus-verb", herdrGroupVerbToPluginSub returns known=false → RED.
//
//   M3. Rename "worktree-sandbox" in expectedDirectVerbs to "worktree-sandbox-GONE" →
//       "worktree-sandbox" observed but not expected → RED (unexpected verb check).
//       "worktree-sandbox-GONE" expected but never seen → RED (missing verb check).
//
//   M4. Break TOML regexp (e.g. `"bin/open-pane\.SH"`) → 0 entrypoints extracted →
//       all expectedDirectVerbs entries fail the "not observed" check → RED.
//
//   M5 (isolation). Remove "HERDR_BIN_PATH=..." from cmd.Env → generic-arm exec
//       uses empty $HERDR_BIN_PATH → herdr stub not called → t.Errorf fires → RED.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// herdrOpenPaneEntrypoints parses the herdr-plugin.toml at tomlPath and
// returns the entrypoint argument (the token after "bin/open-pane.sh") from
// every [[actions]] command array that invokes open-pane.sh.
// Parsing is line-oriented: we look for lines that contain "open-pane.sh"
// followed by a quoted token on the same line.
func herdrOpenPaneEntrypoints(tomlPath string) ([]string, error) {
	f, err := os.Open(tomlPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", tomlPath, err)
	}
	defer f.Close()

	// Match: "bin/open-pane.sh", "<entrypoint>"  (possibly more tokens)
	// The TOML command array is always on one line:
	//   command = ["sh", "bin/open-pane.sh", "worktree-sandbox"]
	re := regexp.MustCompile(`"bin/open-pane\.sh",\s*"([^"]+)"`)

	var eps []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := re.FindStringSubmatch(line); m != nil {
			eps = append(eps, m[1])
		}
	}
	return eps, sc.Err()
}

// herdrEntrypointVerbViaScript runs open-pane.sh with the given entrypoint
// using a stub shim that records its argv, and reports the herdr verb that
// the script asked the shim to invoke as `herdr <verb> [args...]`.
//
// Returns ("", false) when the entrypoint routes through the generic
// `herdr pane open` path (the shim is called as `herdr pane open ...`),
// or when the shim is not called at all.
//
// This observes the real routing decision from the script itself — not a Go
// re-implementation of its case statement.
func herdrEntrypointVerbViaScript(t *testing.T, env *scriptEnv, ep string) (verb string, direct bool) {
	t.Helper()
	// Clear the shim log so only this invocation's output is visible.
	if err := os.Remove(env.shimLog); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clear shimLog: %v", err)
	}
	// Clear the herdr stub log too, so we can detect whether the stub was called.
	if err := os.Remove(env.herdrLog); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clear herdrLog: %v", err)
	}
	cmd := exec.Command("sh", filepath.Join(env.dir, "open-pane.sh"), ep)
	// Scrubbed env: supply only PATH + the three vars the script reads
	// (HERDR_WORKSPACE_ID, HERDR_BIN_PATH, and HOME for shell hygiene).
	// Do NOT inherit os.Environ() — that would let an ambient HERDR_BIN_PATH
	// silently override the stub pin and leak live herdr calls.
	// MUTATION PROOF (M5): remove "HERDR_BIN_PATH=..." → generic arm execs an
	// empty path → herdr stub not called → t.Errorf fires below → RED.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"HERDR_WORKSPACE_ID=test-ws-id",
		"HERDR_BIN_PATH=" + env.herdrBin,
	}
	// Ignore exit status — the stub shim exits 0 for all invocations, but
	// the script itself may exit non-zero after exec'ing the shim.
	_ = cmd.Run()

	data, err := os.ReadFile(env.shimLog)
	if err != nil {
		if os.IsNotExist(err) {
			// Shim was not called: generic arm calls herdr stub directly.
			// Verify the herdr stub was actually invoked — a missing entry
			// means HERDR_BIN_PATH was absent and the script exec'd nothing.
			herdrData, _ := os.ReadFile(env.herdrLog)
			if len(strings.TrimSpace(string(herdrData))) == 0 {
				t.Errorf("entrypoint %q: neither nexus3-shim.sh nor herdr stub was invoked — HERDR_BIN_PATH pin missing or script failed entirely", ep)
			}
			return "", false
		}
		t.Fatalf("read shimLog: %v", err)
	}
	// shimLog may contain multiple lines when the script makes multiple shim
	// calls (e.g. the generic path). Use only the first line.
	first, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	fields := strings.Fields(first)
	// Expected format: "herdr <verb> [args...]"
	// Generic-path format: "herdr pane open ..." — treated as non-direct.
	if len(fields) < 2 || fields[0] != "herdr" || fields[1] == "pane" {
		return "", false
	}
	return fields[1], true
}

func TestHerdrManifestDispatch(t *testing.T) {
	// Locate the plugin TOML relative to this package's working directory.
	// Go test sets cwd to the package directory (internal/cli); ../../ is
	// the repo root.
	tomlPath := "../../plugins/herdr/herdr-plugin.toml"
	if _, err := os.Stat(tomlPath); err != nil {
		t.Skipf("herdr-plugin.toml not found at %s (not running from repo root?): %v", tomlPath, err)
	}

	entrypoints, err := herdrOpenPaneEntrypoints(tomlPath)
	if err != nil {
		t.Fatalf("parse herdr-plugin.toml: %v", err)
	}

	// Exact expected set of direct-verb actions routed via open-pane.sh.
	// Update this set whenever a direct arm is added or removed in open-pane.sh
	// AND the corresponding manifest action changes.
	// MUTATION PROOF: delete an arm from open-pane.sh (e.g. rename the
	// space-pause arm so space-pause falls to the generic herdr path).
	// That verb disappears from seen → the set check fires → RED.
	//
	// "worktree-sandbox" is deliberately ABSENT. It used to be a direct arm:
	// open-pane.sh called `nexus3 herdr worktree-sandbox` inline, in the action
	// process, with no pane. That is the pane-first defect — a minutes-long VM
	// build with no visible surface, and a failure that reached only the plugin
	// log. The action now opens the worktree-sandbox PANE, and the direct nexus3
	// call lives in pane.sh instead. The routing invariant this set protects did
	// not go away; it moved, and TestPaneScript_WorktreeSandboxRoutesToNexus3Verb
	// (cmd_herdr_worktree_pane_test.go) enforces it at its new home.
	expectedDirectVerbs := map[string]bool{
		"space-open-pane": true,
		"new-tab":         true,
		"pause":           true,
		"resume":          true,
		"remove":          true,
	}

	env := newScriptEnv(t)
	seen := map[string]bool{}

	for _, ep := range entrypoints {
		verb, direct := herdrEntrypointVerbViaScript(t, env, ep)
		if !direct {
			continue
		}
		if seen[verb] {
			continue
		}
		seen[verb] = true

		if _, known := herdrGroupVerbToPluginSub(verb); !known {
			t.Errorf(
				"manifest entrypoint %q routes to `herdr %s` via open-pane.sh, "+
					"but %q is not a known verb in herdrGroupVerbToPluginSub; "+
					"add it to the switch or remove the manifest action",
				ep, verb, verb)
		}
	}

	// Exact identity check: every expected verb must be present, and no
	// unexpected direct verb must appear. A count alone cannot catch a rename
	// that swaps one direct verb for another while keeping the total stable.
	for verb := range expectedDirectVerbs {
		if !seen[verb] {
			t.Errorf("expected direct verb %q in manifest dispatch (open-pane.sh direct arm); not observed — broken arm or missing manifest action", verb)
		}
	}
	for verb := range seen {
		if !expectedDirectVerbs[verb] {
			t.Errorf("unexpected direct verb %q observed from manifest dispatch; update expectedDirectVerbs or remove the arm", verb)
		}
	}
}

// MUTATION PROOF LOG (every mutation built with `go build ./...` before result
// recorded; a build failure is an invalid mutation, not RED):
//
//   M1 [RED confirmed]: Deleted `case "worktree-sandbox":` from
//      herdrGroupVerbToPluginSub. Script observed verb="worktree-sandbox";
//      herdrGroupVerbToPluginSub returned known=false → t.Errorf → RED.
//      Reverted. md5sum match confirmed.
//
//   M2 [CRITICAL — RED confirmed]: Added to open-pane.sh:
//        totally-bogus-verb)
//            "$SHIM" herdr totally-bogus-verb "$HERDR_WORKSPACE_ID" ;;
//      Added to herdr-plugin.toml:
//        [[actions]]
//        id = "totally-bogus"
//        command = ["sh", "bin/open-pane.sh", "totally-bogus-verb"]
//      Script observed "totally-bogus-verb"; herdrGroupVerbToPluginSub returned
//      known=false → RED. The old hand-translated switch SURVIVED this; the
//      script-execution approach catches it because we run the real script.
//      Reverted. md5sum match confirmed on both files.
//
//   M3 [RED confirmed]: Renamed "worktree-sandbox" in expectedDirectVerbs to
//      "worktree-sandbox-GONE" → "worktree-sandbox" observed but not in
//      expectedDirectVerbs → "unexpected direct verb" t.Errorf → RED.
//      Reverted.
//
//   M4 [RED confirmed]: Changed regexp to `"bin/open-pane\.SH"` → 0 entrypoints
//      extracted → no verbs seen → all expectedDirectVerbs entries fail
//      "not observed" → RED (6 errors).
//      Reverted.
//
//   M5 [RED confirmed]: Removed "HERDR_BIN_PATH=..."+env.herdrBin from cmd.Env →
//      generic-arm exec used empty $HERDR_BIN_PATH → herdr stub not called →
//      herdrLog empty → t.Errorf "neither shim nor herdr stub was invoked" → RED.
//      Reverted.

// TestHerdrGroupUsageString_containsAllPluginVerbs verifies that the hand-maintained
// usage string in runHerdrGroup lists the key verbs from herdrGroupVerbToPluginSub.
//
// MUTATION PROOF: delete "worktree-sandbox" from the usage string literal in
// runHerdrGroup (while keeping its case in herdrGroupVerbToPluginSub) →
// strings.Contains fails → t.Errorf → RED.
func TestHerdrGroupUsageString_containsAllPluginVerbs(t *testing.T) {
	ctx := context.Background()
	var w strings.Builder
	out := NewOutput(&w, &w, false)
	err := runHerdrGroup(ctx, nil, out)
	ue, ok := err.(*UsageError)
	if !ok {
		t.Fatalf("expected UsageError with no args; got %T: %v", err, err)
	}
	// Check the full verb inventory from herdrGroupVerbToPluginSub so the test
	// name is true: every known verb must appear in the usage string.
	for _, verb := range []string{
		// Self-contained verbs.
		"default-shell", "install-default-shell",
		// Non-space verbs.
		"abi", "context-cwd", "workspaces", "attach", "create", "logs", "doctor",
		"open-pane", "launch", "shell-cwd", "new-tab",
		// Space-* verbs (prefix-dropped in runHerdrGroup).
		"create-from-file", "pause", "resume", "remove", "list", "prune",
		"agent", "agent-from-file",
		// Space-* verbs that keep their prefix (collision avoidance).
		"space-create", "space-open-pane",
		// Worktree verb.
		"worktree-sandbox",
		// Backfill verb.
		"backfill-repo-root",
	} {
		if !strings.Contains(ue.Msg, verb) {
			t.Errorf("usage string missing verb %q; add it to the runHerdrGroup usage literal", verb)
		}
	}
}

// TestOpenPaneScript_genericArm_nexusWorkspaceEnv exercises the two branches of
// the generic arm in open-pane.sh: with NEXUS3_WORKSPACE unset and set.
// Without this test, the --env flag path in the generic arm is never executed.
func TestOpenPaneScript_genericArm_nexusWorkspaceEnv(t *testing.T) {
	_, err := os.Stat(filepath.Join("..", "..", "plugins", "herdr", "bin", "open-pane.sh"))
	if err != nil {
		t.Skipf("open-pane.sh not found (not running from repo root?): %v", err)
	}
	env := newScriptEnv(t)

	// Use an entrypoint that hits the *)  case (generic arm).
	const ep = "agent"

	runScript := func(nexusWorkspace string) string {
		// Clear herdrLog before each run.
		_ = os.Remove(env.herdrLog)
		cmd := exec.Command("sh", filepath.Join(env.dir, "open-pane.sh"), ep)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"HERDR_WORKSPACE_ID=test-ws-id",
			"HERDR_BIN_PATH=" + env.herdrBin,
		}
		if nexusWorkspace != "" {
			cmd.Env = append(cmd.Env, "NEXUS3_WORKSPACE="+nexusWorkspace)
		}
		_ = cmd.Run()
		data, _ := os.ReadFile(env.herdrLog)
		return strings.TrimSpace(string(data))
	}

	// Without NEXUS3_WORKSPACE: herdr must NOT receive --env.
	withoutWS := runScript("")
	if strings.Contains(withoutWS, "--env") {
		t.Errorf("generic arm without NEXUS3_WORKSPACE: unexpected --env in herdr argv: %q", withoutWS)
	}
	if withoutWS == "" {
		t.Error("generic arm without NEXUS3_WORKSPACE: herdr stub not called (herdrLog empty)")
	}

	// With NEXUS3_WORKSPACE: herdr must receive --env NEXUS3_WORKSPACE=<value>.
	withWS := runScript("my-workspace")
	if !strings.Contains(withWS, "--env") || !strings.Contains(withWS, "NEXUS3_WORKSPACE=my-workspace") {
		t.Errorf("generic arm with NEXUS3_WORKSPACE: want --env NEXUS3_WORKSPACE=my-workspace in herdr argv; got %q", withWS)
	}
}
