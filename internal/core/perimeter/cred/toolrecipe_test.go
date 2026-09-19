package cred

import (
	"errors"
	"testing"
)

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

func TestToolRecipeValidate_RejectsEmptyVersion(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/test-agent",
		Packages: []RecipePackage{
			{
				Kind:    RecipeKindNPM,
				Name:    "@example/cli",
				Version: "",
			},
		},
	}
	err := recipe.Validate()
	if err == nil {
		t.Fatal("ToolRecipe.Validate() returned nil for a recipe with an empty Version; want a non-nil error (AC-3)")
	}
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

func TestToolRecipeValidate_RejectsEmptyName(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/x",
		Packages: []RecipePackage{
			{
				Kind:    RecipeKindTarball,
				Name:    "",
				Version: "1.0.0",
			},
		},
	}
	if err := recipe.Validate(); err == nil {
		t.Fatal("Validate() returned nil for a recipe with an empty Name; want a non-nil error")
	}
}

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

func asRecipeValidationError(err error, target **RecipeValidationError) bool {
	return errors.As(err, target)
}

func TestToolRecipeValidate_AcceptsFloatingVersionForNPM(t *testing.T) {
	recipe := ToolRecipe{
		BinPath:  "/usr/local/bin/x",
		Packages: []RecipePackage{{Kind: RecipeKindNPM, Name: "@example/cli", Version: FloatingVersion}},
	}
	if err := recipe.Validate(); err != nil {
		t.Fatalf("Validate() error for FloatingVersion on npm package: %v", err)
	}
}

func TestToolRecipeValidate_RejectsFloatingVersionForTarball(t *testing.T) {
	recipe := ToolRecipe{
		BinPath:  "/usr/local/bin/x",
		Packages: []RecipePackage{{Kind: RecipeKindTarball, Name: "node", Version: FloatingVersion}},
	}
	err := recipe.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil for FloatingVersion on tarball package; want non-nil error")
	}
	var rve *RecipeValidationError
	if !errors.As(err, &rve) {
		t.Fatalf("error is %T, want *RecipeValidationError", err)
	}
	if rve.Field != "Version" {
		t.Errorf("RecipeValidationError.Field = %q; want \"Version\"", rve.Field)
	}
}

func TestToolRecipeValidate_AcceptsFloatingOCI(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/claude-code",
		Packages: []RecipePackage{{
			Kind:       RecipeKindOCI,
			Name:       "claude-code-oci",
			Version:    FloatingVersion,
			Image:      "docker/sandbox-templates:claude-code-minimal-nightly",
			SrcPath:    "/home/agent/.local/share/claude/versions/",
			InstallDir: "/usr/local/share/claude",
		}},
	}
	if err := recipe.Validate(); err != nil {
		t.Fatalf("Validate() error for floating OCI package: %v", err)
	}
}

func TestToolRecipeValidate_AcceptsDigestPinnedOCI(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/claude-code",
		Packages: []RecipePackage{{
			Kind:       RecipeKindOCI,
			Name:       "claude-code-oci",
			Version:    "sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abc1",
			Image:      "docker/sandbox-templates:claude-code-minimal-nightly",
			SrcPath:    "/home/agent/.local/share/claude/versions/",
			InstallDir: "/usr/local/share/claude",
		}},
	}
	if err := recipe.Validate(); err != nil {
		t.Fatalf("Validate() error for digest-pinned OCI package: %v", err)
	}
}

func TestToolRecipeValidate_RejectsOCIMissingImage(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/claude-code",
		Packages: []RecipePackage{{
			Kind:       RecipeKindOCI,
			Name:       "claude-code-oci",
			Version:    FloatingVersion,
			SrcPath:    "/home/agent/.local/share/claude/versions/",
			InstallDir: "/usr/local/share/claude",
		}},
	}
	err := recipe.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil for OCI package with empty Image; want non-nil error")
	}
	var rve *RecipeValidationError
	if !errors.As(err, &rve) {
		t.Fatalf("error is %T, want *RecipeValidationError", err)
	}
	if rve.Field != "Image" {
		t.Errorf("RecipeValidationError.Field = %q; want \"Image\"", rve.Field)
	}
}

func TestToolRecipeValidate_RejectsOCIMissingSrcPath(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/claude-code",
		Packages: []RecipePackage{{
			Kind:       RecipeKindOCI,
			Name:       "claude-code-oci",
			Version:    FloatingVersion,
			Image:      "docker/sandbox-templates:claude-code-minimal-nightly",
			InstallDir: "/usr/local/share/claude",
		}},
	}
	err := recipe.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil for OCI package with empty SrcPath; want non-nil error")
	}
	var rve *RecipeValidationError
	if !errors.As(err, &rve) {
		t.Fatalf("error is %T, want *RecipeValidationError", err)
	}
	if rve.Field != "SrcPath" {
		t.Errorf("RecipeValidationError.Field = %q; want \"SrcPath\"", rve.Field)
	}
}

func TestToolRecipeValidate_RejectsOCIMissingInstallDir(t *testing.T) {
	recipe := ToolRecipe{
		BinPath: "/usr/local/bin/claude-code",
		Packages: []RecipePackage{{
			Kind:    RecipeKindOCI,
			Name:    "claude-code-oci",
			Version: FloatingVersion,
			Image:   "docker/sandbox-templates:claude-code-minimal-nightly",
			SrcPath: "/home/agent/.local/share/claude/versions/",
		}},
	}
	err := recipe.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil for OCI package with empty InstallDir; want non-nil error")
	}
	var rve *RecipeValidationError
	if !errors.As(err, &rve) {
		t.Fatalf("error is %T, want *RecipeValidationError", err)
	}
	if rve.Field != "InstallDir" {
		t.Errorf("RecipeValidationError.Field = %q; want \"InstallDir\"", rve.Field)
	}
}

func TestRecipePackage_IsFloatingOCI(t *testing.T) {
	p := RecipePackage{Kind: RecipeKindOCI, Version: FloatingVersion}
	if !p.IsFloating() {
		t.Error("IsFloating() = false for OCI package with FloatingVersion; want true")
	}
}

func TestRecipePackage_IsFloating(t *testing.T) {
	if p := (RecipePackage{Version: FloatingVersion}); !p.IsFloating() {
		t.Error("IsFloating() = false for FloatingVersion; want true")
	}
	if p := (RecipePackage{Version: "1.2.3"}); p.IsFloating() {
		t.Error("IsFloating() = true for concrete version; want false")
	}
	if p := (RecipePackage{Version: ""}); p.IsFloating() {
		t.Error("IsFloating() = true for empty version; want false")
	}
}

func TestClaudeCodeProfile_ToolRecipeShape(t *testing.T) {
	r := ClaudeCodeProfile.ToolRecipe
	if r.BinPath != "/usr/local/bin/claude" {
		t.Errorf("ClaudeCodeProfile.ToolRecipe.BinPath = %q; want /usr/local/bin/claude", r.BinPath)
	}
	if len(r.Packages) != 2 {
		t.Fatalf("ClaudeCodeProfile.ToolRecipe.Packages has %d entries; want 2 (Node tarball + npm)", len(r.Packages))
	}
	node := r.Packages[0]
	if node.Kind != RecipeKindTarball {
		t.Errorf("Packages[0].Kind = %q; want %q", node.Kind, RecipeKindTarball)
	}
	const wantNodeVersion = "22.23.2"
	if node.Version != wantNodeVersion {
		t.Errorf("Packages[0] (node) Version = %q; want %q (exact pin required — non-emptiness does not guard against typos)", node.Version, wantNodeVersion)
	}
	const wantNodeURLTemplate = "https://nodejs.org/dist/v{VERSION}/node-v{VERSION}-linux-{ARCH}.tar.gz"
	if node.URLTemplate != wantNodeURLTemplate {
		t.Errorf("Packages[0] (node) URLTemplate = %q; want %q (exact pin required — a wrong platform or path segment fails only at image-build time)", node.URLTemplate, wantNodeURLTemplate)
	}
	// --strip-components=1 into /usr/local; wrong dir breaks npm install -g.
	const wantNodeInstallDir = "/usr/local"
	if node.InstallDir != wantNodeInstallDir {
		t.Errorf("Packages[0] (node) InstallDir = %q; want %q (exact pin required — wrong dir breaks npm install -g)", node.InstallDir, wantNodeInstallDir)
	}
	const wantNodeX64SHA = "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a"
	if node.SHA256ByArch["x64"] != wantNodeX64SHA {
		t.Errorf("Packages[0] (node) SHA256ByArch[x64] = %q; want %q (exact pin required)", node.SHA256ByArch["x64"], wantNodeX64SHA)
	}
	npm := r.Packages[1]
	if npm.Kind != RecipeKindNPM {
		t.Errorf("Packages[1].Kind = %q; want %q", npm.Kind, RecipeKindNPM)
	}
	const wantClaudeCodeName = "@anthropic-ai/claude-code"
	if npm.Name != wantClaudeCodeName {
		t.Errorf("Packages[1] (claude-code) Name = %q; want %q (exact pin required — non-emptiness does not guard against renames)", npm.Name, wantClaudeCodeName)
	}
	// S4 changes this package to FloatingVersion; assert floating, not a pinned number.
	if !npm.IsFloating() {
		t.Errorf("Packages[1] (@anthropic-ai/claude-code) Version = %q; want FloatingVersion (%q) — the npm package must float so new sandboxes get the current release; a concrete pin here silently freezes every future sandbox", npm.Version, FloatingVersion)
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
