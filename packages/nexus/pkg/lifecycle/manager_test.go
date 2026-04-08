package lifecycle

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeExecutableScript(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func TestManager_AutodetectsLifecycleScripts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script execution semantics are Unix-specific")
	}

	root := t.TempDir()
	marker := filepath.Join(root, "setup-ran.txt")
	writeExecutableScript(t, root, ".nexus/lifecycles/setup.sh", "#!/usr/bin/env bash\n: > \""+marker+"\"\n")

	mgr, err := NewManager(root)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if err := mgr.RunPreStart(); err != nil {
		t.Fatalf("run pre-start: %v", err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected setup marker file: %v", err)
	}
}

func TestManager_AutodetectedScriptsUseStrictShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script execution semantics are Unix-specific")
	}

	root := t.TempDir()
	writeExecutableScript(t, root, ".nexus/lifecycles/setup.sh", "#!/usr/bin/env bash\necho \"$UNBOUND_VAR\"\n")

	mgr, err := NewManager(root)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if err := mgr.RunPreStart(); err == nil {
		t.Fatal("expected pre-start to fail due to strict shell unbound variable")
	}
}

func TestManager_FailsWhenAutodetectedScriptNotExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissions semantics are Unix-specific")
	}

	root := t.TempDir()
	path := filepath.Join(root, ".nexus", "lifecycles", "setup.sh")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := NewManager(root); err == nil {
		t.Fatal("expected NewManager to fail for non-executable autodetected script")
	}
}

func TestManager_ConfigLifecycleOverridesAutodetectedScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script execution semantics are Unix-specific")
	}

	root := t.TempDir()
	configMarker := filepath.Join(root, "config-ran.txt")
	scriptMarker := filepath.Join(root, "script-ran.txt")

	workspaceCfg := `{
  "version": 1,
  "runtime": {"required": ["linux"]},
  "lifecycle": {
    "onSetup": ["touch ` + configMarker + `"]
  }
}`
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nexus", "workspace.json"), []byte(workspaceCfg), 0o644); err != nil {
		t.Fatalf("write workspace.json: %v", err)
	}

	writeExecutableScript(t, root, ".nexus/lifecycles/setup.sh", "#!/usr/bin/env bash\n: > \""+scriptMarker+"\"\n")

	mgr, err := NewManager(root)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if err := mgr.RunPreStart(); err != nil {
		t.Fatalf("run pre-start: %v", err)
	}

	if _, err := os.Stat(configMarker); err != nil {
		t.Fatalf("expected config lifecycle command to run: %v", err)
	}
	if _, err := os.Stat(scriptMarker); err == nil {
		t.Fatal("expected autodetected setup script not to run when config onSetup is set")
	}
}
