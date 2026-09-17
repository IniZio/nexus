package builder

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

// claudeCodeRecipe mirrors [cred.ClaudeCodeProfile].ToolRecipe as a local copy to avoid dependency on the live profile value.
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
			VersionCmd: "node --version",
		},
		{
			Kind:    cred.RecipeKindNPM,
			Name:    "@anthropic-ai/claude-code",
			Version: "2.1.226",
		},
	},
}

const nodeLayerGolden = `RUN recipe_ver_ge() { a="$1"; b="$2"; while [ -n "$a" ] || [ -n "$b" ]; do ah="${a%%.*}"; bh="${b%%.*}"; if [ "$a" = "$ah" ]; then a=""; else a="${a#*.}"; fi; if [ "$b" = "$bh" ]; then b=""; else b="${b#*.}"; fi; if [ "${ah:-0}" -gt "${bh:-0}" ]; then return 0; fi; if [ "${ah:-0}" -lt "${bh:-0}" ]; then return 1; fi; done; return 0; }; \
    existing="$(node --version 2>/dev/null || true)"; existing="${existing#v}"; existing="${existing%%[!0-9.]*}"; \
    if [ -n "$existing" ] && recipe_ver_ge "$existing" "22.23.2"; then \
    echo "recipe node: existing $existing >= 22.23.2, skipped"; \
    else \
    mkdir -p /usr/local && \
    curl -fsSL "https://nodejs.org/dist/v22.23.2/node-v22.23.2-linux-x64.tar.gz" -o /tmp/node.tar.gz && \
    echo "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a  /tmp/node.tar.gz" | sha256sum -c - && \
    tar -C /usr/local -xzf /tmp/node.tar.gz --strip-components=1 && \
    rm /tmp/node.tar.gz; \
    fi
RUN npm install -g @anthropic-ai/claude-code@2.1.226
`

func TestRenderRecipeLayer_NodeGuardGolden(t *testing.T) {
	got, err := RenderRecipeLayer(claudeCodeRecipe, "x64")
	if err != nil {
		t.Fatalf("RenderRecipeLayer: %v", err)
	}
	if string(got) != nodeLayerGolden {
		t.Errorf("rendered layer differs from golden\ngot:\n%s\nwant:\n%s", got, nodeLayerGolden)
	}
	live, err := RenderRecipeLayer(cred.ClaudeCodeProfile.ToolRecipe, "x64")
	if err != nil {
		t.Fatalf("RenderRecipeLayer(live profile): %v", err)
	}
	if !strings.Contains(string(live), `existing="$(node --version 2>/dev/null || true)"`) {
		t.Errorf("live ClaudeCodeProfile node recipe does not declare VersionCmd; guard absent:\n%s", live)
	}
}

func TestRenderRecipeLayer_VersionCmdRequiresNumericVersion(t *testing.T) {
	recipe := cred.ToolRecipe{Packages: []cred.RecipePackage{{
		Kind:         cred.RecipeKindTarball,
		Name:         "x",
		Version:      "1.0.0-rc1",
		URLTemplate:  "https://example.com/{VERSION}.tar.gz",
		SHA256ByArch: map[string]string{"x64": "abc"},
		InstallDir:   "/opt/x",
		VersionCmd:   "x --version",
	}}}
	if _, err := RenderRecipeLayer(recipe, "x64"); err == nil || !strings.Contains(err.Error(), "dotted-numeric") {
		t.Fatalf("expected dotted-numeric error, got %v", err)
	}
}

// runNodeLayerSnippet executes the rendered node RUN instruction under sh with a fake
// PATH: `node` prints nodeVersion (absent when empty); curl/sha256sum/tar/rm are
// stubs that append their argv to a log so the install branch never touches the host.
func runNodeLayerSnippet(t *testing.T, nodeVersion string) (stdout, log string) {
	t.Helper()
	rendered, err := RenderRecipeLayer(claudeCodeRecipe, "x64")
	if err != nil {
		t.Fatalf("RenderRecipeLayer: %v", err)
	}
	first := strings.SplitN(string(rendered), "\nRUN ", 2)[0]
	script := strings.ReplaceAll(strings.TrimPrefix(first, "RUN "), "\\\n", "")

	bin := t.TempDir()
	logFile := filepath.Join(bin, "calls.log")
	for _, stub := range []string{"curl", "sha256sum", "tar", "rm"} {
		body := "#!/bin/sh\necho \"" + stub + " $*\" >> " + logFile + "\n"
		if err := os.WriteFile(filepath.Join(bin, stub), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if nodeVersion != "" {
		body := "#!/bin/sh\necho \"" + nodeVersion + "\"\n"
		if err := os.WriteFile(filepath.Join(bin, "node"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh -c snippet: %v\noutput:\n%s", err, out)
	}
	logBytes, _ := os.ReadFile(logFile)
	return string(out), string(logBytes)
}

func TestNodeGuard_SkipsWhenExistingNewerOrEqual(t *testing.T) {
	for _, v := range []string{"v22.23.2", "v22.23.10", "v24.1.0"} {
		t.Run(v, func(t *testing.T) {
			out, log := runNodeLayerSnippet(t, v)
			want := "recipe node: existing " + strings.TrimPrefix(v, "v") + " >= 22.23.2, skipped"
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q, got:\n%s", want, out)
			}
			if log != "" {
				t.Errorf("install chain ran despite newer node:\n%s", log)
			}
		})
	}
}

func TestNodeGuard_InstallsWhenOlderOrMissing(t *testing.T) {
	for _, v := range []string{"", "v22.23.1", "v20.19.0", "v22.9"} {
		t.Run("node="+v, func(t *testing.T) {
			out, log := runNodeLayerSnippet(t, v)
			if strings.Contains(out, "skipped") {
				t.Errorf("guard skipped install for node %q:\n%s", v, out)
			}
			for _, step := range []string{"curl -fsSL https://nodejs.org/dist/v22.23.2/node-v22.23.2-linux-x64.tar.gz -o /tmp/node.tar.gz", "sha256sum -c -", "tar -C /usr/local -xzf /tmp/node.tar.gz --strip-components=1", "rm /tmp/node.tar.gz"} {
				if !strings.Contains(log, step) {
					t.Errorf("install chain missing step %q\nlog:\n%s", step, log)
				}
			}
		})
	}
}

func TestIsDottedNumeric(t *testing.T) {
	for in, want := range map[string]bool{"22.23.2": true, "1": true, "": false, "1..2": false, "1.0-rc": false, "v1.0": false} {
		if got := isDottedNumeric(in); got != want {
			t.Errorf("isDottedNumeric(%q) = %v, want %v", in, got, want)
		}
	}
}

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

func TestRenderRecipeLayer_ClaudeCode(t *testing.T) {
	got, err := RenderRecipeLayer(claudeCodeRecipe, "x64")
	if err != nil {
		t.Fatalf("RenderRecipeLayer: %v", err)
	}
	output := string(got)

	if want := "https://nodejs.org/dist/v22.23.2/node-v22.23.2-linux-x64.tar.gz"; !strings.Contains(output, want) {
		t.Errorf("output does not contain Node URL %q\ngot:\n%s", want, output)
	}
	if want := "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a  /tmp/node.tar.gz"; !strings.Contains(output, want) {
		t.Errorf("output does not contain Node checksum line %q\ngot:\n%s", want, output)
	}
	if !strings.Contains(output, "sha256sum -c -") {
		t.Errorf("output does not contain sha256sum -c - invocation\ngot:\n%s", output)
	}
	if !strings.Contains(output, "--strip-components=1") {
		t.Errorf("output does not contain --strip-components=1\ngot:\n%s", output)
	}
	if want := "npm install -g @anthropic-ai/claude-code@2.1.226"; !strings.Contains(output, want) {
		t.Errorf("output does not contain npm install line %q\ngot:\n%s", want, output)
	}
	if strings.Contains(output, "cursor.com/install") {
		t.Errorf("output must not contain a floating installer URL\ngot:\n%s", output)
	}
}

func TestRenderRecipeLayer_CursorAgent(t *testing.T) {
	got, err := RenderRecipeLayer(cursorAgentRecipe, "x64")
	if err != nil {
		t.Fatalf("RenderRecipeLayer: %v", err)
	}
	output := string(got)

	if want := "https://downloads.cursor.com/lab/2026.08.25-3e8eec8/linux/x64/agent-cli-package.tar.gz"; !strings.Contains(output, want) {
		t.Errorf("output does not contain cursor URL %q\ngot:\n%s", want, output)
	}
	if want := "7a212e5a17ff9316f5acc78808e33c536940d5455645022e6388d99ba48c8425  /tmp/cursor-agent.tar.gz"; !strings.Contains(output, want) {
		t.Errorf("output does not contain cursor checksum line %q\ngot:\n%s", want, output)
	}
	if !strings.Contains(output, "sha256sum -c -") {
		t.Errorf("output does not contain sha256sum -c - invocation\ngot:\n%s", output)
	}
	if want := "/usr/local/share/cursor-agent/versions/2026.08.25-3e8eec8"; !strings.Contains(output, want) {
		t.Errorf("output does not contain versioned install dir %q\ngot:\n%s", want, output)
	}
	if want := "ln -sf"; !strings.Contains(output, want) {
		t.Errorf("output does not contain symlink step %q\ngot:\n%s", want, output)
	}
	if strings.Contains(output, "cursor.com/install") {
		t.Errorf("output must not contain a floating installer URL\ngot:\n%s", output)
	}
}

// TestRenderRecipeLayer_Determinism: two renders of the same recipe must be byte-identical (deliberate opposite of the nonce-bearing agent COPY layer).
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

// TestRenderRecipeLayer_UnknownArch: empty SHA256ByArch sentinel means "not yet measured"; renderer must error rather than produce unverified output.
func TestRenderRecipeLayer_UnknownArch(t *testing.T) {
	_, err := RenderRecipeLayer(cursorAgentRecipe, "arm64")
	if err == nil {
		t.Fatal("expected error for empty arm64 hash, got nil")
	}
	if !strings.Contains(err.Error(), "arm64") {
		t.Errorf("error should mention arch, got: %v", err)
	}

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

// TestRenderRecipeLayer_NoAgentBranching: renderer source must not contain agent-name branches (structural guarantee, not runtime behaviour).
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

	// Compound profile-name strings (with dash) and constant names; bare words like "claude" omitted (appear in vendor URLs).
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
