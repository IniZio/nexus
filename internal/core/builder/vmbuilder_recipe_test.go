package builder_test

// Tests that guestBuild (the real production function that assembles the argv
// exec'd into the builder VM as RunBuilderRole) correctly propagates ToolRecipe
// and TargetArch to the exec argv.
//
// This is the test that catches the S7 bug class: recipe declared on the host
// profile but silently dropped before reaching the SolveRequest inside the VM.
//
// Coverage claim: the test drives the REAL guestBuild production code path via
// the GuestBuild export shim. The path it covers is:
//
//	BuildInVM → guestBuild → argv → nexus3-agent --builder-role --tool-recipe=... --target-arch=...
//
// What remains uncovered by make test (//go:build integration files):
//   - The in-VM half: nexus3-agent parsing --tool-recipe → RunBuilderRole →
//     BuildInGuestImage → buildkit_linux.go → SolveRequest.
//     That wiring is tested end-to-end by the S4 live proof
//     (internal/test/selfhost/builder_vm_e2e_test.go, //go:build integration).
//   - The cmd_sandbox.go → BuildInVM → guestBuild chain (requires a KVM host).

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// fakeExecFn is a GuestExecFn substitute that captures argv without executing
// anything. It returns success (exitCode=0, err=nil).
func captureArgvFn(captured *[]string) builder.GuestExecFn {
	return func(_ context.Context, argv []string, _ io.Writer) (int32, error) {
		*captured = append([]string(nil), argv...) // defensive copy
		return 0, nil
	}
}

// minimalRecipe returns a ToolRecipe with one package and a non-empty SHA for
// the given arch. The renderer requires a non-empty SHA to emit the layer (it
// errors on the empty-sentinel); using a known-good value keeps the test
// focused on argv propagation, not renderer validation.
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

// TestGuestBuild_RecipeReachesArgv is the primary regression test for S7.
//
// It calls the REAL guestBuild via the GuestBuild export shim with a non-empty
// recipe and asserts that --tool-recipe=<json> and --target-arch=x64 appear in
// the exec argv. Revert the plumbing in vmbuilder.go (remove the JSON append
// block) and this test goes RED.
func TestGuestBuild_RecipeReachesArgv(t *testing.T) {
	recipe := minimalRecipe()
	targetArch := "x64"

	var capturedArgv []string
	execFn := captureArgvFn(&capturedArgv)

	err := builder.GuestBuild(context.Background(), execFn, nil, recipe, targetArch)
	if err != nil {
		t.Fatalf("GuestBuild failed: %v", err)
	}

	// Assert --builder-role is present (invariant).
	if !sliceContains(capturedArgv, "--builder-role") {
		t.Errorf("argv missing --builder-role: %v", capturedArgv)
	}

	// Assert --tool-recipe= is present and carries parseable JSON.
	recipeArg := argWithPrefix(capturedArgv, "--tool-recipe=")
	if recipeArg == "" {
		t.Fatalf("argv missing --tool-recipe=: %v\n\nThis is the S7 regression: the recipe is not reaching the SolveRequest inside the builder VM.", capturedArgv)
	}
	var decoded cred.ToolRecipe
	recipeJSON := strings.TrimPrefix(recipeArg, "--tool-recipe=")
	if err := json.Unmarshal([]byte(recipeJSON), &decoded); err != nil {
		t.Fatalf("--tool-recipe value is not valid JSON: %v\nraw: %s", err, recipeJSON)
	}

	// Assert --target-arch= is present with the expected value.
	archArg := argWithPrefix(capturedArgv, "--target-arch=")
	if archArg == "" {
		t.Fatalf("argv missing --target-arch=: %v", capturedArgv)
	}
	if got := strings.TrimPrefix(archArg, "--target-arch="); got != targetArch {
		t.Errorf("--target-arch: got %q, want %q", got, targetArch)
	}
}

// TestGuestBuild_ZeroRecipeNoRecipeArgs verifies that when no recipe is set
// (zero value), no --tool-recipe or --target-arch args are added to argv.
// This prevents spurious args on builds that have no agent tool configured.
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

// TestGuestBuild_RecipeRoundTrip asserts that a populated ToolRecipe survives
// JSON marshal → unmarshal intact. This proves the VM-boundary serialisation
// is lossless for the cred.ToolRecipe type (maps, nested slices).
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

	// BinPath round-trips.
	if decoded.BinPath != original.BinPath {
		t.Errorf("BinPath: got %q, want %q", decoded.BinPath, original.BinPath)
	}
	// Package count round-trips.
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

// TestGoArchToVendorArch validates the explicit arch-namespace conversion.
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
