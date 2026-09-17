package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakeHerdrScript(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "herdr")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHerdrWriteConfigTOML(t *testing.T) {
	self, _ := os.Executable()

	cases := []struct {
		name        string
		initial     string
		wantContent string
		wantChanged bool
		wantForeign string
	}{
		{
			name:        "absent",
			initial:     "",
			wantContent: fmt.Sprintf("[terminal]\ndefault_shell = %q\n", self),
			wantChanged: true,
		},
		{
			name:        "present_no_key",
			initial:     "[terminal]\nsome_other = \"foo\"\n",
			wantContent: fmt.Sprintf("[terminal]\ndefault_shell = %q\nsome_other = \"foo\"\n", self),
			wantChanged: true,
		},
		{
			name:        "present_no_section",
			initial:     "[other]\nfoo = \"bar\"\n",
			wantContent: fmt.Sprintf("[other]\nfoo = \"bar\"\n\n[terminal]\ndefault_shell = %q\n", self),
			wantChanged: true,
		},
		{
			name:        "present_exact_value",
			initial:     fmt.Sprintf("[terminal]\ndefault_shell = %q\n", self),
			wantContent: fmt.Sprintf("[terminal]\ndefault_shell = %q\n", self),
			wantChanged: false,
		},
		{
			name:        "present_foreign_shell",
			initial:     "[terminal]\ndefault_shell = \"/usr/bin/other-shell\"\n",
			wantContent: fmt.Sprintf("[terminal]\ndefault_shell = %q\n", self),
			wantChanged: true,
			wantForeign: "/usr/bin/other-shell",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.toml")
			if tc.initial != "" {
				if err := os.WriteFile(configPath, []byte(tc.initial), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			changed, backupPath, foreign, err := herdrWriteConfigTOML(configPath, self)
			if err != nil {
				t.Fatalf("herdrWriteConfigTOML: %v", err)
			}
			if changed != tc.wantChanged {
				t.Errorf("changed=%v want %v", changed, tc.wantChanged)
			}
			if foreign != tc.wantForeign {
				t.Errorf("foreign=%q want %q", foreign, tc.wantForeign)
			}
			got, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			if string(got) != tc.wantContent {
				t.Errorf("content mismatch:\ngot:  %q\nwant: %q", string(got), tc.wantContent)
			}

			changed2, _, _, err2 := herdrWriteConfigTOML(configPath, self)
			if err2 != nil {
				t.Fatalf("second call: %v", err2)
			}
			if changed2 {
				t.Error("second call changed=true (not idempotent)")
			}
			got2, _ := os.ReadFile(configPath)
			if string(got2) != string(got) {
				t.Error("second call altered file content")
			}

			if tc.wantForeign != "" {
				if backupPath == "" {
					t.Error("foreign shell case: want non-empty backupPath")
				} else if _, statErr := os.Stat(backupPath); statErr != nil {
					t.Errorf("backup file not found: %v", statErr)
				}
			}
		})
	}
}

func TestHerdrInstallDefaultShell_WriteConfig_ConfigCheckFail(t *testing.T) {
	if !herdrSkipInstallProbeForTest {
		t.Fatal("testmain_test.go must set herdrSkipInstallProbeForTest")
	}
	fakeHerdr := writeFakeHerdrScript(t, `case "$*" in
  *"config check"*) exit 1;;
esac
exit 0
`)
	t.Setenv("HERDR_BIN_PATH", fakeHerdr)

	home := t.TempDir()
	t.Setenv("HOME", home)
	xdgConfig := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	configDir := filepath.Join(xdgConfig, "herdr")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := "[terminal]\nsome_key = \"value\"\n"
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	err := runHerdrInstallDefaultShell(context.Background(), []string{"--write-config"}, &Output{w: &sb})
	if err == nil {
		t.Fatal("want error from config check failure, got nil")
	}

	got, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	if string(got) != original {
		t.Errorf("config not restored:\ngot:  %q\nwant: %q", string(got), original)
	}
	combined := err.Error() + sb.String()
	if !strings.Contains(combined, "default_shell") {
		t.Errorf("paste instruction missing from output+error: %q", combined)
	}
}

func TestHerdrInstallDefaultShell_WriteConfig_ForeignShellChains(t *testing.T) {
	if !herdrSkipInstallProbeForTest {
		t.Fatal("testmain_test.go must set herdrSkipInstallProbeForTest")
	}
	fakeHerdr := writeFakeHerdrScript(t, "exit 0\n")
	t.Setenv("HERDR_BIN_PATH", fakeHerdr)

	home := t.TempDir()
	t.Setenv("HOME", home)
	xdgConfig := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	foreignShell := writeFakeHerdrScript(t, "exit 0\n")
	foreignShell = filepath.Join(filepath.Dir(foreignShell), "other-guest-shell")
	if err := os.Rename(filepath.Join(filepath.Dir(foreignShell), "herdr"), foreignShell); err != nil {
		t.Fatal(err)
	}

	configDir := filepath.Join(xdgConfig, "herdr")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	original := fmt.Sprintf("[terminal]\ndefault_shell = %q\n", foreignShell)
	if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	if err := runHerdrInstallDefaultShell(context.Background(), []string{"--write-config"}, &Output{w: &sb}); err != nil {
		t.Fatalf("install-default-shell: %v", err)
	}

	installPath := filepath.Join(home, ".local", "bin", "nexus-guest-shell")
	sidecarData, err := os.ReadFile(installPath + herdrSidecarSuffix)
	if err != nil {
		t.Fatalf("sidecar: %v", err)
	}
	_, _, chained := herdrParseSidecar(sidecarData)
	if chained != foreignShell {
		t.Errorf("sidecar chained shell = %q, want %q", chained, foreignShell)
	}

	entries, _ := os.ReadDir(configDir)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.toml.bak.") {
			found = true
		}
	}
	if !found {
		t.Error("no .bak file found in config dir")
	}
}
