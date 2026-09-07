package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestVerbInventory fails if the set of registered command names diverges from
// testdata/verb-inventory.golden. Run with UPDATE_GOLDEN=1 to regenerate.
func TestVerbInventory(t *testing.T) {
	all := All()
	names := make([]string, 0, len(all))
	for _, c := range all {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	got := strings.Join(names, "\n") + "\n"

	goldenPath := filepath.Join("testdata", "verb-inventory.golden")

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden missing at %s; run: UPDATE_GOLDEN=1 make test GOTEST_P=1 GOTEST_PARALLEL=1", goldenPath)
	}
	if string(want) != got {
		t.Errorf("verb registry diverges from %s\nwant:\n%sgot:\n%s\nTo accept: UPDATE_GOLDEN=1 make test GOTEST_P=1 GOTEST_PARALLEL=1", goldenPath, want, got)
	}
}

func TestSpecCoversAllVisibleVerbs(t *testing.T) {
	specPath := filepath.Join("..", "..", "doc", "specs", "cli-surface.md")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("spec missing at %s: %v", specPath, err)
	}
	spec := string(raw)

	for _, c := range All() {
		if c.Hidden {
			continue
		}
		marker := "## " + c.Name
		if !strings.Contains(spec, marker) {
			t.Errorf("verb %q registered but not in %s (add a '## %s' section)", c.Name, specPath, c.Name)
		}
	}
}
