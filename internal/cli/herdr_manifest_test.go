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

// Every pane must have an action that opens it, or it's unreachable.
func TestHerdrManifest_EveryPaneIsReachable(t *testing.T) {
	panes, entrypoints := parseManifest(t)
	if len(panes) == 0 {
		t.Fatal("parsed zero panes — the manifest parser is broken, not the manifest")
	}

	exempt := map[string]string{
		"shell":  "reached via space-open-pane entrypoint",
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

// Every entrypoint must resolve to a pane or open-pane.sh case.
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
		if !strings.Contains(body, ep) {
			t.Errorf("action entrypoint %q is neither a pane id nor handled in open-pane.sh", ep)
		}
	}
}

// Shell placement must be "tab" (load-bearing: herdrOpenGuestShellPane fallback).
func TestHerdrManifest_ShellPlacementIsTab(t *testing.T) {
	raw, err := os.ReadFile(manifestPath(t))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

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

func herdrManifestEventNames(t *testing.T, tomlPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

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

// D-HSH-20 regression: event names must use dots, not underscores.
// Count guard catches deletions; membership catches misspellings (mutations verified RED).
func TestHerdrManifestEventNames(t *testing.T) {
	// wantEventCount is the exact number of [[events]] hooks this test expects.
	// MUTATION M2: remove blocks → len(gotNames)=0 ≠ 3 → RED.
	const wantEventCount = 3

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
