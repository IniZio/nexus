package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
				if v, ok := m[key]; ok && !isNexusReadAnywherePerms(key, v) {
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
		if v, ok := staged[key]; ok && !isNexusReadAnywherePerms(key, v) {
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
		if v, leaked := staged[k]; leaked && !isNexusReadAnywherePerms(k, v) {
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
	if string(staged["skipDangerousModePermissionPrompt"]) != "true" {
		t.Errorf("claude-code guest settings must carry skipDangerousModePermissionPrompt:true; got %s", data)
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
	if readErr != nil {
		t.Fatalf("read settings.json: %v", readErr)
	}
	var staged map[string]json.RawMessage
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatalf("staged settings.json is not valid JSON: %v", err)
	}
	if string(staged["skipDangerousModePermissionPrompt"]) != "true" {
		t.Errorf("claude-code guest settings must carry skipDangerousModePermissionPrompt:true; got %s", data)
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

// TestAssembleCuratedConfig_StagingExcludeGlobs verifies that paths matching
// StagingExcludeGlobs are omitted from the staging dir while sibling paths
// outside the excluded subtree are still staged.
func TestAssembleCuratedConfig_StagingExcludeGlobs(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(srcDir, path)
		must(os.MkdirAll(filepath.Dir(full), 0o755))
		must(os.WriteFile(full, []byte(content), 0o644))
	}

	write("plugins/installed_plugins.json", `{"plugins":{}}`)
	write("plugins/known_marketplaces.json", `{}`)
	write("plugins/marketplaces/foo/manifest.json", `{"name":"foo"}`)
	write("plugins/cache/groundwork/2.8.1/node_modules/heavy/index.js", `huge`)
	write("plugins/cache/context-mode/index.js", `big`)

	profile := cred.MustProfileByName(cred.ClaudeCodeProfileName)

	if err := service.AssembleCuratedConfig(profile, srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	wantPresent := []string{
		filepath.Join("plugins", "installed_plugins.json"),
		filepath.Join("plugins", "known_marketplaces.json"),
		filepath.Join("plugins", "marketplaces", "foo", "manifest.json"),
	}
	for _, rel := range wantPresent {
		if _, err := os.Stat(filepath.Join(destDir, rel)); os.IsNotExist(err) {
			t.Errorf("expected file missing from staging: %s", rel)
		}
	}

	wantAbsent := []string{
		filepath.Join("plugins", "cache", "groundwork", "2.8.1", "node_modules", "heavy", "index.js"),
		filepath.Join("plugins", "cache", "context-mode", "index.js"),
		filepath.Join("plugins", "cache"),
	}
	for _, rel := range wantAbsent {
		if _, err := os.Stat(filepath.Join(destDir, rel)); err == nil {
			t.Errorf("excluded path present in staging (should be absent): %s", rel)
		}
	}
}

// TestAssembleCuratedConfig_HardlinkFallback verifies that copyRaw hardlinks
// when source and dest are on the same filesystem, and that the staged file
// shares an inode with the original.
func TestAssembleCuratedConfig_HardlinkFallback(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	srcFile := filepath.Join(srcDir, "CLAUDE.md")
	must(os.WriteFile(srcFile, []byte("# test\n"), 0o644))

	profile := cred.AgentProfile{
		MountAllowlist: []string{"CLAUDE.md"},
	}
	if err := service.AssembleCuratedConfig(profile, srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	dstFile := filepath.Join(destDir, "CLAUDE.md")
	srcInfo, err := os.Stat(srcFile)
	if err != nil {
		t.Fatalf("stat src: %v", err)
	}
	dstInfo, err := os.Stat(dstFile)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}

	// On the same tmpfs/ext4 the files should share an inode (hardlink).
	// If the test host uses a cross-device tmpdir the Link call falls back to
	// copy; in that case the file still exists and content is correct.
	srcSys, ok1 := srcInfo.Sys().(*syscall.Stat_t)
	dstSys, ok2 := dstInfo.Sys().(*syscall.Stat_t)
	if ok1 && ok2 {
		if srcSys.Dev == dstSys.Dev {
			if srcSys.Ino != dstSys.Ino {
				t.Errorf("same-device files should share inode (hardlink); src ino=%d dst ino=%d", srcSys.Ino, dstSys.Ino)
			}
		}
	}

	data, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if string(data) != "# test\n" {
		t.Errorf("staged content mismatch: %q", data)
	}
}

// TestCopyRaw_PreexistingHardlink verifies that when dst is a hardlink to a
// DIFFERENT file (hostOrig), CopyRaw writes src content to dst without
// truncating hostOrig's inode.
func TestCopyRaw_PreexistingHardlink(t *testing.T) {
	dir := t.TempDir()
	hostOrig := filepath.Join(dir, "host-orig.txt")
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(hostOrig, []byte("HOST-ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("NEW-CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hostOrig, dst); err != nil {
		t.Skipf("os.Link not supported: %v", err)
	}

	if err := service.CopyRaw(src, dst); err != nil {
		t.Fatalf("CopyRaw: %v", err)
	}

	orig, err := os.ReadFile(hostOrig)
	if err != nil {
		t.Fatalf("read hostOrig: %v", err)
	}
	if string(orig) != "HOST-ORIGINAL" {
		t.Errorf("hostOrig inode truncated: got %q, want HOST-ORIGINAL", orig)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "NEW-CONTENT" {
		t.Errorf("dst = %q, want NEW-CONTENT", got)
	}
}

// TestCopyRaw_PreexistingHardlink_EXDEVFallback covers the temp+rename path
// when osLinkFn returns EXDEV on every call.
func TestCopyRaw_PreexistingHardlink_EXDEVFallback(t *testing.T) {
	dir := t.TempDir()
	hostOrig := filepath.Join(dir, "host-orig.txt")
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(hostOrig, []byte("HOST-ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("NEW-CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hostOrig, dst); err != nil {
		t.Skipf("os.Link not supported: %v", err)
	}

	restore := service.SetOsLinkFn(func(_, _ string) error {
		return &os.LinkError{Op: "link", Err: syscall.EXDEV}
	})
	defer restore()

	if err := service.CopyRaw(src, dst); err != nil {
		t.Fatalf("CopyRaw (EXDEV fallback): %v", err)
	}

	orig, err := os.ReadFile(hostOrig)
	if err != nil {
		t.Fatalf("read hostOrig: %v", err)
	}
	if string(orig) != "HOST-ORIGINAL" {
		t.Errorf("hostOrig inode truncated: got %q, want HOST-ORIGINAL", orig)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "NEW-CONTENT" {
		t.Errorf("dst = %q, want NEW-CONTENT", got)
	}
}

func TestAssembleCuratedConfig_ClaudeReadAnywhere(t *testing.T) {
	srcDir, destDir := t.TempDir(), t.TempDir()
	host := `{"model":"m"}`
	if err := os.WriteFile(filepath.Join(srcDir, "settings.json"), []byte(host), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.AssembleCuratedConfig(cred.MustProfileByName(cred.ClaudeCodeProfileName), srcDir, destDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var staged struct {
		Model       string `json:"model"`
		Permissions struct {
			Dirs []string `json:"additionalDirectories"`
		} `json:"permissions"`
		SkipPrompt bool `json:"skipDangerousModePermissionPrompt"`
	}
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatal(err)
	}
	if staged.Model != "m" || len(staged.Permissions.Dirs) != 1 || staged.Permissions.Dirs[0] != "/" {
		t.Errorf("want model kept and additionalDirectories [\"/\"], got %s", data)
	}
	if !staged.SkipPrompt {
		t.Errorf("want skipDangerousModePermissionPrompt true, got %s", data)
	}
	if strings.Contains(string(data), "defaultMode") {
		t.Errorf("defaultMode must not be staged (triggers auto-mode offer dialog), got %s", data)
	}
	// Host settings must be untouched.
	if got, _ := os.ReadFile(filepath.Join(srcDir, "settings.json")); string(got) != host {
		t.Errorf("host settings.json modified: %s", got)
	}
}

// isNexusReadAnywherePerms reports whether (key, v) is the only "permissions"
// value nexus itself injects, so host-permission leak checks can tell it apart.
func isNexusReadAnywherePerms(key string, v json.RawMessage) bool {
	var p struct {
		Dirs []string `json:"additionalDirectories"`
	}
	var all map[string]json.RawMessage
	return key == "permissions" && json.Unmarshal(v, &p) == nil && json.Unmarshal(v, &all) == nil &&
		len(all) == 1 && len(p.Dirs) == 1 && p.Dirs[0] == "/"
}
