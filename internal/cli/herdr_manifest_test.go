package cli

import (
	"os"
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
