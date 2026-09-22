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
	r := MustProfileByName(ClaudeCodeProfileName).ToolRecipe
	if r.BinPath != "/usr/local/bin/claude" {
		t.Errorf("MustProfileByName(ClaudeCodeProfileName).ToolRecipe.BinPath = %q; want /usr/local/bin/claude", r.BinPath)
	}
	if len(r.Packages) != 1 {
		t.Fatalf("MustProfileByName(ClaudeCodeProfileName).ToolRecipe.Packages has %d entries; want 1 (OCI package)", len(r.Packages))
	}
	pkg := r.Packages[0]
	if pkg.Kind != RecipeKindOCI {
		t.Errorf("Packages[0].Kind = %q; want %q", pkg.Kind, RecipeKindOCI)
	}
	if pkg.Name != "claude-code" {
		t.Errorf("Packages[0].Name = %q; want %q", pkg.Name, "claude-code")
	}
	if !pkg.IsFloating() {
		t.Errorf("Packages[0].Version = %q; want FloatingVersion (%q)", pkg.Version, FloatingVersion)
	}
	const wantImage = "docker/sandbox-templates:claude-code-minimal-nightly"
	if pkg.Image != wantImage {
		t.Errorf("Packages[0].Image = %q; want %q", pkg.Image, wantImage)
	}
	const wantSrcPath = "/home/agent/.local/share/claude/versions/"
	if pkg.SrcPath != wantSrcPath {
		t.Errorf("Packages[0].SrcPath = %q; want %q", pkg.SrcPath, wantSrcPath)
	}
	const wantInstallDir = "/usr/local/share/claude/versions"
	if pkg.InstallDir != wantInstallDir {
		t.Errorf("Packages[0].InstallDir = %q; want %q", pkg.InstallDir, wantInstallDir)
	}
	if pkg.BinRel != "" {
		t.Errorf("Packages[0].BinRel = %q; want empty (version dir entry is the executable)", pkg.BinRel)
	}
	if len(pkg.Symlinks) != 1 || pkg.Symlinks[0].LinkPath != "/usr/local/bin/claude" {
		t.Errorf("Packages[0].Symlinks = %v; want [{LinkPath: /usr/local/bin/claude}]", pkg.Symlinks)
	}
}

func TestCursorAgentProfile_ToolRecipeShape(t *testing.T) {
	r := MustProfileByName(CursorAgentProfileName).ToolRecipe
	if r.BinPath != "/usr/local/bin/cursor-agent" {
		t.Errorf("MustProfileByName(CursorAgentProfileName).ToolRecipe.BinPath = %q; want /usr/local/bin/cursor-agent", r.BinPath)
	}
	if len(r.Packages) != 1 {
		t.Fatalf("MustProfileByName(CursorAgentProfileName).ToolRecipe.Packages has %d entries; want 1 (OCI package)", len(r.Packages))
	}
	pkg := r.Packages[0]
	if pkg.Kind != RecipeKindOCI {
		t.Errorf("Packages[0].Kind = %q; want %q", pkg.Kind, RecipeKindOCI)
	}
	if pkg.Name != "cursor-agent" {
		t.Errorf("Packages[0].Name = %q; want %q", pkg.Name, "cursor-agent")
	}
	if !pkg.IsFloating() {
		t.Errorf("Packages[0].Version = %q; want FloatingVersion (%q)", pkg.Version, FloatingVersion)
	}
	const wantImage = "docker/sandbox-templates:cursor-agent-nightly"
	if pkg.Image != wantImage {
		t.Errorf("Packages[0].Image = %q; want %q", pkg.Image, wantImage)
	}
	const wantSrcPath = "/home/agent/.local/share/cursor-agent/versions/"
	if pkg.SrcPath != wantSrcPath {
		t.Errorf("Packages[0].SrcPath = %q; want %q", pkg.SrcPath, wantSrcPath)
	}
	const wantInstallDir = "/usr/local/share/cursor-agent/versions"
	if pkg.InstallDir != wantInstallDir {
		t.Errorf("Packages[0].InstallDir = %q; want %q", pkg.InstallDir, wantInstallDir)
	}
	if pkg.BinRel != "cursor-agent" {
		t.Errorf("Packages[0].BinRel = %q; want %q", pkg.BinRel, "cursor-agent")
	}
	if len(pkg.Symlinks) != 1 || pkg.Symlinks[0].LinkPath != "/usr/local/bin/cursor-agent" {
		t.Errorf("Packages[0].Symlinks = %v; want [{LinkPath: /usr/local/bin/cursor-agent}]", pkg.Symlinks)
	}
}
