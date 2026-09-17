//go:build herdr_live

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeReleaseServer(t *testing.T, binaryContent []byte) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(binaryContent)
	checksum := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/nexus-linux-amd64"):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(binaryContent)
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			fmt.Fprintf(w, "%s  nexus-linux-amd64\n", checksum)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func buildNexusBinary(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/nexus")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		liveSkip(t, "go build ./cmd/nexus failed: %v\n%s", err, out)
	}
	return binary
}

func buildNexusBinaryVersioned(t *testing.T, ver string) string {
	t.Helper()
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus")
	ldflag := fmt.Sprintf("-X github.com/IniZio/nexus/internal/cli.version=%s", ver)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-ldflags", ldflag, "-o", binary, "./cmd/nexus")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		liveSkip(t, "go build (versioned) failed: %v\n%s", err, out)
	}
	return binary
}

func pluginDirPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "plugins", "herdr")
	if _, err := os.Stat(filepath.Join(dir, "herdr-plugin.toml")); err != nil {
		t.Fatalf("plugins/herdr/herdr-plugin.toml not found: %v", err)
	}
	return dir
}

func runBuildSh(t *testing.T, probeHome, installDir, shimDir, baseURL string, extraEnv ...string) ([]byte, error) {
	t.Helper()
	env := []string{
		"HOME=" + probeHome,
		"PATH=" + os.Getenv("PATH"),
		"NEXUS_RELEASE_BASE_URL=" + baseURL,
		"INSTALL_DIR=" + installDir,
		"NEXUS_SHIM_DIR=" + shimDir,
		"HERDR_PLUGIN_ROOT=" + pluginDirPath(t),
	}
	env = append(env, extraEnv...)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "build.sh")
	cmd.Dir = pluginDirPath(t)
	cmd.Env = env
	return cmd.CombinedOutput()
}

func writeStubBinary(t *testing.T, path, ver string, vcExit int) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "version" ]; then
    echo "nexus %s (go1.26.6)"
elif [ "$1" = "--version" ]; then
    exit 1
elif [ "$1" = "herdr" ] && [ "$2" = "version-check" ]; then
    exit %d
elif [ "$1" = "herdr" ] && [ "$2" = "abi" ]; then
    echo "3"
fi
exit 0
`, ver, vcExit)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writeStubBinary %s: %v", path, err)
	}
}

func assertPluginListed(t *testing.T, listOut []byte, pluginName string) {
	t.Helper()
	var resp struct {
		Result struct {
			Plugins []struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"plugins"`
		} `json:"result"`
	}
	if json.Unmarshal(listOut, &resp) == nil && len(resp.Result.Plugins) > 0 {
		for _, p := range resp.Result.Plugins {
			if strings.Contains(p.Name, pluginName) && p.Enabled {
				return
			}
		}
		t.Errorf("plugin %q (enabled=true) not in plugin list: %s", pluginName, listOut)
		return
	}
	var arr []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if json.Unmarshal(listOut, &arr) == nil && len(arr) > 0 {
		for _, p := range arr {
			if strings.Contains(p.Name, pluginName) && p.Enabled {
				return
			}
		}
		t.Errorf("plugin %q (enabled=true) not in plugin list: %s", pluginName, listOut)
		return
	}
	if !strings.Contains(string(listOut), pluginName) {
		t.Errorf("plugin %q not found in plugin list output: %s", pluginName, listOut)
	}
}

func TestHerdrRefusalGuard(t *testing.T) {
	realHome := herdrLiveRealHome
	if !isRealConfigRoot(realHome, realHome) {
		t.Error("refusal guard must fire when probeHome == real HOME")
	}
	tmp, err := os.MkdirTemp("/tmp", "hp")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmp)
	if isRealConfigRoot(tmp, realHome) {
		t.Errorf("refusal guard must NOT fire for isolated temp dir %q", tmp)
	}
}

// TestHerdrPluginInstall_FreshHome runs build.sh against a fresh isolated
// herdr session (no pre-existing binary → download path) and verifies the
// binary and shim are installed, then links the plugin and lists it.
//
// Mutation note: removing --write-config from the build.sh install-default-shell
// call causes the config.toml assertion below to fail. Removing the shim-write
// step causes the shim assertion to fail. build.sh is invoked directly; herdr
// plugin link is not used (herdr 0.9.0 never runs [[build]] on link).
func TestHerdrPluginInstall_FreshHome(t *testing.T) {
	probeHome, sess := startIsolatedHerdr(t)
	nexusBin := buildNexusBinary(t)

	binaryContent, err := os.ReadFile(nexusBin)
	if err != nil {
		t.Fatalf("read nexus binary: %v", err)
	}

	srv := fakeReleaseServer(t, binaryContent)
	installDir := t.TempDir()
	shimDir := t.TempDir()

	out, err := runBuildSh(t, probeHome, installDir, shimDir, srv.URL)
	if err != nil {
		t.Fatalf("build.sh failed: %v\n%s", err, out)
	}
	t.Logf("build.sh output:\n%s", out)

	installedBin := filepath.Join(installDir, "nexus")
	fi, err := os.Stat(installedBin)
	if err != nil {
		t.Fatalf("installed binary not found at %s: %v", installedBin, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("installed binary %s is not executable (mode %o)", installedBin, fi.Mode())
	}

	shimPath := filepath.Join(shimDir, "nexus-shim.sh")
	if _, err := os.Stat(shimPath); err != nil {
		t.Fatalf("nexus-shim.sh not found at %s: %v", shimPath, err)
	}

	outStr := string(out)
	if !strings.Contains(outStr, "nexus plugin: installed") && !strings.Contains(outStr, "nexus plugin: shim written") {
		t.Errorf("build.sh output missing install confirmation:\n%s", outStr)
	}

	linkOut, linkErr := herdrRun(probeHome, sess, "plugin", "link", pluginDirPath(t))
	if linkErr != nil {
		t.Fatalf("herdr plugin link: %v\n%s", linkErr, linkOut)
	}
	t.Logf("plugin link: %s", linkOut)

	listOut, listErr := herdrRun(probeHome, sess, "plugin", "list", "--json")
	if listErr != nil {
		t.Fatalf("herdr plugin list --json: %v\n%s", listErr, listOut)
	}
	t.Logf("plugin list: %s", listOut)
	assertPluginListed(t, listOut, "nexus")

	// Mutation note: removing --write-config from build.sh's install-default-shell call
	// causes this assertion to fail — the config file is never written.
	configPath := filepath.Join(probeHome, ".config", "herdr", "config.toml")
	configBytes, cfgErr := os.ReadFile(configPath)
	if cfgErr != nil {
		t.Errorf("herdr config.toml not found at %s: %v", configPath, cfgErr)
	} else {
		configStr := string(configBytes)
		guestShellPath := filepath.Join(probeHome, ".local", "bin", "nexus-guest-shell")
		if !strings.Contains(configStr, "default_shell") {
			t.Errorf("config.toml missing default_shell entry:\n%s", configStr)
		}
		if !strings.Contains(configStr, guestShellPath) {
			t.Errorf("config.toml default_shell does not point to guest-shell %q:\n%s",
				guestShellPath, configStr)
		}
		t.Logf("config.toml:\n%s", configStr)
	}

	ccOut, ccErr := herdrRun(probeHome, sess, "config", "check")
	if ccErr != nil {
		t.Errorf("herdr config check failed: %v\n%s", ccErr, ccOut)
	} else {
		t.Logf("herdr config check: %s", ccOut)
	}

	outStr = string(out)
	if !strings.Contains(outStr, "nexus plugin: shim written") {
		t.Errorf("build.sh did not reach shim-written step; full output:\n%s", outStr)
	}
}

// TestHerdrPluginInstall_Upgrade tests build.sh skip-if-newer behaviour with
// three stubs: older (exit 10 → download), dev (exit 12 → keep), newer (exit 11 → keep).
func TestHerdrPluginInstall_Upgrade(t *testing.T) {
	probeHome, _ := startIsolatedHerdr(t)
	nexusBin := buildNexusBinary(t)

	binaryContent, err := os.ReadFile(nexusBin)
	if err != nil {
		t.Fatalf("read nexus binary: %v", err)
	}

	srv := fakeReleaseServer(t, binaryContent)

	t.Run("A_UpgradeOlder", func(t *testing.T) {
		installDir := t.TempDir()
		shimDir := t.TempDir()
		stubPath := filepath.Join(installDir, "nexus")
		writeStubBinary(t, stubPath, "v0.0.1", int(vcoPinNewer))

		out, err := runBuildSh(t, probeHome, installDir, shimDir, srv.URL)
		t.Logf("build.sh output (A):\n%s", out)
		if err != nil {
			t.Fatalf("build.sh failed (A): %v\n%s", err, out)
		}
		outStr := string(out)
		if strings.Contains(outStr, "kept") {
			t.Errorf("sub-test A: expected download, got 'kept': %s", outStr)
		}
		if !strings.Contains(outStr, "nexus plugin: installed") && !strings.Contains(outStr, "nexus plugin: upgraded") {
			t.Errorf("sub-test A: neither 'installed' nor 'upgraded' in output: %s", outStr)
		}
		fi, err := os.Stat(stubPath)
		if err != nil {
			t.Fatalf("sub-test A: installed binary missing: %v", err)
		}
		if fi.Size() == 0 {
			t.Errorf("sub-test A: installed binary is empty")
		}
	})

	t.Run("B_KeepDevBuild", func(t *testing.T) {
		installDir := t.TempDir()
		shimDir := t.TempDir()
		writeStubBinary(t, filepath.Join(installDir, "nexus"), "v99.99.99-dev+abc", int(vcoDev))

		out, err := runBuildSh(t, probeHome, installDir, shimDir, srv.URL)
		t.Logf("build.sh output (B):\n%s", out)
		if err != nil {
			t.Fatalf("build.sh failed (B): %v\n%s", err, out)
		}
		outStr := string(out)
		if !strings.Contains(outStr, "kept") {
			t.Errorf("sub-test B: expected 'kept': %s", outStr)
		}
		if !strings.Contains(outStr, "dev build") {
			t.Errorf("sub-test B: expected 'dev build': %s", outStr)
		}
	})

	t.Run("C_KeepNewer", func(t *testing.T) {
		installDir := t.TempDir()
		shimDir := t.TempDir()
		writeStubBinary(t, filepath.Join(installDir, "nexus"), "v999.0.0", int(vcoInstalledNewer))

		out, err := runBuildSh(t, probeHome, installDir, shimDir, srv.URL)
		t.Logf("build.sh output (C):\n%s", out)
		if err != nil {
			t.Fatalf("build.sh failed (C): %v\n%s", err, out)
		}
		outStr := string(out)
		if !strings.Contains(outStr, "kept") {
			t.Errorf("sub-test C: expected 'kept': %s", outStr)
		}
		if !strings.Contains(outStr, "newer than") {
			t.Errorf("sub-test C: expected 'newer than': %s", outStr)
		}
	})
}

// TestHerdrPluginStartup_SkewNotice verifies that nexus herdr version-check
// emits herdrUpdateRemedy (exit 10) when the pin file names a newer release.
//
// The binary is built with -ldflags version=0.1.0 so the comparison is a
// proper semver check rather than the always-exit-12 dev-build fast-path.
func TestHerdrPluginStartup_SkewNotice(t *testing.T) {
	probeHome, _ := startIsolatedHerdr(t)
	nexusBin := buildNexusBinary(t)

	binaryContent, err := os.ReadFile(nexusBin)
	if err != nil {
		t.Fatalf("read nexus binary: %v", err)
	}

	versionedBin := buildNexusBinaryVersioned(t, "0.1.0")

	srv := fakeReleaseServer(t, binaryContent)
	installDir := t.TempDir()
	shimDir := t.TempDir()

	out, err := runBuildSh(t, probeHome, installDir, shimDir, srv.URL)
	if err != nil {
		t.Fatalf("build.sh failed: %v\n%s", err, out)
	}

	installedBin := filepath.Join(installDir, "nexus")
	versionedContent, err := os.ReadFile(versionedBin)
	if err != nil {
		t.Fatalf("read versioned binary: %v", err)
	}
	if err := os.WriteFile(installedBin, versionedContent, 0o755); err != nil {
		t.Fatalf("replace installed binary: %v", err)
	}

	pinFile := filepath.Join(t.TempDir(), "nexus-version")
	if err := os.WriteFile(pinFile, []byte("v999.99.99"), 0o644); err != nil {
		t.Fatalf("write pin file: %v", err)
	}

	vcCmd := exec.Command(installedBin, "herdr", "version-check", "--pin", pinFile)
	vcOut, _ := vcCmd.CombinedOutput()
	t.Logf("version-check output: %s (exit: %v)", vcOut, vcCmd.ProcessState.ExitCode())

	exitCode := vcCmd.ProcessState.ExitCode()
	if exitCode != int(vcoPinNewer) {
		t.Errorf("herdr version-check: want exit %d (vcoPinNewer), got %d\noutput: %s",
			vcoPinNewer, exitCode, vcOut)
	}
	if !strings.Contains(string(vcOut), herdrUpdateRemedy) {
		t.Errorf("herdr version-check missing update remedy %q\ngot: %s", herdrUpdateRemedy, vcOut)
	}
}

// TestHerdrContract_WorkspaceListIsolated verifies that herdr workspace list
// output in an isolated session parses correctly through herdrParseWorkspaceRefs.
func TestHerdrContract_WorkspaceListIsolated(t *testing.T) {
	probeHome, sess := startIsolatedHerdr(t)

	out, err := herdrRun(probeHome, sess, "workspace", "list")
	if err != nil {
		t.Fatalf("herdr workspace list: %v\n%s", err, out)
	}
	t.Logf("workspace list output: %s", out)

	refs, err := herdrParseWorkspaceRefs(out)
	if err != nil {
		t.Fatalf("herdrParseWorkspaceRefs: %v\nraw: %s", err, out)
	}
	if refs == nil {
		t.Error("herdrParseWorkspaceRefs returned nil; want empty slice")
	}
	t.Logf("parsed %d workspace refs", len(refs))
}
