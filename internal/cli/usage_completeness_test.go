package cli

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every flag `sandbox create` accepts must appear in the usage string it
// prints.
//
// This is not a style rule. The usage string is the CLI's only self-
// documentation — there is no `--help` for hand-rolled groups — and
// scripts/docs/extract-surface.sh derives the documented surface from it. A
// flag missing here is invisible three times over: to an operator reading the
// error, to the surface inventory, and to anyone auditing what shipped.
//
// It found five real omissions when written: --mount and --mount-named (both
// flagship features, live host mounts and named volumes), --egress and
// --allow-host (egress control), and --repo — which nexus3's OWN error message
// instructs the operator to pass:
//
//	GitHub credential would be unbounded (D-PD-36): pass --repo owner/name …
//
// A flag the product tells you to use, that the product does not list, is the
// clearest possible case for pinning this mechanically.
func TestSandboxCreateUsage_ListsEveryAcceptedFlag(t *testing.T) {
	src, err := os.ReadFile("cmd_sandbox.go")
	if err != nil {
		t.Fatalf("read cmd_sandbox.go: %v", err)
	}
	text := string(src)

	body, ok := sliceBetween(text, "f := sandboxCreateFlags{}", "return f, nil")
	if !ok {
		t.Fatal("could not locate the sandbox create flag parser; update this test's anchors")
	}
	usage, ok := sliceBetween(text, "usage: sandbox create ", `"}`)
	if !ok {
		t.Fatal("could not locate the sandbox create usage string")
	}

	caseFlag := regexp.MustCompile(`case "(--[a-z0-9-]+)"`)
	seen := map[string]bool{}
	var accepted []string
	for _, m := range caseFlag.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			accepted = append(accepted, m[1])
		}
	}
	if len(accepted) == 0 {
		t.Fatal("found no flags in the parser; the anchors are wrong, not the code")
	}
	sort.Strings(accepted)

	// Compare as whole tokens, not substrings. A naive strings.Contains check
	// reports --mount as present because --mount-named contains it, which is
	// exactly how this test first passed against a usage string that had lost
	// --mount.
	flagToken := regexp.MustCompile(`--[a-z0-9-]+`)
	inUsage := map[string]bool{}
	for _, tok := range flagToken.FindAllString(usage, -1) {
		inUsage[tok] = true
	}

	var missing []string
	for _, f := range accepted {
		if !inUsage[f] {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		t.Errorf("sandbox create accepts %v but its usage string does not list them; "+
			"they are undiscoverable to operators and invisible to extract-surface.sh", missing)
	}
}

// sliceBetween returns the text between the first occurrence of open and the
// next occurrence of close after it.
func sliceBetween(s, open, close string) (string, bool) {
	i := strings.Index(s, open)
	if i < 0 {
		return "", false
	}
	i += len(open)
	j := strings.Index(s[i:], close)
	if j < 0 {
		return "", false
	}
	return s[i : i+j], true
}
