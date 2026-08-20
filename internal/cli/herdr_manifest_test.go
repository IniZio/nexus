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
