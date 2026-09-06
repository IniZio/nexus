package main

import (
	"encoding/json"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// TestParseBuilderToolRecipe_RoundTrip is the primary guard for the
// --tool-recipe argv decode (cmd/nexus3-agent/main.go lines 304-309, now
// delegated to parseBuilderToolRecipe). It proves that a fully-populated
// ToolRecipe — including the SHA256ByArch map and nested Packages/Symlinks
// slices — survives the JSON encode/decode round-trip intact. A decode that
// silently produced a zero-value recipe would fail this test.
//
// This test is compiled by `make test` (no build tag).
func TestParseBuilderToolRecipe_RoundTrip(t *testing.T) {
	want := cred.ToolRecipe{
		BinPath: "/usr/local/bin/cursor-agent",
		Packages: []cred.RecipePackage{
			{
				Kind:        cred.RecipeKindTarball,
				Name:        "cursor-agent",
				Version:     "2026.08.25-3e8eec8",
				URLTemplate: "https://downloads.cursor.com/production/{VERSION}/linux/{ARCH}/cursor-agent",
				SHA256ByArch: map[string]string{
					"x64":   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
					"arm64": "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
				},
				InstallDir: "/usr/local/share/cursor-agent/versions/{VERSION}",
				Symlinks: []cred.RecipeSymlink{
					{
						LinkPath:   "/usr/local/bin/cursor-agent",
						TargetPath: "/usr/local/share/cursor-agent/versions/{VERSION}/agent-cli",
					},
				},
			},
		},
	}

	// Encode to JSON (as the host vmbuilder does) then decode (as the guest does).
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	got, err := parseBuilderToolRecipe(string(encoded))
	if err != nil {
		t.Fatalf("parseBuilderToolRecipe: %v", err)
	}

	// Assert Packages length — a zero-value recipe has nil/empty Packages.
	if len(got.Packages) != len(want.Packages) {
		t.Fatalf("Packages len: got %d, want %d", len(got.Packages), len(want.Packages))
	}

	pkg := got.Packages[0]
	wantPkg := want.Packages[0]

	if pkg.Kind != wantPkg.Kind {
		t.Errorf("Packages[0].Kind: got %q, want %q", pkg.Kind, wantPkg.Kind)
	}
	if pkg.Name != wantPkg.Name {
		t.Errorf("Packages[0].Name: got %q, want %q", pkg.Name, wantPkg.Name)
	}
	if pkg.Version != wantPkg.Version {
		t.Errorf("Packages[0].Version: got %q, want %q", pkg.Version, wantPkg.Version)
	}
	if pkg.URLTemplate != wantPkg.URLTemplate {
		t.Errorf("Packages[0].URLTemplate: got %q, want %q", pkg.URLTemplate, wantPkg.URLTemplate)
	}
	if pkg.InstallDir != wantPkg.InstallDir {
		t.Errorf("Packages[0].InstallDir: got %q, want %q", pkg.InstallDir, wantPkg.InstallDir)
	}

	// SHA256ByArch — this is the key field: a dropped recipe produces nil here.
	for arch, wantHash := range wantPkg.SHA256ByArch {
		gotHash, ok := pkg.SHA256ByArch[arch]
		if !ok {
			t.Errorf("Packages[0].SHA256ByArch: missing key %q", arch)
			continue
		}
		if gotHash != wantHash {
			t.Errorf("Packages[0].SHA256ByArch[%q]: got %q, want %q", arch, gotHash, wantHash)
		}
	}
	if len(pkg.SHA256ByArch) != len(wantPkg.SHA256ByArch) {
		t.Errorf("Packages[0].SHA256ByArch len: got %d, want %d", len(pkg.SHA256ByArch), len(wantPkg.SHA256ByArch))
	}

	// Symlinks slice.
	if len(pkg.Symlinks) != len(wantPkg.Symlinks) {
		t.Fatalf("Packages[0].Symlinks len: got %d, want %d", len(pkg.Symlinks), len(wantPkg.Symlinks))
	}
	for i, wantSym := range wantPkg.Symlinks {
		gotSym := pkg.Symlinks[i]
		if gotSym.LinkPath != wantSym.LinkPath {
			t.Errorf("Packages[0].Symlinks[%d].LinkPath: got %q, want %q", i, gotSym.LinkPath, wantSym.LinkPath)
		}
		if gotSym.TargetPath != wantSym.TargetPath {
			t.Errorf("Packages[0].Symlinks[%d].TargetPath: got %q, want %q", i, gotSym.TargetPath, wantSym.TargetPath)
		}
	}

	// BinPath on the outer ToolRecipe.
	if got.BinPath != want.BinPath {
		t.Errorf("BinPath: got %q, want %q", got.BinPath, want.BinPath)
	}
}

// TestParseBuilderToolRecipe_Empty confirms an absent --tool-recipe flag
// (empty string) returns a zero-value recipe and no error.
func TestParseBuilderToolRecipe_Empty(t *testing.T) {
	got, err := parseBuilderToolRecipe("")
	if err != nil {
		t.Fatalf("unexpected error for empty input: %v", err)
	}
	if len(got.Packages) != 0 {
		t.Errorf("expected zero Packages for empty input, got %d", len(got.Packages))
	}
}

// TestParseBuilderToolRecipe_InvalidJSON confirms a malformed flag value
// returns an error rather than a zero-value recipe.
func TestParseBuilderToolRecipe_InvalidJSON(t *testing.T) {
	_, err := parseBuilderToolRecipe("{not valid json")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
