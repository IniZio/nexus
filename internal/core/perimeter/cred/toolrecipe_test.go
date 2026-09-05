package cred

// Tests for ToolRecipe (AC-1 and AC-3).
//
// AC-1: every registered profile declares a ToolRecipe; a missing one fails
// a registry-driven test (not a hand-listed pair of names).
//
// AC-3: a recipe with an empty Version is rejected by Validate.

import (
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

// asRecipeValidationError attempts a type assertion from err to *RecipeValidationError.
// Using a helper avoids importing errors (which is not in the cred package's existing
// import graph) just for errors.As.
func asRecipeValidationError(err error, target **RecipeValidationError) bool {
	if rve, ok := err.(*RecipeValidationError); ok {
		*target = rve
		return true
	}
	return false
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
	if node.Version == "" {
		t.Error("Packages[0] (node) Version is empty")
	}
	if node.SHA256ByArch["x64"] == "" {
		t.Error("Packages[0] (node) SHA256ByArch[x64] is empty; amd64 hash must be pinned")
	}
	// Second package: claude-code npm package.
	npm := r.Packages[1]
	if npm.Kind != RecipeKindNPM {
		t.Errorf("Packages[1].Kind = %q; want %q", npm.Kind, RecipeKindNPM)
	}
	if npm.Version == "" {
		t.Error("Packages[1] (@anthropic-ai/claude-code) Version is empty")
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
	if pkg.Version == "" {
		t.Error("Packages[0] (cursor-agent) Version is empty")
	}
	if pkg.SHA256ByArch["x64"] == "" {
		t.Error("Packages[0] (cursor-agent) SHA256ByArch[x64] is empty; x64 hash must be pinned")
	}
	// arm64 entry must exist (even as empty sentinel) to make the gap explicit.
	if _, ok := pkg.SHA256ByArch["arm64"]; !ok {
		t.Error("Packages[0] (cursor-agent) SHA256ByArch has no arm64 key; add an empty sentinel to make the gap explicit")
	}
	// Must have a symlink from /usr/local/bin/cursor-agent.
	if len(pkg.Symlinks) == 0 {
		t.Error("Packages[0] (cursor-agent) has no Symlinks; expected at least one for /usr/local/bin/cursor-agent")
	}
	found := false
	for _, s := range pkg.Symlinks {
		if s.LinkPath == "/usr/local/bin/cursor-agent" {
			found = true
		}
	}
	if !found {
		t.Error("cursor-agent Symlinks does not contain a link at /usr/local/bin/cursor-agent")
	}
}
