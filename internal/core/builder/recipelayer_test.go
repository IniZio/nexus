package builder

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// claudeCodeRecipe mirrors [cred.ClaudeCodeProfile].ToolRecipe. Using a local
// copy avoids a dependency on the live profile value (which S5 may update
// concurrently) while still exercising the same structural shape.
var claudeCodeRecipe = cred.ToolRecipe{
	BinPath: "/usr/local/bin/claude",
	Packages: []cred.RecipePackage{
		{
			Kind:        cred.RecipeKindTarball,
			Name:        "node",
			Version:     "22.23.2",
			URLTemplate: "https://nodejs.org/dist/v{VERSION}/node-v{VERSION}-linux-{ARCH}.tar.gz",
			SHA256ByArch: map[string]string{
				"x64": "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a",
			},
			InstallDir: "/usr/local",
		},
		{
			Kind:    cred.RecipeKindNPM,
			Name:    "@anthropic-ai/claude-code",
			Version: "2.1.226",
		},
	},
}

// cursorAgentRecipe mirrors [cred.CursorAgentProfile].ToolRecipe.
var cursorAgentRecipe = cred.ToolRecipe{
	BinPath: "/usr/local/bin/cursor-agent",
	Packages: []cred.RecipePackage{
		{
			Kind:        cred.RecipeKindTarball,
			Name:        "cursor-agent",
			Version:     "2026.08.25-3e8eec8",
			URLTemplate: "https://downloads.cursor.com/lab/{VERSION}/linux/{ARCH}/agent-cli-package.tar.gz",
			SHA256ByArch: map[string]string{
				"x64":   "7a212e5a17ff9316f5acc78808e33c536940d5455645022e6388d99ba48c8425",
				"arm64": "",
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

// TestRenderRecipeLayer_ClaudeCode verifies the rendered steps for the
// claude-code recipe: Node tarball download+verify+extract followed by npm
// install.
func TestRenderRecipeLayer_ClaudeCode(t *testing.T) {
	got, err := RenderRecipeLayer(claudeCodeRecipe, "x64")
	if err != nil {
		t.Fatalf("RenderRecipeLayer: %v", err)
	}
	output := string(got)

	// Node tarball: correct URL with {VERSION} and {ARCH} expanded.
	if want := "https://nodejs.org/dist/v22.23.2/node-v22.23.2-linux-x64.tar.gz"; !strings.Contains(output, want) {
		t.Errorf("output does not contain Node URL %q\ngot:\n%s", want, output)
	}
	// Checksum verification in sha256sum -c - style.
	if want := "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a  /tmp/node.tar.gz"; !strings.Contains(output, want) {
		t.Errorf("output does not contain Node checksum line %q\ngot:\n%s", want, output)
	}
	// sha256sum -c - invocation.
	if !strings.Contains(output, "sha256sum -c -") {
		t.Errorf("output does not contain sha256sum -c - invocation\ngot:\n%s", output)
	}
	// --strip-components=1 for the tarball extract.
	if !strings.Contains(output, "--strip-components=1") {
		t.Errorf("output does not contain --strip-components=1\ngot:\n%s", output)
	}
	// npm install at pinned version.
	if want := "npm install -g @anthropic-ai/claude-code@2.1.226"; !strings.Contains(output, want) {
		t.Errorf("output does not contain npm install line %q\ngot:\n%s", want, output)
	}
	// No floating installer.
	if strings.Contains(output, "cursor.com/install") {
		t.Errorf("output must not contain a floating installer URL\ngot:\n%s", output)
	}
}

// TestRenderRecipeLayer_CursorAgent verifies the rendered steps for the
// cursor-agent recipe: a single self-contained tarball with a symlink.
func TestRenderRecipeLayer_CursorAgent(t *testing.T) {
	got, err := RenderRecipeLayer(cursorAgentRecipe, "x64")
	if err != nil {
		t.Fatalf("RenderRecipeLayer: %v", err)
	}
	output := string(got)

	// Correct URL with version and arch expanded.
	if want := "https://downloads.cursor.com/lab/2026.08.25-3e8eec8/linux/x64/agent-cli-package.tar.gz"; !strings.Contains(output, want) {
		t.Errorf("output does not contain cursor URL %q\ngot:\n%s", want, output)
	}
	// Checksum line.
	if want := "7a212e5a17ff9316f5acc78808e33c536940d5455645022e6388d99ba48c8425  /tmp/cursor-agent.tar.gz"; !strings.Contains(output, want) {
		t.Errorf("output does not contain cursor checksum line %q\ngot:\n%s", want, output)
	}
	// sha256sum -c - invocation.
	if !strings.Contains(output, "sha256sum -c -") {
		t.Errorf("output does not contain sha256sum -c - invocation\ngot:\n%s", output)
	}
	// Install dir with version expanded.
	if want := "/usr/local/share/cursor-agent/versions/2026.08.25-3e8eec8"; !strings.Contains(output, want) {
		t.Errorf("output does not contain versioned install dir %q\ngot:\n%s", want, output)
	}
	// Symlink creation: ln -sf <target> <link>.
	if want := "ln -sf"; !strings.Contains(output, want) {
		t.Errorf("output does not contain symlink step %q\ngot:\n%s", want, output)
	}
	// No floating installer.
	if strings.Contains(output, "cursor.com/install") {
		t.Errorf("output must not contain a floating installer URL\ngot:\n%s", output)
	}
}

// TestRenderRecipeLayer_Determinism is the AC-3 proof: two calls with the
// same recipe and arch must produce byte-identical output. This is the
// deliberate opposite of the nexus3-agent COPY layer (newAgentContextFilename)
// which carries a per-solve nonce to bust the buildkit cache.
func TestRenderRecipeLayer_Determinism(t *testing.T) {
	for _, tc := range []struct {
		name   string
		recipe cred.ToolRecipe
		arch   string
	}{
		{"claude-code/x64", claudeCodeRecipe, "x64"},
		{"cursor-agent/x64", cursorAgentRecipe, "x64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := RenderRecipeLayer(tc.recipe, tc.arch)
			if err != nil {
				t.Fatalf("first render: %v", err)
			}
			b, err := RenderRecipeLayer(tc.recipe, tc.arch)
			if err != nil {
				t.Fatalf("second render: %v", err)
			}
			if !bytes.Equal(a, b) {
				t.Errorf("renders not byte-identical:\nfirst:\n%s\nsecond:\n%s", a, b)
			}
		})
	}
}

// TestRenderRecipeLayer_UnknownArch asserts that a tarball package with an
// absent or empty SHA256ByArch entry for the target arch causes an error
// rather than producing unverified output.
//
// Decision: refuse to render (return error). Rationale: silently omitting the
// verification step violates AC-2. The empty sentinel in SHA256ByArch is an
// explicit marker meaning "not yet measured"; building without a known hash
// would install an unverified binary into the guest image. An error at render
// time is a clear, actionable signal to measure and record the hash.
func TestRenderRecipeLayer_UnknownArch(t *testing.T) {
	// cursor-agent arm64: SHA256ByArch["arm64"] is present but empty.
	_, err := RenderRecipeLayer(cursorAgentRecipe, "arm64")
	if err == nil {
		t.Fatal("expected error for empty arm64 hash, got nil")
	}
	if !strings.Contains(err.Error(), "arm64") {
		t.Errorf("error should mention arch, got: %v", err)
	}

	// Absent key (no entry at all for the arch).
	recipeMissingArch := cred.ToolRecipe{
		Packages: []cred.RecipePackage{
			{
				Kind:         cred.RecipeKindTarball,
				Name:         "node",
				Version:      "22.23.2",
				URLTemplate:  "https://nodejs.org/dist/v{VERSION}/node-v{VERSION}-linux-{ARCH}.tar.gz",
				SHA256ByArch: map[string]string{"x64": "abc123"},
				InstallDir:   "/usr/local",
			},
		},
	}
	_, err = RenderRecipeLayer(recipeMissingArch, "arm64")
	if err == nil {
		t.Fatal("expected error for absent arm64 key, got nil")
	}
}

// TestRenderRecipeLayer_UnknownKind asserts that an unrecognised package kind
// returns an error rather than silently producing empty output.
func TestRenderRecipeLayer_UnknownKind(t *testing.T) {
	recipe := cred.ToolRecipe{
		Packages: []cred.RecipePackage{
			{Kind: "apt", Name: "curl", Version: "1.0"},
		},
	}
	_, err := RenderRecipeLayer(recipe, "x64")
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
}

// TestRenderRecipeLayer_NoAgentBranching is the AC-1 meta-test: the renderer
// source must not contain any branch on the profile or agent name. It reads
// recipelayer.go from disk and checks that the profile-name constants and the
// raw name strings are absent.
//
// This test asserts the structural guarantee, not the runtime behaviour. A
// renderer that behaves correctly for known inputs but contains a dormant
// switch on the agent name would still fail this test.
func TestRenderRecipeLayer_NoAgentBranching(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	srcFile := filepath.Join(filepath.Dir(thisFile), "recipelayer.go")
	src, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatalf("read recipelayer.go: %v", err)
	}

	// These strings must not appear in the renderer source. The compound
	// profile-name strings (with a dash) and the Go constant names are the
	// canonical identifiers to check; bare words like "claude" or "cursor"
	// are intentionally omitted from the list because they appear legitimately
	// in vendor URL hostnames and tool descriptions in comments.
	forbidden := []string{
		"claude-code",
		"cursor-agent",
		"ClaudeCodeProfileName",
		"CursorAgentProfileName",
		"ClaudeCodeProfile",
		"CursorAgentProfile",
	}
	for _, s := range forbidden {
		if bytes.Contains(src, []byte(s)) {
			t.Errorf("recipelayer.go contains agent-name string %q — renderer must not branch on the agent name", s)
		}
	}
}

// TestExpandPlaceholders verifies {VERSION} and {ARCH} substitution and
// confirms {OS} is intentionally passed through unexpanded.
func TestExpandPlaceholders(t *testing.T) {
	cases := []struct {
		in, ver, arch, want string
	}{
		{
			in:   "https://nodejs.org/dist/v{VERSION}/node-v{VERSION}-linux-{ARCH}.tar.gz",
			ver:  "22.23.2",
			arch: "x64",
			want: "https://nodejs.org/dist/v22.23.2/node-v22.23.2-linux-x64.tar.gz",
		},
		{
			in:   "/usr/local/share/cursor-agent/versions/{VERSION}",
			ver:  "2026.08.25-3e8eec8",
			arch: "x64",
			want: "/usr/local/share/cursor-agent/versions/2026.08.25-3e8eec8",
		},
		{
			// {OS} is not substituted; it passes through unchanged. This is
			// intentional — all current profiles hardcode "linux" in the URL.
			in:   "https://example.com/{OS}/{ARCH}/{VERSION}.tar.gz",
			ver:  "1.0",
			arch: "x64",
			want: "https://example.com/{OS}/x64/1.0.tar.gz",
		},
	}
	for _, tc := range cases {
		got := expandPlaceholders(tc.in, tc.ver, tc.arch)
		if got != tc.want {
			t.Errorf("expandPlaceholders(%q, %q, %q) = %q, want %q",
				tc.in, tc.ver, tc.arch, got, tc.want)
		}
	}
}

// TestRecipeTmpName verifies safe /tmp filename generation from package names.
func TestRecipeTmpName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"node", "node"},
		{"cursor-agent", "cursor-agent"},
		{"@anthropic-ai/claude-code", "anthropic-ai-claude-code"},
	}
	for _, tc := range cases {
		got := recipeTmpName(tc.in)
		if got != tc.want {
			t.Errorf("recipeTmpName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
