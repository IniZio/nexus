package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
)

// buildFakeHome creates the following layout inside dir:
//
//	CLAUDE.md
//	skills/demo/SKILL.md
//	settings.json          (contains both portable and secret keys)
//	.credentials.json      (MUST NOT appear in dest)
//	.claude.json           (MUST NOT appear in dest)
//	settings.local.json    (MUST NOT appear in dest)
func buildFakeHome(t *testing.T, dir string) {
	t.Helper()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, content string) {
		t.Helper()
		full := filepath.Join(dir, path)
		must(os.MkdirAll(filepath.Dir(full), 0o755))
		must(os.WriteFile(full, []byte(content), 0o644))
	}

	write("CLAUDE.md", "# My CLAUDE.md\n")
	write("skills/demo/SKILL.md", "# demo skill\n")
	// Plant EVERY hard-denied secret filename INSIDE the skills/ tree so the
	// "skills/**" glob would reach each one if its exclusion were removed. This
	// makes the deny for each filename independently mutation-proven: remove any
	// entry from secretFileNames and that file appears in destDir, causing
	// assertNoSecrets to fail. (Top-level copies are excluded by the allowlist,
	// a different mechanism — planting under the glob tests the deny itself.)
	write("skills/.credentials.json", `{"leaked":"true"}`)
	write("skills/.claude.json", `{"leaked":"session"}`)
	write("skills/settings.local.json", `{"leaked":"local"}`)

	settings := map[string]any{
		// Portable — must survive filtering.
		"model": "claude-opus-4-5",
		"theme": "dark",
		// Secret — must be stripped. One entry per key in secretSettingsTopKeys
		// so every strip is mutation-proven, plus the sandbox.credentials subtree.
		"apiKeyHelper":        "secret-script",
		"awsCredentialExport": "aws-secret",
		"gcpAuthRefresh":      "gcp-secret",
		"otelHeadersHelper":   "otel-secret",
		"env":                 map[string]string{"MY_SECRET": "hunter2"},
		"permissions":         map[string]any{"allow": []string{"*"}},
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{{"matcher": ".*", "hooks": []map[string]any{{"type": "command", "command": "evil"}}}},
		},
		// sandbox.* is NOT allowlisted — the whole key (incl. its credentials
		// subtree) must be dropped.
		"sandbox": map[string]any{
			"credentials": map[string]string{"token": "sandbox-secret"},
			"network":     map[string]any{"allowedDomains": []string{"example.com"}},
		},
		// An unrecognised/future key that might carry a secret — the allowlist
		// must DROP it (this is the whole point of allowlist-over-denylist).
		"someFutureSecretKey": "leak-me-if-you-can",
	}
	b, err := json.Marshal(settings)
	must(err)
	write("settings.json", string(b))

	// Planted secret files — any of these appearing in destDir is a test failure.
	write(".credentials.json", `{"oauth_token":"super-secret"}`)
	write(".claude.json", `{"session":"abc"}`)
	write("settings.local.json", `{"apiKeyHelper":"local-secret"}`)
}

// knownSecretFilenames lists every secret filename the assembler must exclude.
// Removing the .credentials.json exclusion from agentconfig.go would cause
// assertNoSecrets to find ".credentials.json" in destDir and fail — that is the
// mutation-proven invariant.
var knownSecretFilenames = []string{
	".credentials.json",
	".claude.json",
	"settings.local.json",
}

// knownSecretSettingsKeys are top-level keys that must never appear in the
// staged settings.json.
var knownSecretSettingsKeys = []string{
	"apiKeyHelper",
	"awsCredentialExport",
	"gcpAuthRefresh",
	"otelHeadersHelper",
	"env",
	"hooks",
	"permissions",
}

// assertNoSecrets walks destDir and fails if any known-secret filename or
// known-secret settings key is found. This is the hard invariant.
func assertNoSecrets(t *testing.T, destDir string) {
	t.Helper()

	err := filepath.WalkDir(destDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()

		// Check secret filenames.
		for _, secret := range knownSecretFilenames {
			if name == secret {
				t.Errorf("secret file present in destDir: %s", path)
			}
		}

		// For settings.json, parse and check for secret keys.
		if name == "settings.json" {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("could not read %s: %v", path, err)
				return nil
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(data, &m); err != nil {
				t.Errorf("staged settings.json is not valid JSON: %v", err)
				return nil
			}
			for _, key := range knownSecretSettingsKeys {
				if _, ok := m[key]; ok {
					t.Errorf("secret key %q found in staged settings.json at %s", key, path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk destDir: %v", err)
	}
}

func TestAssembleCuratedConfig(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	buildFakeHome(t, srcDir)

	profile := cred.ClaudeCodeProfile
	// MountAllowlist: ["CLAUDE.md", "skills/**", "settings.json"]

	if err := service.AssembleCuratedConfig(profile, srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	// ---- Presence assertions ----

	wantPresent := []string{
		"CLAUDE.md",
		filepath.Join("skills", "demo", "SKILL.md"),
		"settings.json",
	}
	for _, rel := range wantPresent {
		full := filepath.Join(destDir, rel)
		if _, err := os.Stat(full); os.IsNotExist(err) {
			t.Errorf("expected file missing in destDir: %s", rel)
		}
	}

	// ---- Absence / secret assertions (mutation-proven) ----
	// If the .credentials.json exclusion in agentconfig.go is removed, the
	// planted .credentials.json in srcDir would NOT match MountAllowlist
	// (no glob covers it), so the absence is also protected by allowlist design.
	// However, the assertNoSecrets check below is the independent mechanical
	// guarantee — it fails whenever ANY known-secret filename appears, regardless
	// of how it got there.
	assertNoSecrets(t, destDir)

	// ---- settings.json content assertions ----

	settingsPath := filepath.Join(destDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read staged settings.json: %v", err)
	}
	var staged map[string]json.RawMessage
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatalf("parse staged settings.json: %v", err)
	}

	// Portable keys must survive.
	if _, ok := staged["model"]; !ok {
		t.Error("staged settings.json missing portable key 'model'")
	}

	// Secret keys must be absent.
	for _, key := range knownSecretSettingsKeys {
		if _, ok := staged[key]; ok {
			t.Errorf("staged settings.json still contains secret key %q", key)
		}
	}
}

// TestAssembleCuratedConfig_AllowlistDropsUnknownAndSecret verifies the
// allowlist posture: only vetted portable keys survive; the whole sandbox key
// (incl. its credentials subtree) and any unrecognised/future key are dropped.
func TestAssembleCuratedConfig_AllowlistDropsUnknownAndSecret(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()
	buildFakeHome(t, srcDir)

	if err := service.AssembleCuratedConfig(cred.ClaudeCodeProfile, srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destDir, "settings.json"))
	if err != nil {
		t.Fatalf("read staged settings.json: %v", err)
	}
	var staged map[string]json.RawMessage
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatalf("parse staged settings.json: %v", err)
	}
	// Portable allowlisted keys survive.
	for _, k := range []string{"model", "theme"} {
		if _, ok := staged[k]; !ok {
			t.Errorf("portable key %q was dropped", k)
		}
	}
	// Non-allowlisted keys — sandbox (secret subtree) and an unknown future key —
	// must be dropped entirely by the allowlist, even though neither is on any
	// hardcoded denylist. Mutation: invert copyFilteredSettings back to a
	// denylist and someFutureSecretKey survives, failing here.
	for _, k := range []string{"sandbox", "someFutureSecretKey"} {
		if _, leaked := staged[k]; leaked {
			t.Errorf("non-allowlisted key %q leaked into staged settings.json", k)
		}
	}
}

// TestAssembleCuratedConfig_SymlinkPolicy verifies the SECURITY-critical symlink
// policy: directory symlinks are FOLLOWED (real skills often symlink out of the
// config dir), but FILE symlinks are never read/copied — closing the exfil
// vector where a symlinked file under the shared tree points at a secret.
func TestAssembleCuratedConfig_SymlinkPolicy(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// A skill dir living OUTSIDE the config dir, reached via a symlink — the
	// common real-world layout (~/.claude/skills/foo -> ~/.agents/skills/foo).
	externalSkill := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalSkill, "SKILL.md"), []byte("# external skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A secret living outside the config dir, arbitrarily named.
	externalSecret := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(externalSecret, []byte("PRIVATE-KEY-DO-NOT-LEAK"), 0o600); err != nil {
		t.Fatal(err)
	}

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(srcDir, "skills"), 0o755))
	must(os.WriteFile(filepath.Join(srcDir, "CLAUDE.md"), []byte("# md\n"), 0o644))
	// Also plant a real in-tree secret to be pointed at by an in-tree file symlink.
	must(os.WriteFile(filepath.Join(srcDir, ".credentials.json"), []byte(`{"oauth":"secret"}`), 0o600))

	// (1) Directory symlink → external skill dir. MUST be followed and its file copied.
	must(os.Symlink(externalSkill, filepath.Join(srcDir, "skills", "external")))
	// (2) File symlink under skills/ → in-tree .credentials.json. MUST NOT be copied.
	must(os.Symlink(filepath.Join(srcDir, ".credentials.json"), filepath.Join(srcDir, "skills", "notes.md")))
	// (3) File symlink under skills/ → out-of-tree arbitrarily-named secret. MUST NOT be copied.
	must(os.Symlink(externalSecret, filepath.Join(srcDir, "skills", "harmless.md")))

	if err := service.AssembleCuratedConfig(cred.ClaudeCodeProfile, srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	// (1) The symlinked skill dir's content must be present (functional requirement:
	//     users with symlinked skills must still get them; the mutation is refusing
	//     to follow dir symlinks, which makes this assertion fail).
	if _, err := os.Stat(filepath.Join(destDir, "skills", "external", "SKILL.md")); os.IsNotExist(err) {
		t.Error("symlinked skill dir was not followed; external/SKILL.md missing from destDir")
	}

	// (2)+(3) No secret content may have been copied via a file symlink, under any name.
	err := filepath.WalkDir(destDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		s := string(data)
		if strings.Contains(s, "PRIVATE-KEY-DO-NOT-LEAK") || strings.Contains(s, `"oauth":"secret"`) {
			t.Errorf("secret content leaked via a file symlink into %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk destDir: %v", err)
	}
}

// TestAssembleCuratedConfig_MissingSourceSkipped verifies that a missing
// agentConfigDir (or missing glob targets) does not return an error.
func TestAssembleCuratedConfig_MissingSourceSkipped(t *testing.T) {
	destDir := t.TempDir()
	profile := cred.ClaudeCodeProfile

	// Use a source directory that doesn't exist — should be silently skipped.
	err := service.AssembleCuratedConfig(profile, "/nonexistent/path/that/does/not/exist", destDir)
	if err != nil {
		t.Fatalf("expected no error for missing source, got: %v", err)
	}
}

// TestAssembleCuratedConfig_GitDirExcluded verifies that a .git directory
// accidentally inside the source tree is never staged.
func TestAssembleCuratedConfig_GitDirExcluded(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Plant a file inside a .git dir that a "**" glob would otherwise reach.
	gitFile := filepath.Join(srcDir, "skills", ".git", "config")
	if err := os.MkdirAll(filepath.Dir(gitFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitFile, []byte("git innards"), 0o644); err != nil {
		t.Fatal(err)
	}

	profile := cred.ClaudeCodeProfile
	if err := service.AssembleCuratedConfig(profile, srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	// The .git/config must not appear in dest.
	err := filepath.WalkDir(destDir, func(path string, d os.DirEntry, _ error) error {
		if !d.IsDir() && d.Name() == "config" {
			t.Errorf("file from .git dir appeared in destDir: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
