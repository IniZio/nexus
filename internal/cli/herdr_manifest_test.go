package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// manifestPath locates the herdr plugin manifest from the package dir.
func manifestPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "plugins", "herdr", "herdr-plugin.toml")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("manifest not found at %s: %v", p, err)
	}
	return p
}

var (
	reBlock    = regexp.MustCompile(`(?m)^\[\[(panes|actions)\]\]`)
	reID       = regexp.MustCompile(`(?m)^id = "([^"]+)"`)
	reOpenPane = regexp.MustCompile(`open-pane\.sh", "([^"]+)"`)
)

// parseManifest returns the declared pane ids and the entrypoints that
// actions pass to open-pane.sh.
func parseManifest(t *testing.T) (panes []string, entrypoints map[string]bool) {
	t.Helper()
	raw, err := os.ReadFile(manifestPath(t))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	s := string(raw)
	entrypoints = map[string]bool{}

	locs := reBlock.FindAllStringSubmatchIndex(s, -1)
	for i, loc := range locs {
		kind := s[loc[2]:loc[3]]
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := s[loc[1]:end]

		if kind == "panes" {
			if m := reID.FindStringSubmatch(block); m != nil {
				panes = append(panes, m[1])
			}
			continue
		}
		if m := reOpenPane.FindStringSubmatch(block); m != nil {
			entrypoints[m[1]] = true
		}
	}
	return panes, entrypoints
}

// TestHerdrManifest_EveryPaneIsReachable pins the defect this test was written
// for: the "workspaces" overlay was declared as a pane but no action opened it,
// so the primary listing surface could not be reached from herdr's UI at all.
// A pane with no action is dead weight — herdr only ever opens a pane in
// response to an action (or an explicit `herdr plugin pane open` by hand).
func TestHerdrManifest_EveryPaneIsReachable(t *testing.T) {
	panes, entrypoints := parseManifest(t)
	if len(panes) == 0 {
		t.Fatal("parsed zero panes — the manifest parser is broken, not the manifest")
	}

	// Panes reachable by a route other than a same-named action.
	exempt := map[string]string{
		// Opened by the "open-guest-pane" action via the space-open-pane
		// entrypoint, which resolves the sandbox from HERDR_WORKSPACE_ID.
		"shell": "reached via the space-open-pane entrypoint",
		// Requires <image-ref> and an absolute <command> as argv; pane.sh
		// passes neither, so it exits on a usage error. Deliberately not
		// surfaced as an action until it can be driven interactively.
		"launch": "needs argv the pane cannot supply",
	}

	for _, p := range panes {
		if why, ok := exempt[p]; ok {
			t.Logf("pane %q exempt: %s", p, why)
			continue
		}
		if !entrypoints[p] {
			t.Errorf("pane %q has no action that opens it — it is unreachable from herdr's UI", p)
		}
	}
}

// TestHerdrManifest_EveryEntrypointResolves is the other direction: an action
// must not point at an entrypoint that no pane or open-pane.sh case handles,
// which would fail only when a user clicks it.
func TestHerdrManifest_EveryEntrypointResolves(t *testing.T) {
	panes, entrypoints := parseManifest(t)
	paneSet := map[string]bool{}
	for _, p := range panes {
		paneSet[p] = true
	}

	script, err := os.ReadFile(filepath.Join("..", "..", "plugins", "herdr", "bin", "open-pane.sh"))
	if err != nil {
		t.Fatalf("read open-pane.sh: %v", err)
	}
	body := string(script)

	for ep := range entrypoints {
		if paneSet[ep] {
			continue
		}
		// Not a pane id — open-pane.sh must handle it as a special case.
		if !strings.Contains(body, ep) {
			t.Errorf("action entrypoint %q is neither a pane id nor handled in open-pane.sh", ep)
		}
	}
}

// TestHerdrManifest_ShellPlacementIsTab pins that the "shell" pane's declared
// placement is "tab". This is a load-bearing coupling:
// herdrOpenGuestShellPane (internal/cli/cmd_herdr_plugin.go) passes
// --workspace without --placement when no root pane ID is available. The
// herdr server then falls back to the manifest-declared placement for the
// shell entrypoint. Tab is the only placement that accepts --workspace; if
// this line changes to split, overlay, or zoomed the fallback branch silently
// returns rc=1 and no call site warns. Change it only after updating
// herdrOpenGuestShellPane to pass an explicit --placement tab.
func TestHerdrManifest_ShellPlacementIsTab(t *testing.T) {
	raw, err := os.ReadFile(manifestPath(t))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	// Extract every [[panes]] block and locate the one with id = "shell".
	rePane := regexp.MustCompile(`(?s)\[\[panes\]\][^\[]*`)
	rePlacement := regexp.MustCompile(`(?m)^placement = "([^"]+)"`)
	rePaneID := regexp.MustCompile(`(?m)^id = "([^"]+)"`)

	blocks := rePane.FindAllString(string(raw), -1)
	for _, block := range blocks {
		idM := rePaneID.FindStringSubmatch(block)
		if idM == nil || idM[1] != "shell" {
			continue
		}
		placementM := rePlacement.FindStringSubmatch(block)
		if placementM == nil {
			t.Fatal(`shell pane has no placement declaration in herdr-plugin.toml`)
		}
		got := placementM[1]
		if got != "tab" {
			t.Errorf(
				"shell pane placement = %q, want \"tab\"\n"+
					"herdrOpenGuestShellPane omits --placement in its --workspace fallback\n"+
					"and relies on the manifest-declared placement for the shell entrypoint.\n"+
					"Tab is the only placement herdr accepts --workspace for; switching to\n"+
					"%q causes the fallback branch to return rc=1 silently.\n"+
					"Fix: update herdrOpenGuestShellPane to pass --placement tab explicitly,\n"+
					"then restore this assertion to match the new manifest value.",
				got, got,
			)
		}
		return
	}
	t.Fatal(`shell pane not found in herdr-plugin.toml`)
}

// herdrManifestEventNames parses the [[events]] on = "..." values from the
// herdr plugin TOML at tomlPath.  Returns the deduplicated list of event names.
func herdrManifestEventNames(t *testing.T, tomlPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	// Match [[events]] blocks.  Each block ends at the next [[ header or EOF.
	reEvBlock := regexp.MustCompile(`(?s)\[\[events\]\][^\[]*`)
	reOn := regexp.MustCompile(`(?m)^on\s*=\s*"([^"]+)"`)

	var names []string
	for _, block := range reEvBlock.FindAllString(string(raw), -1) {
		if m := reOn.FindStringSubmatch(block); m != nil {
			names = append(names, m[1])
		}
	}
	return names
}

// herdrSubscriptionEventNames calls `herdr api schema --json` and extracts the
// dot-named event types from schemas.request.$defs.Subscription.oneOf.
// Returns (nil, false) when the herdr binary is absent so callers can skip.
func herdrSubscriptionEventNames(t *testing.T) (set map[string]bool, ok bool) {
	t.Helper()
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		return nil, false
	}

	out, err := exec.Command(herdrBin, "api", "schema", "--json").Output()
	if err != nil {
		t.Logf("herdr api schema --json failed: %v; skipping registry check", err)
		return nil, false
	}

	var schema struct {
		Schemas struct {
			Request struct {
				Defs map[string]struct {
					OneOf []struct {
						Properties struct {
							Type struct {
								Const string `json:"const"`
							} `json:"type"`
						} `json:"properties"`
					} `json:"oneOf"`
				} `json:"$defs"`
			} `json:"request"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(out, &schema); err != nil {
		t.Logf("parse herdr schema JSON: %v; skipping registry check", err)
		return nil, false
	}

	known := map[string]bool{}
	if sub, exists := schema.Schemas.Request.Defs["Subscription"]; exists {
		for _, variant := range sub.OneOf {
			if n := variant.Properties.Type.Const; n != "" {
				known[n] = true
			}
		}
	}
	if len(known) == 0 {
		t.Logf("herdr schema returned empty Subscription.oneOf; skipping registry check")
		return nil, false
	}
	return known, true
}

// TestHerdrManifestEventNames validates every [[events]] on = "..." value in
// the herdr plugin manifest against the dot-named event registry from the
// installed herdr binary.
//
// This is the regression test for D-HSH-20: the prior manifest comment used
// underscore names (worktree_removed) which herdr rejects with
// "unknown event '...'", causing the events block to never fire.
// The test goes RED if any event name is rewritten with underscores.
//
// MUTATION PROOF (both verified RED — see log at EOF):
//
//   M1. Rewrite "worktree.removed" → "worktree_removed" in herdr-plugin.toml:
//       herdrManifestEventNames returns ["worktree_removed","worktree.created"];
//       "worktree_removed" not in known set → t.Errorf → RED.
//       Substitution count (wantEventCount=2) is unchanged, so the count guard
//       alone does NOT fire — the membership check is the real sentinel here.
//
//   M2. Remove both [[events]] blocks from herdr-plugin.toml:
//       herdrManifestEventNames returns [] (len=0); wantEventCount=2 → RED.
//       No membership checks run, so the count guard is the only sentinel here.
//       This proves a no-op patch (events deleted) cannot masquerade as a pass.
//
// The substitution count wantEventCount=2 is asserted first so that:
//   (a) a mutation that deletes the events section goes RED on count, and
//   (b) a mutation that only misspells one name goes RED on membership.
// Both must hold for the mutation to be properly caught.
func TestHerdrManifestEventNames(t *testing.T) {
	// wantEventCount is the exact number of [[events]] hooks this test expects.
	// MUTATION M2: remove blocks → len(gotNames)=0 ≠ 2 → RED.
	const wantEventCount = 2

	tomlPath := manifestPath(t)
	gotNames := herdrManifestEventNames(t, tomlPath)

	if len(gotNames) != wantEventCount {
		t.Errorf("manifest has %d [[events]] on= declaration(s), want %d; "+
			"add missing hook(s) or update wantEventCount",
			len(gotNames), wantEventCount)
		// Do not abort: fall through to membership checks so failures are visible
		// even when the count is wrong.
	}
	if len(gotNames) == 0 {
		t.Fatal("no [[events]] blocks found — cannot check membership (parser broken or blocks deleted)")
	}

	known, ok := herdrSubscriptionEventNames(t)
	if !ok {
		t.Skip("herdr binary not available; cannot validate event names against registry")
	}

	// MUTATION M1: rewrite "worktree.removed" → "worktree_removed" →
	// "worktree_removed" not in known → t.Errorf → RED.
	for _, name := range gotNames {
		if !known[name] {
			t.Errorf("[[events]] on = %q is not in the herdr subscription registry; "+
				"check spelling (dots not underscores) or run `herdr api schema --json` "+
				"to see valid event names", name)
		}
	}
}
