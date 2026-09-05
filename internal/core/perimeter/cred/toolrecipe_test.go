package cred

// Tests for ToolRecipe (AC-1 and AC-3).
//
// AC-1: every registered profile declares a ToolRecipe; a missing one fails
// a registry-driven test (not a hand-listed pair of names).
//
// AC-3: a recipe with an empty Version is rejected by Validate.

import (
	"errors"
	"testing"
)

// TestRegisteredProfiles_HaveToolRecipe iterates ProfileNames() — the same
// set the registry owns — and asserts every profile carries a non-empty
// ToolRecipe. This test will fail automatically when a new profile is added
// to the registry without a recipe, before any consumer code ships (AC-1).
func TestRegisteredProfiles_HaveToolRecipe(t *testing.T) {
	for _, name := range ProfileNames() {
		p, ok := ProfileByName(name)
		if !ok {
			t.Fatalf("ProfileNames() returned %q but ProfileByName(%q) = ok=false", name, name)
		}
		if len(p.ToolRecipe.Packages) == 0 {
			t.Errorf("profile %q has no ToolRecipe packages; every registered profile must declare one (AC-1)", name)
		}
		if p.ToolRecipe.BinPath == "" {
			t.Errorf("profile %q ToolRecipe.BinPath is empty; must be the absolute guest path of the agent binary", name)
		}
		if err := p.ToolRecipe.Validate(); err != nil {
			t.Errorf("profile %q ToolRecipe.Validate() error: %v", name, err)
		}
	}
}

// TestToolRecipeValidate_RejectsEmptyVersion proves that a recipe carrying a
// package with an empty Version is refused before any build can start (AC-3).
// The test supplies a hand-crafted recipe with one valid and one zero-Version
// package; Validate must return a non-nil error citing the zero-Version package.
func TestToolRecipeValidate_RejectsEmptyVersion(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/test-agent",
		Packages: []RecipePackage{
			{
				Kind:    RecipeKindNPM,
				Name:    "@example/cli",
				Version: "", // empty — must be rejected
			},
		},
	}
	err := recipe.Validate()
	if err == nil {
		t.Fatal("ToolRecipe.Validate() returned nil for a recipe with an empty Version; want a non-nil error (AC-3)")
	}
	// The error must name the field that failed.
	var rve *RecipeValidationError
	if ok := asRecipeValidationError(err, &rve); !ok {
		t.Fatalf("Validate() returned %T (%v); want *RecipeValidationError", err, err)
	}
	if rve.Field != "Version" {
		t.Errorf("RecipeValidationError.Field = %q; want \"Version\"", rve.Field)
	}
	if rve.PackageIndex != 0 {
		t.Errorf("RecipeValidationError.PackageIndex = %d; want 0", rve.PackageIndex)
	}
}

// TestToolRecipeValidate_AcceptsPopulatedRecipe ensures Validate passes for a
// well-formed recipe so the validation logic does not over-reject.
func TestToolRecipeValidate_AcceptsPopulatedRecipe(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/example",
		Packages: []RecipePackage{
			{
				Kind:    RecipeKindNPM,
				Name:    "@example/cli",
				Version: "1.2.3",
			},
		},
	}
	if err := recipe.Validate(); err != nil {
		t.Fatalf("Validate() returned error for a valid recipe: %v", err)
	}
}

// TestToolRecipeValidate_RejectsEmptyName ensures a package with an empty Name
// is also rejected.
func TestToolRecipeValidate_RejectsEmptyName(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/x",
		Packages: []RecipePackage{
			{
				Kind:    RecipeKindTarball,
				Name:    "", // empty — must be rejected
				Version: "1.0.0",
			},
		},
	}
	if err := recipe.Validate(); err == nil {
		t.Fatal("Validate() returned nil for a recipe with an empty Name; want non-nil error")
	}
}

// TestToolRecipeValidate_SecondPackageEmptyVersion checks that Validate catches
// an empty Version in a non-first package, and that PackageIndex is correct.
func TestToolRecipeValidate_SecondPackageEmptyVersion(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/x",
		Packages: []RecipePackage{
			{Kind: RecipeKindTarball, Name: "node", Version: "22.0.0"},
			{Kind: RecipeKindNPM, Name: "@example/cli", Version: ""},
		},
	}
	err := recipe.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil for recipe with second package having empty Version")
	}
	var rve *RecipeValidationError
	if ok := asRecipeValidationError(err, &rve); !ok {
		t.Fatalf("error is %T, want *RecipeValidationError", err)
	}
	if rve.PackageIndex != 1 {
		t.Errorf("PackageIndex = %d, want 1", rve.PackageIndex)
	}
}

// asRecipeValidationError unwraps err into target using errors.As.
func asRecipeValidationError(err error, target **RecipeValidationError) bool {
	return errors.As(err, target)
}

// TestClaudeCodeProfile_ToolRecipeSymmetry and TestCursorAgentProfile_ToolRecipeSymmetry
// pin the key structural properties of each profile's recipe as regression guards.
// They do NOT encode branching logic — they are ordinary property checks on the data.

func TestClaudeCodeProfile_ToolRecipeShape(t *testing.T) {
	r := ClaudeCodeProfile.ToolRecipe
	if r.BinPath != "/usr/local/bin/claude" {
		t.Errorf("ClaudeCodeProfile.ToolRecipe.BinPath = %q; want /usr/local/bin/claude", r.BinPath)
	}
	if len(r.Packages) != 2 {
		t.Fatalf("ClaudeCodeProfile.ToolRecipe.Packages has %d entries; want 2 (Node tarball + npm)", len(r.Packages))
	}
	// First package: Node.js tarball (runtime prerequisite).
	node := r.Packages[0]
	if node.Kind != RecipeKindTarball {
		t.Errorf("Packages[0].Kind = %q; want %q", node.Kind, RecipeKindTarball)
	}
	const wantNodeVersion = "22.23.2"
	if node.Version != wantNodeVersion {
		t.Errorf("Packages[0] (node) Version = %q; want %q (exact pin required — non-emptiness does not guard against typos)", node.Version, wantNodeVersion)
	}
	// Source: https://nodejs.org/dist/v22.23.2/SHASUMS256.txt
	const wantNodeX64SHA = "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a"
	if node.SHA256ByArch["x64"] != wantNodeX64SHA {
		t.Errorf("Packages[0] (node) SHA256ByArch[x64] = %q; want %q (exact pin required)", node.SHA256ByArch["x64"], wantNodeX64SHA)
	}
	// Second package: claude-code npm package.
	npm := r.Packages[1]
	if npm.Kind != RecipeKindNPM {
		t.Errorf("Packages[1].Kind = %q; want %q", npm.Kind, RecipeKindNPM)
	}
	const wantClaudeCodeVersion = "2.1.226"
	if npm.Version != wantClaudeCodeVersion {
		t.Errorf("Packages[1] (@anthropic-ai/claude-code) Version = %q; want %q (exact pin required)", npm.Version, wantClaudeCodeVersion)
	}
}

func TestCursorAgentProfile_ToolRecipeShape(t *testing.T) {
	r := CursorAgentProfile.ToolRecipe
	if r.BinPath != "/usr/local/bin/cursor-agent" {
		t.Errorf("CursorAgentProfile.ToolRecipe.BinPath = %q; want /usr/local/bin/cursor-agent", r.BinPath)
	}
	if len(r.Packages) != 1 {
		t.Fatalf("CursorAgentProfile.ToolRecipe.Packages has %d entries; want 1 (self-contained tarball)", len(r.Packages))
	}
	pkg := r.Packages[0]
	if pkg.Kind != RecipeKindTarball {
		t.Errorf("Packages[0].Kind = %q; want %q", pkg.Kind, RecipeKindTarball)
	}
	// Verified 2026-09-05 (R1): linux/x64, 84,518,977 bytes.
	// Vendor publishes no checksum file; version and hash from direct artifact fetch.
	const wantCursorVersion = "2026.08.25-3e8eec8"
	if pkg.Version != wantCursorVersion {
		t.Errorf("Packages[0] (cursor-agent) Version = %q; want %q (exact pin required — non-emptiness does not guard against typos)", pkg.Version, wantCursorVersion)
	}
	const wantCursorX64SHA = "7a212e5a17ff9316f5acc78808e33c536940d5455645022e6388d99ba48c8425"
	if pkg.SHA256ByArch["x64"] != wantCursorX64SHA {
		t.Errorf("Packages[0] (cursor-agent) SHA256ByArch[x64] = %q; want %q (exact pin required)", pkg.SHA256ByArch["x64"], wantCursorX64SHA)
	}
	// arm64 entry must exist (even as empty sentinel) to make the gap explicit.
	if _, ok := pkg.SHA256ByArch["arm64"]; !ok {
		t.Error("Packages[0] (cursor-agent) SHA256ByArch has no arm64 key; add an empty sentinel to make the gap explicit")
	}
	// Must have a symlink from /usr/local/bin/cursor-agent whose TargetPath
	// points to the actual binary name inside the versioned directory.
	// The tarball (agent-cli-package.tar.gz) extracts a top-level dist-package/
	// directory; with --strip-components=1 the binary lands at
	// {InstallDir}/cursor-agent (not "agent-cli" — that would be a dangling
	// symlink). Pin the exact string so a rename in the tarball fails loudly.
	if len(pkg.Symlinks) == 0 {
		t.Error("Packages[0] (cursor-agent) has no Symlinks; expected at least one for /usr/local/bin/cursor-agent")
	}
	const wantLinkPath = "/usr/local/bin/cursor-agent"
	const wantTargetPath = "/usr/local/share/cursor-agent/versions/{VERSION}/cursor-agent"
	found := false
	for _, s := range pkg.Symlinks {
		if s.LinkPath == wantLinkPath {
			found = true
			if s.TargetPath != wantTargetPath {
				t.Errorf("cursor-agent symlink TargetPath = %q; want %q (exact pin required — a wrong name produces a dangling symlink)", s.TargetPath, wantTargetPath)
			}
		}
	}
	if !found {
		t.Errorf("cursor-agent Symlinks does not contain a link at %q", wantLinkPath)
	}
}
