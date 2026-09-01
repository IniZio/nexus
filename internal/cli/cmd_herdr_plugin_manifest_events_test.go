//go:build herdr_live

// D-HSH-20 regression test: validate plugin manifest event names against the
// REAL herdr binary.
//
// # Why this test exists
//
// For months "worktree_removed" (underscore) sat in the plugin manifest while
// herdr's registry keyed on "worktree.removed" (dot). No test caught it because
// every test used a mock that accepts any string. Only running the real binary
// exposed the warning. This is the repo's documented signature failure: a checker
// that shares the broken mechanism it is meant to catch.
//
// This test closes the gap by cross-checking every [[events]] on = "..." value
// in plugins/herdr/herdr-plugin.toml against the authoritative event registry
// emitted by `herdr api schema --json`. A dot-to-underscore typo anywhere in
// the manifest now makes this test RED.
//
// # Oracle choice: herdr api schema --json over herdr plugin list
//
// herdr api schema --json is preferred because:
//  1. Binary-only: no running herdr daemon required → Tier 1, CI-runnable.
//  2. Machine-parseable authoritative JSON: the Subscription oneOf carries all
//     27 dot-named event types as "const" values.
//  3. herdr plugin list requires: daemon running + plugin installed + daemon
//     must have loaded the plugin. That is Tier 2 (needs a live herdr session).
//
// # Tier
//
// Tier 1 — binary-only. Requires only the herdr binary; boots no VM and needs
// no running herdr daemon. Runs in CI under the herdr-live job.
//
// # Mutation proof (AC-19e)
//
// Set NEXUS3_TEST_MANIFEST to the fixture paths in internal/cli/testdata/ to
// verify RED/GREEN behaviour without touching the live manifest:
//
//	NEXUS3_TEST_MANIFEST=internal/cli/testdata/herdr-plugin-invalid-events.toml \
//	  go test -tags herdr_live ./internal/cli/ -run TestHerdrPluginManifest_EventNamesValid -v
//	# FAIL: manifest event "worktree_removed" is NOT in herdr's event registry
//
//	NEXUS3_TEST_MANIFEST=internal/cli/testdata/herdr-plugin-valid-events.toml \
//	  go test -tags herdr_live ./internal/cli/ -run TestHerdrPluginManifest_EventNamesValid -v
//	# PASS
//
// TestHerdrPluginManifest_EventNamesValid_MutationProof runs both cases in-process
// and asserts the substitution count, satisfying AC-19e without requiring the caller
// to run the test twice.
package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestHerdrPluginManifest_EventNamesValid asserts that every [[events]] on = "..."
// value in plugins/herdr/herdr-plugin.toml is accepted by the real herdr binary.
//
// The bar (AC-19d): this test FAILS if the manifest says "worktree_removed".
// If it passes against both "worktree.removed" and "worktree_removed", it is
// the same mock-shaped non-test that let D-HSH-20 ship.
func TestHerdrPluginManifest_EventNamesValid(t *testing.T) {
	// Locate herdr binary. Fail loudly — a skip here recreates the never-runs
	// problem this test exists to fix. The CI install step is the first line of
	// defence; this is the second.
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		t.Fatalf("herdr not found in PATH: %v\n"+
			"  install with: curl -fsSL https://herdr.dev/install.sh | sh\n"+
			"  (see also .github/workflows/ci.yml herdr-live job)", err)
	}
	out, verErr := exec.Command(herdrBin, "--version").CombinedOutput()
	if verErr != nil {
		t.Fatalf("herdr --version: %v\n%s", verErr, out)
	}
	t.Logf("herdr binary: %s (%s)", herdrBin, strings.TrimSpace(string(out)))

	// Determine manifest path. NEXUS3_TEST_MANIFEST overrides for fixture-based
	// mutation proofing — do NOT use the live manifest for that; write to testdata/.
	manifestPath := os.Getenv("NEXUS3_TEST_MANIFEST")
	if manifestPath == "" {
		manifestPath = filepath.Join(moduleRoot(t), "plugins", "herdr", "herdr-plugin.toml")
	}

	// Extract [[events]] on = "..." values from the manifest.
	eventNames := parseManifestEventNames(t, manifestPath)
	t.Logf("manifest %s: found %d event(s): %v", filepath.Base(manifestPath), len(eventNames), eventNames)

	if len(eventNames) == 0 {
		// No events in manifest is valid (current state of the real manifest).
		// Log clearly so readers know the test ran, not that it was skipped.
		t.Logf("no [[events]] entries in manifest — nothing to validate (test ran, all zero checks passed)")
		return
	}

	// Get the authoritative event registry from herdr api schema --json.
	validEvents := herdrSchemaEventNames(t, herdrBin)
	t.Logf("herdr schema: %d valid event name(s)", len(validEvents))

	// Assert every manifest event is in the registry.
	failed := false
	for _, name := range eventNames {
		if _, ok := validEvents[name]; !ok {
			t.Errorf("manifest event %q is NOT in herdr's event registry\n"+
				"  D-HSH-20 class: use dots not underscores (e.g. worktree.removed, not worktree_removed)\n"+
				"  known events: %v", name, sortedEventNames(validEvents))
			failed = true
		}
	}
	if !failed {
		t.Logf("all %d manifest event(s) are valid herdr event names", len(eventNames))
	}
}

// TestHerdrPluginManifest_EventNamesValid_MutationProof is AC-19e.
//
// It runs the same parser and oracle check as TestHerdrPluginManifest_EventNamesValid
// against both testdata fixtures, asserting:
//
//  1. The invalid fixture (worktree_removed) causes at least one rejection. — RED.
//  2. The substitution count is non-zero (no-op patches must not read as a pass).
//  3. The valid fixture (worktree.removed) produces zero rejections. — GREEN.
//
// This proves the test is sensitive to the exact bug class from D-HSH-20 without
// touching the live manifest.
func TestHerdrPluginManifest_EventNamesValid_MutationProof(t *testing.T) {
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		t.Fatalf("herdr not found in PATH: %v", err)
	}

	root := moduleRoot(t)
	invalidFixture := filepath.Join(root, "internal", "cli", "testdata", "herdr-plugin-invalid-events.toml")
	validFixture := filepath.Join(root, "internal", "cli", "testdata", "herdr-plugin-valid-events.toml")

	// AC-19e: assert the substitution count. A no-op mutation (fixture does not
	// actually contain the mutated name) must not read as a pass.
	invalidBytes, readErr := os.ReadFile(invalidFixture)
	if readErr != nil {
		t.Fatalf("read invalid fixture: %v", readErr)
	}
	substitutionCount := strings.Count(string(invalidBytes), "worktree_removed")
	if substitutionCount == 0 {
		t.Fatalf("AC-19e: no-op patch — invalid fixture contains 0 occurrences of "+
			"'worktree_removed'; the mutation is not applied and cannot be mutation-proven")
	}
	t.Logf("AC-19e substitution count: %d occurrence(s) of 'worktree_removed' in invalid fixture", substitutionCount)

	validEvents := herdrSchemaEventNames(t, herdrBin)

	// --- RED check: invalid fixture must produce at least one rejection ---
	t.Run("RED/worktree_removed", func(t *testing.T) {
		names := parseManifestEventNames(t, invalidFixture)
		if len(names) == 0 {
			t.Fatalf("invalid fixture has no [[events]] entries — cannot mutation-prove RED")
		}
		rejections := 0
		for _, name := range names {
			if _, ok := validEvents[name]; !ok {
				rejections++
				t.Logf("confirmed: %q is NOT in herdr's registry (expected — this is the D-HSH-20 bug)", name)
			}
		}
		if rejections == 0 {
			t.Fatalf("mutation proof BROKEN: herdr accepted all events in invalid fixture — "+
				"the test would NOT have caught D-HSH-20\n"+
				"  invalid fixture events: %v\n"+
				"  valid events: %v", names, sortedEventNames(validEvents))
		}
		t.Logf("RED confirmed: %d event(s) rejected from invalid fixture", rejections)
	})

	// --- GREEN check: valid fixture must produce zero rejections ---
	t.Run("GREEN/worktree.removed", func(t *testing.T) {
		names := parseManifestEventNames(t, validFixture)
		if len(names) == 0 {
			t.Fatalf("valid fixture has no [[events]] entries — cannot mutation-prove GREEN")
		}
		for _, name := range names {
			if _, ok := validEvents[name]; !ok {
				t.Errorf("GREEN broken: %q is NOT in herdr's registry — valid fixture must pass", name)
			}
		}
		t.Logf("GREEN confirmed: all %d event(s) in valid fixture are accepted", len(names))
	})
}

// parseManifestEventNames reads a herdr-plugin.toml and returns every
// [[events]] on = "..." value it contains.
//
// Parsing is line-by-line with no TOML library. A TOML dependency in this test
// would introduce exactly the kind of indirection this test exists to remove —
// a library that accepts "worktree_removed" trivially as a string value would
// hide the very bug we are checking for. The format of [[events]] entries is
// regular enough that simple string scanning is correct and transparent.
func parseManifestEventNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open manifest %s: %v", path, err)
	}
	defer f.Close()

	var names []string
	inEventsTable := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[[events]]" {
			inEventsTable = true
			continue
		}
		// Any other [[...]] header exits the events context.
		if strings.HasPrefix(line, "[[") && line != "[[events]]" {
			inEventsTable = false
		}
		if inEventsTable && strings.HasPrefix(line, "on") {
			// Parse: on = "worktree.removed"  or  on = 'worktree.removed'
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				val = strings.Trim(val, `"'`)
				if val != "" {
					names = append(names, val)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan manifest %s: %v", path, err)
	}
	return names
}

// herdrSchemaEventNames calls `herdr api schema --json` and returns the set of
// valid event type names from the Subscription oneOf in the request schema.
//
// The schema emits a request schema whose $defs["Subscription"] carries a oneOf
// array; each variant has properties.type.const set to a dot-named event name
// (e.g. "worktree.removed"). This is the authoritative source — the same registry
// that herdr uses to validate plugin manifests at install time.
func herdrSchemaEventNames(t *testing.T, herdrBin string) map[string]struct{} {
	t.Helper()
	out, err := exec.Command(herdrBin, "api", "schema", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr api schema --json: %v\n%s", err, out)
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
		t.Fatalf("parse herdr api schema --json: %v\nraw (first 512): %.512s", err, out)
	}

	sub, ok := schema.Schemas.Request.Defs["Subscription"]
	if !ok {
		t.Fatalf("herdr api schema --json: no Subscription def in schemas.request.$defs — schema shape may have changed")
	}

	names := make(map[string]struct{}, len(sub.OneOf))
	for _, variant := range sub.OneOf {
		if c := variant.Properties.Type.Const; c != "" {
			names[c] = struct{}{}
		}
	}

	if len(names) == 0 {
		t.Fatalf("herdr api schema --json: Subscription def has no oneOf variants — schema shape may have changed; raw (first 512): %.512s", out)
	}
	return names
}

// sortedEventNames returns the keys of the map sorted for deterministic error output.
func sortedEventNames(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
