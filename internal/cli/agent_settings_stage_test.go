package cli

// TestStageAgentCuratedConfig_UsesConfigDirEnvVar is the mutation-proof
// adoption guard for the cmd_sandbox.go call site. It proves that
// stageAgentCuratedConfig uses service.AgentSettingsDir (ConfigDirEnvVar)
// and never filepath.Dir(profile.SettingsPath), which ignores ConfigDirEnvVar.
//
// Design: CURSOR_CONFIG_DIR is set to a temp dir containing a cli-config.json
// with a distinctive sentinel model value ("nexus3-ConfigDirEnvVar-sentinel").
// This value cannot appear in the operator's real ~/.cursor/cli-config.json.
// The test asserts the staged model value equals the sentinel; if the call
// site reverts to filepath.Dir(profile.SettingsPath) it reads from ~/.cursor
// and the sentinel is absent, causing the assertion to fail.
//
// Mutation proof RED evidence (verified during implementation):
//
//	Mutant: service.AgentSettingsDir(profile) → filepath.Dir(profile.SettingsPath)
//	go vet: clean (compiles).
//	--- FAIL: TestStageAgentCuratedConfig_UsesConfigDirEnvVar (0.00s)
//	    agent_settings_stage_test.go:XX: staged "model" = <actual_value>, want
//	        sentinel "nexus3-ConfigDirEnvVar-sentinel" — stageAgentCuratedConfig
//	        read from ~/.cursor instead of CURSOR_CONFIG_DIR
//	FAIL
import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// configDirEnvVarSentinel is written into the fake cli-config.json as the
// "model" value. It cannot appear in any real cursor install because cursor
// rejects it as an invalid model name. Its presence in the staged file proves
// stageAgentCuratedConfig read from CURSOR_CONFIG_DIR, not from ~/.cursor.
const configDirEnvVarSentinel = "nexus3-ConfigDirEnvVar-sentinel"

func TestStageAgentCuratedConfig_UsesConfigDirEnvVar(t *testing.T) {
	fakeSettingsDir := t.TempDir()
	cliConfig := map[string]any{
		// Sentinel in an allowlisted key. The staged file must carry exactly
		// this value — proves the source was CURSOR_CONFIG_DIR, not ~/.cursor.
		"model":        configDirEnvVarSentinel,
		"approvalMode": "allowlist",
		// Non-allowlisted — must be stripped. Proves the allowlist ran.
		"privacyCache": map[string]any{"fingerprint": "host-specific"},
	}
	b, err := json.Marshal(cliConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeSettingsDir, "cli-config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	// Direct CURSOR_CONFIG_DIR (cursor's ConfigDirEnvVar) at the fake dir.
	// Also set XDG_CONFIG_HOME to a different temp dir to prove CredDirEnvVar
	// is never consulted — a regression to filepath.Dir would bypass both.
	t.Setenv("CURSOR_CONFIG_DIR", fakeSettingsDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	destDir := t.TempDir()
	if err := stageAgentCuratedConfig(cred.CursorAgentProfile, destDir); err != nil {
		t.Fatalf("stageAgentCuratedConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "cli-config.json"))
	if err != nil {
		t.Fatalf("staged cli-config.json absent: %v\n"+
			"(likely filepath.Dir regression: reads from ~/.cursor, not CURSOR_CONFIG_DIR)", err)
	}

	var staged map[string]json.RawMessage
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatalf("staged cli-config.json not valid JSON: %v", err)
	}

	// The sentinel value in "model" must match exactly — proves the source.
	rawModel, ok := staged["model"]
	if !ok {
		t.Fatal("staged cli-config.json missing allowlisted key \"model\"")
	}
	var gotModel string
	if err := json.Unmarshal(rawModel, &gotModel); err != nil {
		t.Fatalf("cannot unmarshal staged \"model\": %v", err)
	}
	if gotModel != configDirEnvVarSentinel {
		t.Errorf("staged \"model\" = %q, want sentinel %q\n"+
			"stageAgentCuratedConfig read from the wrong directory (filepath.Dir regression?)",
			gotModel, configDirEnvVarSentinel)
	}

	// Belt-and-suspenders: sentinel bytes must appear in the raw staged file.
	if !strings.Contains(string(data), configDirEnvVarSentinel) {
		t.Errorf("sentinel %q absent from raw staged bytes — source directory was wrong", configDirEnvVarSentinel)
	}

	// Non-allowlisted key must be stripped.
	if _, ok := staged["privacyCache"]; ok {
		t.Error("non-allowlisted key \"privacyCache\" leaked into staged cli-config.json")
	}
}
