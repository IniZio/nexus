package builder

import (
	"bytes"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// minimalNPMRecipe returns a minimal npm recipe for use in pipeline tests.
// npm packages carry no per-arch SHA256 so they always render successfully.
func minimalNPMRecipe() cred.ToolRecipe {
	return cred.ToolRecipe{
		BinPath: "/usr/local/bin/test-agent",
		Packages: []cred.RecipePackage{
			{Kind: cred.RecipeKindNPM, Name: "@test/agent", Version: "1.2.3"},
		},
	}
}

// minimalTarballRecipe returns a minimal tarball recipe with a known arch hash.
func minimalTarballRecipe(arch string) cred.ToolRecipe {
	return cred.ToolRecipe{
		BinPath: "/usr/local/bin/test-agent",
		Packages: []cred.RecipePackage{
			{
				Kind:        cred.RecipeKindTarball,
				Name:        "test-agent",
				Version:     "1.0.0",
				URLTemplate: "https://example.com/{VERSION}/{ARCH}/test-agent.tar.gz",
				InstallDir:  "/usr/local/share/test-agent/{VERSION}",
				SHA256ByArch: map[string]string{
					arch: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
			},
		},
	}
}

// TestSynthesizeDockerfile_RecipeLayerOrdering proves that the recipe layer
// appears AFTER the user's Containerfile and BEFORE the nexus3-agent COPY.
//
// Ordering rationale: the recipe installs the agent tool (claude-code,
// cursor-agent) via deterministic RUN instructions that should cache-hit on a
// warm buildkitd; the nexus3-agent COPY is always last and always a cache MISS
// (nonce filename). Appending the recipe between user instructions and the
// agent COPY groups the cacheable work together while preserving the
// anti-corruption nonce for the boot binary.
func TestSynthesizeDockerfile_RecipeLayerOrdering(t *testing.T) {
	containerfile := []byte("FROM ubuntu:24.04\nRUN echo user-step\n")
	recipeBytes := []byte("RUN npm install -g @test/agent@1.2.3\n")
	const agentFile = "_nexus3-agent-deadbeef"
	const installPath = "/sbin/nexus3-agent"

	df := string(synthesizeDockerfile(containerfile, recipeBytes, agentFile, installPath))

	// User Containerfile must lead.
	if !strings.HasPrefix(df, string(containerfile)) {
		t.Fatalf("synthesized Dockerfile does not start with user Containerfile:\n%s", df)
	}

	// Locate each section.
	userEnd := strings.Index(df, "RUN echo user-step\n") + len("RUN echo user-step\n")
	recipePos := strings.Index(df, "RUN npm install -g")
	copyPos := strings.Index(df, "COPY --chmod=0755 --from=nexus3agent")

	if recipePos < 0 {
		t.Fatal("recipe layer not found in synthesized Dockerfile")
	}
	if copyPos < 0 {
		t.Fatal("agent COPY layer not found in synthesized Dockerfile")
	}
	if recipePos < userEnd {
		t.Fatalf("recipe layer appears before the end of user Containerfile (pos %d < %d)", recipePos, userEnd)
	}
	if copyPos < recipePos {
		t.Fatalf("agent COPY (pos %d) appears before recipe layer (pos %d)", copyPos, recipePos)
	}
}

// TestSynthesizeDockerfile_NoRecipeLayerWhenNil proves that passing nil recipe
// bytes omits the recipe section entirely while still emitting the agent COPY.
func TestSynthesizeDockerfile_NoRecipeLayerWhenNil(t *testing.T) {
	containerfile := []byte("FROM ubuntu:24.04\n")
	df := string(synthesizeDockerfile(containerfile, nil, "_nexus3-agent-x", "/sbin/nexus3-agent"))

	if strings.Contains(df, "Recipe layer") {
		t.Fatalf("nil recipe bytes produced a recipe section:\n%s", df)
	}
	if !strings.Contains(df, "COPY --chmod=0755 --from=nexus3agent") {
		t.Fatalf("agent COPY layer missing:\n%s", df)
	}
}

// TestContainerfileOptsOut_RecipeSkipDirective covers both the suppressed and
// unsuppressed paths for the escape-hatch check (AC-7).
//
//   - A Containerfile containing the nexus3:recipe-skip directive must cause
//     renderRecipeIfNeeded to return nil bytes (suppressed).
//   - A Containerfile without the directive must pass through to RenderRecipeLayer
//     and return the rendered bytes.
//
// The opt-out directive is chosen over path-scanning because a false positive
// from path-scanning (e.g. a comment mentioning the bin path) would silently
// suppress the recipe and leave the image without the agent tool. The directive
// requires deliberate operator intent; a missed directive produces an idempotent
// double-install that still results in a working image.
func TestContainerfileOptsOut_RecipeSkipDirective(t *testing.T) {
	recipe := minimalNPMRecipe()

	t.Run("suppressed when directive present", func(t *testing.T) {
		cf := []byte("FROM ubuntu:24.04\n# nexus3:recipe-skip\nRUN my-custom-install\n")
		got, err := renderRecipeIfNeeded(cf, recipe, "x64")
		if err != nil {
			t.Fatalf("renderRecipeIfNeeded: unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil recipe bytes (suppressed), got:\n%s", got)
		}
	})

	t.Run("emitted when directive absent", func(t *testing.T) {
		cf := []byte("FROM ubuntu:24.04\nRUN echo no-skip\n")
		got, err := renderRecipeIfNeeded(cf, recipe, "x64")
		if err != nil {
			t.Fatalf("renderRecipeIfNeeded: unexpected error: %v", err)
		}
		if len(got) == 0 {
			t.Fatal("expected non-empty recipe bytes, got empty")
		}
		if !strings.Contains(string(got), "@test/agent@1.2.3") {
			t.Fatalf("recipe bytes do not contain expected package install:\n%s", got)
		}
	})

	t.Run("suppressed when no packages", func(t *testing.T) {
		emptyRecipe := cred.ToolRecipe{}
		cf := []byte("FROM ubuntu:24.04\n")
		got, err := renderRecipeIfNeeded(cf, emptyRecipe, "x64")
		if err != nil {
			t.Fatalf("renderRecipeIfNeeded: unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil for empty recipe, got:\n%s", got)
		}
	})
}

// TestSynthesizeDockerfile_RecipeDeterminism proves that two calls to
// synthesizeDockerfile with the same recipe bytes (and the same agentFile)
// produce byte-identical output — tested at the synthesizeDockerfile level,
// not only at the renderer level (which is already covered by recipelayer_test.go).
//
// This is the companion to TestAgentLayerCacheKeyIsUniquePerSolve: the recipe
// layer must be deterministic (cache-hits welcome) while the agent COPY must be
// unique (deliberate cache MISS). Using the same agentFile here isolates the
// synthesizeDockerfile function itself.
func TestSynthesizeDockerfile_RecipeDeterminism(t *testing.T) {
	containerfile := []byte("FROM ubuntu:24.04\nRUN echo setup\n")
	recipe := minimalNPMRecipe()
	const arch = "x64"
	const agentFile = "_nexus3-agent-fixed-for-test" // fixed: not testing nonce here
	const installPath = "/sbin/nexus3-agent"

	recipeBytes1, err := renderRecipeIfNeeded(containerfile, recipe, arch)
	if err != nil {
		t.Fatalf("renderRecipeIfNeeded (call 1): %v", err)
	}
	recipeBytes2, err := renderRecipeIfNeeded(containerfile, recipe, arch)
	if err != nil {
		t.Fatalf("renderRecipeIfNeeded (call 2): %v", err)
	}

	df1 := synthesizeDockerfile(containerfile, recipeBytes1, agentFile, installPath)
	df2 := synthesizeDockerfile(containerfile, recipeBytes2, agentFile, installPath)

	if !bytes.Equal(df1, df2) {
		t.Fatalf("two synthesizeDockerfile calls with the same recipe produced different output\n--- call 1 ---\n%s\n--- call 2 ---\n%s",
			df1, df2)
	}
}

// TestRenderRecipeIfNeeded_Arm64EmptySHA256_FailsClear proves that when the
// target architecture has an empty SHA-256 sentinel in the recipe, the build
// pipeline returns a clear, human-readable error rather than an opaque failure.
//
// This covers the arm64 live finding: cursor-agent's arm64 hash is an explicit
// empty sentinel ("arm64": "") because the binary exists but has never been
// pulled and measured. The pipeline must refuse to build for arm64 until the
// hash is filled in, and the error must name the package and arch.
func TestRenderRecipeIfNeeded_Arm64EmptySHA256_FailsClear(t *testing.T) {
	recipe := cred.ToolRecipe{
		BinPath: "/usr/local/bin/cursor-agent",
		Packages: []cred.RecipePackage{
			{
				Kind:        cred.RecipeKindTarball,
				Name:        "cursor-agent",
				Version:     "2026.08.25-3e8eec8",
				URLTemplate: "https://downloads.cursor.com/lab/{VERSION}/linux/{ARCH}/agent-cli-package.tar.gz",
				InstallDir:  "/usr/local/share/cursor-agent/versions/{VERSION}",
				SHA256ByArch: map[string]string{
					"x64":   "7a212e5a17ff9316f5acc78808e33c536940d5455645022e6388d99ba48c8425",
					"arm64": "", // explicit sentinel: not yet measured
				},
			},
		},
	}

	cf := []byte("FROM ubuntu:24.04\n")
	_, err := renderRecipeIfNeeded(cf, recipe, "arm64")
	if err == nil {
		t.Fatal("expected an error for arm64 with empty SHA-256, got nil")
	}
	// Error must name the arch so the operator understands what to do.
	if !strings.Contains(err.Error(), "arm64") {
		t.Fatalf("error does not mention the arch %q: %v", "arm64", err)
	}
	// Error must name the package.
	if !strings.Contains(err.Error(), "cursor-agent") {
		t.Fatalf("error does not mention the package name: %v", err)
	}
}
