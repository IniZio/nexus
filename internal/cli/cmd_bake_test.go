package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/cli/nexusfile"
)

// writeTestNexusfile writes a minimal Nexusfile to a temp dir and returns its path.
func writeTestNexusfile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Nexusfile")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write Nexusfile: %v", err)
	}
	return path
}

const testNexusfileContent = `
[dev]
bake = ["echo bake-one", "echo bake-two"]
up   = ["echo up-one"]
down = ["echo down-one"]

[prod]
bake = ["echo prod-bake"]
up   = ["echo prod-up"]
down = []
`

// ── parseNexusVerbFlags ───────────────────────────────────────────────────────

func TestParseNexusVerbFlags_Defaults(t *testing.T) {
	f, positional, err := parseNexusVerbFlags("bake", []string{"proj/sb"})
	if err != nil {
		t.Fatalf("parseNexusVerbFlags: %v", err)
	}
	if f.nexusfilePath != defaultNexusfile {
		t.Errorf("nexusfilePath: want %q, got %q", defaultNexusfile, f.nexusfilePath)
	}
	if f.section != defaultNexusSection {
		t.Errorf("section: want %q, got %q", defaultNexusSection, f.section)
	}
	if len(positional) != 1 || positional[0] != "proj/sb" {
		t.Errorf("positional: want [proj/sb], got %v", positional)
	}
}

func TestParseNexusVerbFlags_Overrides(t *testing.T) {
	f, positional, err := parseNexusVerbFlags("bake", []string{
		"--nexusfile", "/tmp/Nexusfile",
		"--section", "prod",
		"my-sandbox",
	})
	if err != nil {
		t.Fatalf("parseNexusVerbFlags: %v", err)
	}
	if f.nexusfilePath != "/tmp/Nexusfile" {
		t.Errorf("nexusfilePath: want /tmp/Nexusfile, got %q", f.nexusfilePath)
	}
	if f.section != "prod" {
		t.Errorf("section: want prod, got %q", f.section)
	}
	if len(positional) != 1 || positional[0] != "my-sandbox" {
		t.Errorf("positional: want [my-sandbox], got %v", positional)
	}
}

// ── nexusVerbRun UsageError ───────────────────────────────────────────────────

func TestBakeRun_UsageError_NoRef(t *testing.T) {
	out, _, _ := capture(false)
	bakeRun := nexusVerbRun("bake", func(s nexusfile.Section) []string { return s.Bake })
	err := bakeRun(context.Background(), []string{}, out)
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want UsageError, got %T: %v", err, err)
	}
}

func TestUpRun_UsageError_NoRef(t *testing.T) {
	out, _, _ := capture(false)
	upRun := nexusVerbRun("up", func(s nexusfile.Section) []string { return s.Up })
	err := upRun(context.Background(), []string{}, out)
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want UsageError, got %T: %v", err, err)
	}
}

func TestDownRun_UsageError_NoRef(t *testing.T) {
	out, _, _ := capture(false)
	downRun := nexusVerbRun("down", func(s nexusfile.Section) []string { return s.Down })
	err := downRun(context.Background(), []string{}, out)
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want UsageError, got %T: %v", err, err)
	}
}

// ── Nexusfile resolution + section selection ──────────────────────────────────

// TestNexusfileResolution_Bake verifies that the bake verb resolves the correct
// command list from the Nexusfile without needing a live sandbox. It calls
// runNexusVerbWithSvc with a nil service (the function returns an error before
// touching the service when the Nexusfile yields a non-empty list — but here
// we only want to verify the parser-to-command extraction path). Instead, we
// test the extraction by loading the Nexusfile directly and checking what would
// be passed.
func TestNexusfileResolution_SectionCommands(t *testing.T) {
	path := writeTestNexusfile(t, testNexusfileContent)

	tests := []struct {
		section  string
		verb     string
		selector func(nexusfile.Section) []string
		want     []string
	}{
		{"dev", "bake", func(s nexusfile.Section) []string { return s.Bake }, []string{"echo bake-one", "echo bake-two"}},
		{"dev", "up", func(s nexusfile.Section) []string { return s.Up }, []string{"echo up-one"}},
		{"dev", "down", func(s nexusfile.Section) []string { return s.Down }, []string{"echo down-one"}},
		{"prod", "bake", func(s nexusfile.Section) []string { return s.Bake }, []string{"echo prod-bake"}},
		{"prod", "down", func(s nexusfile.Section) []string { return s.Down }, nil},
	}

	for _, tc := range tests {
		t.Run(tc.section+"/"+tc.verb, func(t *testing.T) {
			nf, err := nexusfile.Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			sec, err := nf.Section(tc.section)
			if err != nil {
				t.Fatalf("Section: %v", err)
			}
			got := tc.selector(sec)
			if len(got) != len(tc.want) {
				t.Fatalf("commands: want %v, got %v", tc.want, got)
			}
			for i, cmd := range tc.want {
				if got[i] != cmd {
					t.Errorf("commands[%d]: want %q, got %q", i, cmd, got[i])
				}
			}
		})
	}
}

func TestNexusfileResolution_MissingSection_Error(t *testing.T) {
	path := writeTestNexusfile(t, testNexusfileContent)
	out, _, _ := capture(false)
	f := nexusVerbFlags{nexusfilePath: path, section: "nonexistent"}
	err := runNexusVerbWithSvc(
		context.Background(), "bake", "proj/sb", f,
		func(s nexusfile.Section) []string { return s.Bake },
		out, nil,
	)
	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("want CodedError, got %T: %v", err, err)
	}
	if coded.Code != ErrCodeInvalidArgument {
		t.Errorf("code: want %q, got %q", ErrCodeInvalidArgument, coded.Code)
	}
}

func TestNexusfileResolution_MissingFile_Error(t *testing.T) {
	out, _, _ := capture(false)
	f := nexusVerbFlags{nexusfilePath: "/no/such/Nexusfile", section: "dev"}
	err := runNexusVerbWithSvc(
		context.Background(), "bake", "proj/sb", f,
		func(s nexusfile.Section) []string { return s.Bake },
		out, nil,
	)
	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("want CodedError, got %T: %v", err, err)
	}
}
