package builder_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/builder"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

func captureArgvFn(captured *[]string) builder.GuestExecFn {
	return func(_ context.Context, argv []string, _ io.Writer) (int32, error) {
		*captured = append([]string(nil), argv...) // defensive copy
		return 0, nil
	}
}

func minimalRecipe() cred.ToolRecipe {
	return cred.ToolRecipe{
		BinPath: "/usr/local/bin",
		Packages: []cred.RecipePackage{
			{
				Kind:        cred.RecipeKindTarball,
				Name:        "node",
				Version:     "22.0.0",
				URLTemplate: "https://example.com/node-{VERSION}-linux-{ARCH}.tar.gz",
				SHA256ByArch: map[string]string{
					"x64":   "aaaa1111" + strings.Repeat("0", 56),
					"arm64": "bbbb2222" + strings.Repeat("0", 56),
				},
				Symlinks: []cred.RecipeSymlink{
					{LinkPath: "/usr/local/bin/node", TargetPath: "/usr/local/share/node/bin/node"},
				},
			},
		},
	}
}

func minimalOCIRecipe() cred.ToolRecipe {
	return cred.ToolRecipe{
		BinPath: "/usr/local/bin/claude",
		Packages: []cred.RecipePackage{
			{
				Kind:       cred.RecipeKindOCI,
				Name:       "claude-code",
				Version:    "sha256:" + strings.Repeat("b", 64),
				Image:      "docker/sandbox-templates:claude-code-minimal-nightly",
				SrcPath:    "/home/agent/.local/share/claude/versions/",
				InstallDir: "/usr/local/share/claude/versions",
				BinRel:     "",
				Symlinks:   []cred.RecipeSymlink{{LinkPath: "/usr/local/bin/claude"}},
			},
		},
	}
}

func TestGuestBuild_RecipeReachesArgv(t *testing.T) {
	recipe := minimalRecipe()
	targetArch := "x64"

	var capturedArgv []string
	execFn := captureArgvFn(&capturedArgv)

	err := builder.GuestBuild(context.Background(), execFn, nil, recipe, targetArch)
	if err != nil {
		t.Fatalf("GuestBuild failed: %v", err)
	}

	if !sliceContains(capturedArgv, "--builder-role") {
		t.Errorf("argv missing --builder-role: %v", capturedArgv)
	}

	recipeArg := argWithPrefix(capturedArgv, "--tool-recipe=")
	if recipeArg == "" {
		t.Fatalf("argv missing --tool-recipe=: %v\n\nThis is the S7 regression: the recipe is not reaching the SolveRequest inside the builder VM.", capturedArgv)
	}
	var decoded cred.ToolRecipe
	recipeJSON := strings.TrimPrefix(recipeArg, "--tool-recipe=")
	if err := json.Unmarshal([]byte(recipeJSON), &decoded); err != nil {
		t.Fatalf("--tool-recipe value is not valid JSON: %v\nraw: %s", err, recipeJSON)
	}

	archArg := argWithPrefix(capturedArgv, "--target-arch=")
	if archArg == "" {
		t.Fatalf("argv missing --target-arch=: %v", capturedArgv)
	}
	if got := strings.TrimPrefix(archArg, "--target-arch="); got != targetArch {
		t.Errorf("--target-arch: got %q, want %q", got, targetArch)
	}
}

func TestGuestBuild_ZeroRecipeNoRecipeArgs(t *testing.T) {
	var capturedArgv []string
	execFn := captureArgvFn(&capturedArgv)

	err := builder.GuestBuild(context.Background(), execFn, nil, cred.ToolRecipe{}, "")
	if err != nil {
		t.Fatalf("GuestBuild failed: %v", err)
	}

	if arg := argWithPrefix(capturedArgv, "--tool-recipe="); arg != "" {
		t.Errorf("expected no --tool-recipe= for zero recipe, got: %s", arg)
	}
	if arg := argWithPrefix(capturedArgv, "--target-arch="); arg != "" {
		t.Errorf("expected no --target-arch= for zero recipe, got: %s", arg)
	}
}

func TestGuestBuild_RecipeRoundTrip(t *testing.T) {
	original := minimalRecipe()

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded cred.ToolRecipe
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.BinPath != original.BinPath {
		t.Errorf("BinPath: got %q, want %q", decoded.BinPath, original.BinPath)
	}
	if len(decoded.Packages) != len(original.Packages) {
		t.Fatalf("Packages length: got %d, want %d", len(decoded.Packages), len(original.Packages))
	}
	for i, op := range original.Packages {
		dp := decoded.Packages[i]
		if dp.Name != op.Name {
			t.Errorf("Packages[%d].Name: got %q, want %q", i, dp.Name, op.Name)
		}
		for arch, wantSHA := range op.SHA256ByArch {
			if got := dp.SHA256ByArch[arch]; got != wantSHA {
				t.Errorf("Packages[%d].SHA256ByArch[%q]: got %q, want %q", i, arch, got, wantSHA)
			}
		}
		if dp.URLTemplate != op.URLTemplate {
			t.Errorf("Packages[%d].URLTemplate: got %q, want %q", i, dp.URLTemplate, op.URLTemplate)
		}
		if len(dp.Symlinks) != len(op.Symlinks) {
			t.Errorf("Packages[%d].Symlinks length: got %d, want %d", i, len(dp.Symlinks), len(op.Symlinks))
		}
	}
}

func TestGuestBuild_RecipeReachesArgv_OCI(t *testing.T) {
	recipe := minimalOCIRecipe()
	targetArch := "x64"

	var capturedArgv []string
	execFn := captureArgvFn(&capturedArgv)

	err := builder.GuestBuild(context.Background(), execFn, nil, recipe, targetArch)
	if err != nil {
		t.Fatalf("GuestBuild failed: %v", err)
	}

	recipeArg := argWithPrefix(capturedArgv, "--tool-recipe=")
	if recipeArg == "" {
		t.Fatalf("argv missing --tool-recipe=: %v", capturedArgv)
	}

	var decoded cred.ToolRecipe
	recipeJSON := strings.TrimPrefix(recipeArg, "--tool-recipe=")
	if err := json.Unmarshal([]byte(recipeJSON), &decoded); err != nil {
		t.Fatalf("--tool-recipe value is not valid JSON: %v\nraw: %s", err, recipeJSON)
	}

	if len(decoded.Packages) != 1 {
		t.Fatalf("Packages len: got %d, want 1", len(decoded.Packages))
	}
	pkg := decoded.Packages[0]
	wantPkg := recipe.Packages[0]
	if pkg.Kind != cred.RecipeKindOCI {
		t.Errorf("Kind: got %q, want %q", pkg.Kind, cred.RecipeKindOCI)
	}
	if pkg.Image != wantPkg.Image {
		t.Errorf("Image: got %q, want %q", pkg.Image, wantPkg.Image)
	}
	if pkg.SrcPath != wantPkg.SrcPath {
		t.Errorf("SrcPath: got %q, want %q", pkg.SrcPath, wantPkg.SrcPath)
	}
	if pkg.BinRel != wantPkg.BinRel {
		t.Errorf("BinRel: got %q, want %q", pkg.BinRel, wantPkg.BinRel)
	}
}

func TestGoArchToVendorArch(t *testing.T) {
	cases := []struct {
		goArch string
		want   string
	}{
		{"amd64", "x64"},
		{"arm64", "arm64"},
		{"riscv64", "riscv64"}, // unknown: passed through
	}
	for _, tc := range cases {
		got := builder.GoArchToVendorArch(tc.goArch)
		if got != tc.want {
			t.Errorf("GoArchToVendorArch(%q) = %q, want %q", tc.goArch, got, tc.want)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func sliceContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func argWithPrefix(ss []string, prefix string) string {
	for _, v := range ss {
		if strings.HasPrefix(v, prefix) {
			return v
		}
	}
	return ""
}
