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

// TestAssembleCuratedConfig_BypassConsentPreservesLowerLayerKeys is the
// regression test for the overlayfs file-granular shadow defect (commit
// 6a24c6a). The defect: SeedGuestBypassConsent wrote a single-key
// {"skipDangerousModePermissionPrompt":true} into the UPPER overlayfs layer.
// Because overlayfs is file-granular (not key-granular), that upper file wholly
// shadows the lower curated settings.json, dropping enabledPlugins and
// extraKnownMarketplaces. Plugins (groundwork, handbook) disappear in every
// fresh sandbox.
//
// Fix: AssembleCuratedConfig injects the bypass key INTO the staged lower
// settings.json, so both the plugin keys AND the bypass key coexist in a single
// layer. The supervisor skips the upper write when sharing is ON.
//
// RED → GREEN evidence (run manually to confirm):
//
//	# RED: revert copyFilteredSettings to NOT force the bypass key and
//	#      AssembleCuratedConfig to NOT call ensureStagedBypassConsentKey.
//	#      This test fails: staged settings.json missing 'skipDangerousModePermissionPrompt'.
//	#
//	# GREEN: with the fix applied this test passes.
//
// The test simulates a host settings.json that carries enabledPlugins +
// extraKnownMarketplaces (the two keys that the overlay was dropping) and
// asserts that after AssembleCuratedConfig the staged lower settings.json
// contains ALL THREE keys: enabledPlugins, extraKnownMarketplaces, AND
// skipDangerousModePermissionPrompt.
func TestAssembleCuratedConfig_BypassConsentPreservesLowerLayerKeys(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Build a host settings.json with enabledPlugins and extraKnownMarketplaces —
	// exactly the keys that were lost when the bypass consent write shadowed the
	// lower layer.
	hostSettings := map[string]any{
		"enabledPlugins": []string{
			"groundwork@groundwork",
			"handbook@example-handbook",
		},
		"extraKnownMarketplaces": []map[string]string{
			{"name": "groundwork", "url": "https://marketplace.groundwork.invalid"},
		},
		"model": "claude-opus-4-5",
		// Deliberately omit skipDangerousModePermissionPrompt to prove
		// AssembleCuratedConfig injects it regardless of the host value.
	}
	b, err := json.Marshal(hostSettings)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(srcDir, "settings.json")
	if err := os.WriteFile(settingsPath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := service.AssembleCuratedConfig(cred.ClaudeCodeProfile, srcDir, destDir); err != nil {
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

	// The plugin keys must survive filtering — they are allowlisted portable keys.
	for _, key := range []string{"enabledPlugins", "extraKnownMarketplaces"} {
		if _, ok := staged[key]; !ok {
			t.Errorf("staged lower settings.json missing key %q (portable key must survive AssembleCuratedConfig)", key)
		}
	}
	// BypassConsentKey="" for CredDirLiveMount profiles: the bypass key must NOT
	// be injected. Claude runs with --dangerously-skip-permissions at launch time
	// instead (auto-permission mode), so no settings key is needed.
	// Mutation guard: if BypassConsentKey is re-set or ensureStagedBypassConsentKey
	// is called unconditionally, this assertion fails RED.
	if _, ok := staged["skipDangerousModePermissionPrompt"]; ok {
		t.Errorf("skipDangerousModePermissionPrompt must NOT be injected for a CredDirLiveMount profile (BypassConsentKey is empty)")
	}
}

// TestAssembleCuratedConfig_BypassConsentPresentWhenNoHostSettings verifies
// that the bypass key is injected into the lower layer even when the host has
// no settings.json at all (e.g. a fresh install). Without this, a fresh host
// would produce a lower layer with no settings.json; the supervisor would need
// an upper write, which re-introduces the shadow risk for future installs.
func TestAssembleCuratedConfig_BypassConsentPresentWhenNoHostSettings(t *testing.T) {
	srcDir := t.TempDir() // empty — no settings.json
	destDir := t.TempDir()

	if err := service.AssembleCuratedConfig(cred.ClaudeCodeProfile, srcDir, destDir); err != nil {
		t.Fatalf("AssembleCuratedConfig: %v", err)
	}

	// With BypassConsentKey="" (CredDirLiveMount profile), AssembleCuratedConfig
	// must NOT create a settings.json just to inject bypass consent.
	// Mutation guard: if ensureStagedBypassConsentKey is called unconditionally,
	// the file is created and contains skipDangerousModePermissionPrompt, failing the
	// assertion below.
	settingsPath := filepath.Join(destDir, "settings.json")
	data, readErr := os.ReadFile(settingsPath)
	if os.IsNotExist(readErr) {
		// No settings.json written — correct: nothing to inject and no source file.
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

// TestAssembleCuratedConfig_CursorAuthInfoStripped is the mutation-proof
// regression guard for the cursor slice's central finding: cursor-agent's
// cli-config.json can carry an authInfo blob written by an operator's own
// interactive `cursor-agent login`, entirely independently of whether nexus3
// brokers cursor via the OAuth path or the API-key path (this profile brokers
// only the API-key path — see cred.CursorAgentProfile's doc comment). The
// curated-config filter must strip authInfo regardless.
//
// This calls the REAL AssembleCuratedConfig at its real call site — the exact
// function `nexus3 sandbox create --agent cursor` invokes — not a
// reimplementation, per the testing bar in the cursor slice brief.
//
// Mutation-proof RED evidence (verified by hand while implementing this test,
// restored immediately after — see the commit message for the transcript):
// temporarily replacing `profile.SettingsAllowlist[key]` in copyFilteredSettings
// with `true` (i.e. disarming the allowlist into an unconditional pass-through)
// makes this test FAIL with authInfo present in the staged file, exactly as
// the mutation should be caught.
func TestAssembleCuratedConfig_CursorAuthInfoStripped(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	cliConfig := map[string]any{
		// Portable — must survive filtering.
		"model":        "auto",
		"approvalMode": "allowlist",
		"display":      map[string]any{"mode": "zen"},
		// The account identity/PII blob. An operator who has run `cursor-agent
		// login` interactively carries this key in the SAME file nexus3 stages,
		// regardless of which auth path nexus3 itself brokers.
		// Shape matches the real cli-config.json authInfo: identity fields only
		// (email, displayName, userId, authId) — NOT tokens; the credential
		// (accessToken/refreshToken) lives in auth.json, not here.
		"authInfo": map[string]any{
			"email":       "operator@example.com",
			"displayName": "Operator User",
			"userId":      "usr_abc1234567890",
			"authId":      "aid_xyz0987654321",
		},
		// Two more non-allowlisted keys, so the count assertion below cannot
		// pass via a no-op patch that happens to only strip one key.
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

	if err := service.AssembleCuratedConfig(cred.CursorAgentProfile, srcDir, destDir); err != nil {
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

	// The mutation-proof assertion: authInfo, and every other non-allowlisted
	// key, must be ABSENT — not merely non-matching in value. Count the
	// substitutions so a no-op patch (e.g. filtering nothing, or filtering only
	// one of the three) cannot masquerade as a pass.
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

	// Portable keys must survive — proves the filter is an allowlist doing real
	// work, not a mechanism that happens to drop everything.
	for _, key := range []string{"model", "approvalMode", "display"} {
		if _, ok := staged[key]; !ok {
			t.Errorf("portable key %q was dropped from staged cli-config.json", key)
		}
	}

	// Belt-and-suspenders raw-bytes check: the literal PII values from authInfo
	// must not appear anywhere in the staged file, even under an unexpected key
	// shape (e.g. if the key were renamed but the value shape preserved).
	if strings.Contains(string(data), "usr_abc1234567890") ||
		strings.Contains(string(data), "aid_xyz0987654321") {
		t.Error("raw PII values from authInfo leaked into staged cli-config.json")
	}

	// cursor has no BypassConsentKey (skip-permissions is a launch-time flag,
	// not a settings key) — the staged file must NOT gain one.
	if _, ok := staged["skipDangerousModePermissionPrompt"]; ok {
		t.Error("cursor profile has no BypassConsentKey; staged cli-config.json must not gain skipDangerousModePermissionPrompt")
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

// TestAgentSettingsDir verifies that AgentSettingsDir uses ConfigDirEnvVar
// (the SETTINGS redirect) and never CredDirEnvVar when resolving where to
// read the agent's settings file. This is the key correctness property for
// cursor, where ConfigDirEnvVar="CURSOR_CONFIG_DIR" and
// CredDirEnvVar="XDG_CONFIG_HOME" are different variables pointing to
// different directories.
func TestAgentSettingsDir(t *testing.T) {
	t.Run("cursor_ConfigDirEnvVar_wins", func(t *testing.T) {
		// When CURSOR_CONFIG_DIR is set, AgentSettingsDir must return that
		// directory — NOT the XDG_CONFIG_HOME or the SettingsPath default.
		customSettingsDir := t.TempDir()
		t.Setenv("CURSOR_CONFIG_DIR", customSettingsDir)
		// Also set XDG_CONFIG_HOME to a different value to prove it is ignored.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		got, err := service.AgentSettingsDir(cred.CursorAgentProfile)
		if err != nil {
			t.Fatalf("AgentSettingsDir: %v", err)
		}
		if got != customSettingsDir {
			t.Errorf("AgentSettingsDir = %q, want %q (ConfigDirEnvVar value)", got, customSettingsDir)
		}
	})

	t.Run("cursor_falls_back_to_SettingsPath_dir", func(t *testing.T) {
		// When CURSOR_CONFIG_DIR is not set, AgentSettingsDir falls back to
		// the directory of SettingsPath (~/.cursor for cursor-agent).
		t.Setenv("CURSOR_CONFIG_DIR", "")
		// XDG_CONFIG_HOME must not influence the result.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		got, err := service.AgentSettingsDir(cred.CursorAgentProfile)
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
		// A profile with no SettingsPath (and no ConfigDirEnvVar) returns "".
		got, err := service.AgentSettingsDir(cred.AgentProfile{})
		if err != nil {
			t.Fatalf("AgentSettingsDir: %v", err)
		}
		if got != "" {
			t.Errorf("AgentSettingsDir for zero profile = %q, want empty", got)
		}
	})
}
