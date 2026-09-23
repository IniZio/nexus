package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
)

// buildFakeHome creates a test home directory tree with secret and portable files.
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
	write("skills/.credentials.json", `{"leaked":"true"}`)
	write("skills/.claude.json", `{"leaked":"session"}`)
	write("skills/settings.local.json", `{"leaked":"local"}`)

	settings := map[string]any{
		"model":               "claude-opus-4-5",
		"theme":               "dark",
		"statusLine":          map[string]string{"type": "command", "command": "bun hud.ts"},
		"apiKeyHelper":        "secret-script",
		"awsCredentialExport": "aws-secret",
		"gcpAuthRefresh":      "gcp-secret",
		"otelHeadersHelper":   "otel-secret",
		"env": map[string]string{
			"MY_SECRET":         "hunter2",
			"GITHUB_TOKEN":      "ghp_real",
			"ANTHROPIC_API_KEY": "sk-ant-real",
			"TMPDIR":            "/dev/shm/host-only",
			"API_TIMEOUT_MS":    "600000",
		},
		"permissions": map[string]any{"allow": []string{"*"}},
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{{"matcher": ".*", "hooks": []map[string]any{{"type": "command", "command": "evil"}}}},
		},
		"sandbox": map[string]any{
			"credentials": map[string]string{"token": "sandbox-secret"},
			"network":     map[string]any{"allowedDomains": []string{"example.com"}},
		},
		"someFutureSecretKey": "leak-me-if-you-can",
	}
	b, err := json.Marshal(settings)
	must(err)
	write("settings.json", string(b))

	write(".credentials.json", `{"oauth_token":"super-secret"}`)
	write(".claude.json", `{"session":"abc"}`)
	write("settings.local.json", `{"apiKeyHelper":"local-secret"}`)
}

var knownSecretFilenames = []string{
	".credentials.json",
	".claude.json",
	"settings.local.json",
}

var knownSecretSettingsKeys = []string{
	"apiKeyHelper",
	"awsCredentialExport",
	"gcpAuthRefresh",
	"otelHeadersHelper",
	"hooks",
	"permissions",
}

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

		for _, secret := range knownSecretFilenames {
			if name == secret {
				t.Errorf("secret file present in destDir: %s", path)
			}
		}

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

	profile := cred.MustProfileByName(cred.ClaudeCodeProfileName)

	if err := service.AssembleCuratedConfig(profile, srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

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

	assertNoSecrets(t, destDir)

	settingsPath := filepath.Join(destDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read staged settings.json: %v", err)
	}
	var staged map[string]json.RawMessage
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatalf("parse staged settings.json: %v", err)
	}

	if _, ok := staged["model"]; !ok {
		t.Error("staged settings.json missing portable key 'model'")
	}

	for _, key := range knownSecretSettingsKeys {
		if _, ok := staged[key]; ok {
			t.Errorf("staged settings.json still contains secret key %q", key)
		}
	}
}

// TestAssembleCuratedConfig_DenylistKeepsUnknownDropsSecret verifies the
// claude-code denylist posture: unlisted keys pass, denied keys and
// credential/host-path env entries do not.
func TestAssembleCuratedConfig_DenylistKeepsUnknownDropsSecret(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()
	buildFakeHome(t, srcDir)

	if err := service.AssembleCuratedConfig(cred.MustProfileByName(cred.ClaudeCodeProfileName), srcDir, destDir); err != nil {
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
	for _, k := range []string{"model", "theme", "statusLine", "someFutureSecretKey"} {
		if _, ok := staged[k]; !ok {
			t.Errorf("non-denied key %q was dropped", k)
		}
	}
	for _, k := range []string{"sandbox", "hooks", "permissions", "apiKeyHelper"} {
		if _, leaked := staged[k]; leaked {
			t.Errorf("denied key %q leaked into staged settings.json", k)
		}
	}
	var env map[string]string
	if err := json.Unmarshal(staged["env"], &env); err != nil {
		t.Fatalf("parse staged env: %v", err)
	}
	if env["API_TIMEOUT_MS"] != "600000" {
		t.Errorf("benign env API_TIMEOUT_MS dropped: %v", env)
	}
	for _, k := range []string{"MY_SECRET", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "TMPDIR"} {
		if _, leaked := env[k]; leaked {
			t.Errorf("env %q leaked into staged settings.json", k)
		}
	}
}

// TestAssembleCuratedConfig_SymlinkPolicy verifies symlink handling: dir-symlinks followed, file-symlinks blocked.
func TestAssembleCuratedConfig_SymlinkPolicy(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	externalSkill := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalSkill, "SKILL.md"), []byte("# external skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	must(os.WriteFile(filepath.Join(srcDir, ".credentials.json"), []byte(`{"oauth":"secret"}`), 0o600))

	must(os.Symlink(externalSkill, filepath.Join(srcDir, "skills", "external")))
	must(os.Symlink(filepath.Join(srcDir, ".credentials.json"), filepath.Join(srcDir, "skills", "notes.md")))
	must(os.Symlink(externalSecret, filepath.Join(srcDir, "skills", "harmless.md")))

	if err := service.AssembleCuratedConfig(cred.MustProfileByName(cred.ClaudeCodeProfileName), srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, "skills", "external", "SKILL.md")); os.IsNotExist(err) {
		t.Error("symlinked skill dir was not followed; external/SKILL.md missing from destDir")
	}

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

// TestAssembleCuratedConfig_MissingSourceSkipped verifies missing source handling.
func TestAssembleCuratedConfig_MissingSourceSkipped(t *testing.T) {
	destDir := t.TempDir()
	profile := cred.MustProfileByName(cred.ClaudeCodeProfileName)

	err := service.AssembleCuratedConfig(profile, "/nonexistent/path/that/does/not/exist", destDir)
	if err != nil {
		t.Fatalf("expected no error for missing source, got: %v", err)
	}
}

// TestAssembleCuratedConfig_BypassConsentPreservesLowerLayerKeys regression test for overlayfs shadow defect.
func TestAssembleCuratedConfig_BypassConsentPreservesLowerLayerKeys(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	hostSettings := map[string]any{
		"enabledPlugins": []string{
			"groundwork@groundwork",
			"handbook@example-handbook",
		},
		"extraKnownMarketplaces": []map[string]string{
			{"name": "groundwork", "url": "https://marketplace.groundwork.invalid"},
		},
		"model": "claude-opus-4-5",
	}
	b, err := json.Marshal(hostSettings)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(srcDir, "settings.json")
	if err := os.WriteFile(settingsPath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := service.AssembleCuratedConfig(cred.MustProfileByName(cred.ClaudeCodeProfileName), srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "settings.json"))
	if err != nil {
		t.Fatalf("staged settings.json not written: %v", err)
	}
	var staged map[string]json.RawMessage
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatalf("staged settings.json is not valid JSON: %v\nraw: %s", err, data)
	}

	for _, key := range []string{"enabledPlugins", "extraKnownMarketplaces"} {
		if _, ok := staged[key]; !ok {
			t.Errorf("staged lower settings.json missing key %q (portable key must survive AssembleCuratedConfig)", key)
		}
	}
	if _, ok := staged["skipDangerousModePermissionPrompt"]; ok {
		t.Errorf("skipDangerousModePermissionPrompt must NOT be injected for claude-code profile (BypassConsentKey is empty)")
	}
}

// TestAssembleCuratedConfig_BypassConsentPresentWhenNoHostSettings verifies bypass handling with no source settings.
func TestAssembleCuratedConfig_BypassConsentPresentWhenNoHostSettings(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	if err := service.AssembleCuratedConfig(cred.MustProfileByName(cred.ClaudeCodeProfileName), srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	settingsPath := filepath.Join(destDir, "settings.json")
	data, readErr := os.ReadFile(settingsPath)
	if os.IsNotExist(readErr) {
		return
	}
	if readErr != nil {
		t.Fatalf("read settings.json: %v", readErr)
	}
	var staged map[string]json.RawMessage
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatalf("staged settings.json is not valid JSON: %v", err)
	}
	if _, ok := staged["skipDangerousModePermissionPrompt"]; ok {
		t.Error("skipDangerousModePermissionPrompt must not be injected for a live-mount profile (BypassConsentKey is empty)")
	}
}

// TestAssembleCuratedConfig_CursorAuthInfoStripped verifies cursor authInfo stripping.
func TestAssembleCuratedConfig_CursorAuthInfoStripped(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	cliConfig := map[string]any{
		"model":        "auto",
		"approvalMode": "allowlist",
		"display":      map[string]any{"mode": "zen"},
		"authInfo": map[string]any{
			"email":       "operator@example.com",
			"displayName": "Operator User",
			"userId":      "usr_abc1234567890",
			"authId":      "aid_xyz0987654321",
		},
		"privacyCache":      map[string]any{"fingerprint": "host-specific-value"},
		"suggestNextPrompt": true,
	}
	b, err := json.Marshal(cliConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "cli-config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := service.AssembleCuratedConfig(cred.MustProfileByName(cred.CursorAgentProfileName), srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	stagedPath := filepath.Join(destDir, "cli-config.json")
	data, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatalf("staged cli-config.json not written: %v", err)
	}
	var staged map[string]json.RawMessage
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatalf("staged cli-config.json is not valid JSON: %v\nraw: %s", err, data)
	}

	droppedKeys := []string{"authInfo", "privacyCache", "suggestNextPrompt"}
	droppedCount := 0
	for _, key := range droppedKeys {
		if _, leaked := staged[key]; leaked {
			t.Errorf("non-allowlisted key %q leaked into staged cli-config.json", key)
		} else {
			droppedCount++
		}
	}
	if droppedCount != len(droppedKeys) {
		t.Fatalf("expected all %d non-allowlisted keys dropped, only %d were", len(droppedKeys), droppedCount)
	}

	for _, key := range []string{"model", "approvalMode", "display"} {
		if _, ok := staged[key]; !ok {
			t.Errorf("portable key %q was dropped from staged cli-config.json", key)
		}
	}

	if strings.Contains(string(data), "usr_abc1234567890") ||
		strings.Contains(string(data), "aid_xyz0987654321") {
		t.Error("raw PII values from authInfo leaked into staged cli-config.json")
	}

	if _, ok := staged["skipDangerousModePermissionPrompt"]; ok {
		t.Error("cursor profile has no BypassConsentKey; staged cli-config.json must not gain skipDangerousModePermissionPrompt")
	}
}

// TestAssembleCuratedConfig_GitDirExcluded verifies .git directory exclusion.
func TestAssembleCuratedConfig_GitDirExcluded(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	gitFile := filepath.Join(srcDir, "skills", ".git", "config")
	if err := os.MkdirAll(filepath.Dir(gitFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitFile, []byte("git innards"), 0o644); err != nil {
		t.Fatal(err)
	}

	profile := cred.MustProfileByName(cred.ClaudeCodeProfileName)
	if err := service.AssembleCuratedConfig(profile, srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

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

// TestAgentSettingsDir verifies ConfigDirEnvVar precedence over CredDirEnvVar.
func TestAgentSettingsDir(t *testing.T) {
	t.Run("cursor_ConfigDirEnvVar_wins", func(t *testing.T) {
		customSettingsDir := t.TempDir()
		t.Setenv("CURSOR_CONFIG_DIR", customSettingsDir)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		got, err := service.AgentSettingsDir(cred.MustProfileByName(cred.CursorAgentProfileName))
		if err != nil {
			t.Fatalf("AgentSettingsDir: %v", err)
		}
		if got != customSettingsDir {
			t.Errorf("AgentSettingsDir = %q, want %q (ConfigDirEnvVar value)", got, customSettingsDir)
		}
	})

	t.Run("cursor_falls_back_to_SettingsPath_dir", func(t *testing.T) {
		t.Setenv("CURSOR_CONFIG_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		got, err := service.AgentSettingsDir(cred.MustProfileByName(cred.CursorAgentProfileName))
		if err != nil {
			t.Fatalf("AgentSettingsDir: %v", err)
		}
		home, _ := os.UserHomeDir()
		want := filepath.Join(home, ".cursor")
		if got != want {
			t.Errorf("AgentSettingsDir = %q, want %q (SettingsPath parent)", got, want)
		}
	})

	t.Run("no_settings_path_returns_empty", func(t *testing.T) {
		got, err := service.AgentSettingsDir(cred.AgentProfile{})
		if err != nil {
			t.Fatalf("AgentSettingsDir: %v", err)
		}
		if got != "" {
			t.Errorf("AgentSettingsDir for zero profile = %q, want empty", got)
		}
	})
}
