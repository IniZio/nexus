//go:build herdr_live

package cli

import (
	"context"
	"crypto/rand"
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

// isRealConfigRoot reports whether probeHome resolves to the same herdr
// config root as the operator's real HOME.
func isRealConfigRoot(probeHome, realHome string) bool {
	return filepath.Clean(probeHome+"/.config/herdr") == filepath.Clean(realHome+"/.config/herdr")
}

func randHexN(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func overrideHomeEnv(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, "HOME=") {
			out = append(out, e)
		}
	}
	return append(out, "HOME="+home)
}

func herdrCmd(probeHome, sessionName string, args ...string) *exec.Cmd {
	var fullArgs []string
	if sessionName != "" {
		fullArgs = append(fullArgs, "--session", sessionName)
	}
	fullArgs = append(fullArgs, args...)
	cmd := exec.Command("herdr", fullArgs...)
	cmd.Env = overrideHomeEnv(os.Environ(), probeHome)
	return cmd
}

func herdrRun(probeHome, sessionName string, args ...string) ([]byte, error) {
	return herdrCmd(probeHome, sessionName, args...).CombinedOutput()
}

func herdrVersionAtLeast(versionOutput string, major, minor, patch int) bool {
	for _, word := range strings.Fields(versionOutput) {
		v := strings.TrimPrefix(word, "v")
		parts := strings.SplitN(v, ".", 3)
		if len(parts) != 3 {
			continue
		}
		var maj, min, pat int
		if _, err := fmt.Sscanf(parts[0], "%d", &maj); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(parts[1], "%d", &min); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(parts[2], "%d", &pat); err != nil {
			continue
		}
		if maj != major {
			return maj > major
		}
		if min != minor {
			return min > minor
		}
		return pat >= patch
	}
	return false
}

// startIsolatedHerdr starts a herdr server in a fresh /tmp home with an isolated
// session.  t.Cleanup stops the server and removes the probe home.
func startIsolatedHerdr(t *testing.T) (probeHome, sessionName string) {
	t.Helper()

	if _, err := exec.LookPath("herdr"); err != nil {
		liveSkip(t, "herdr not found on PATH: %v", err)
	}

	vOut, err := exec.Command("herdr", "--version").CombinedOutput()
	if err != nil {
		liveSkip(t, "herdr --version failed: %v", err)
	}
	if !herdrVersionAtLeast(string(vOut), 0, 9, 0) {
		liveSkip(t, "herdr < 0.9.0 (got %q)", strings.TrimSpace(string(vOut)))
	}

	probeHome, err = os.MkdirTemp("/tmp", "hp") // /tmp avoids AF_UNIX 107-char sun_path limit
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	sessionName = fmt.Sprintf("nexus3-test-%s", randHexN(8))

	if isRealConfigRoot(probeHome, os.Getenv("HOME")) {
		t.Fatalf("REFUSAL: probeHome %q collides with real herdr config root", probeHome)
	}

	srvCmd := herdrCmd(probeHome, sessionName, "server")
	srvCmd.Stdout = nil
	srvCmd.Stderr = nil
	if err := srvCmd.Start(); err != nil {
		os.RemoveAll(probeHome)
		t.Fatalf("herdr server start: %v", err)
	}

	sockPath := filepath.Join(probeHome, ".config", "herdr", "sessions", sessionName, "herdr.sock")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sockPath); statErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, statErr := os.Stat(sockPath); statErr != nil {
		srvCmd.Process.Kill() //nolint:errcheck
		os.RemoveAll(probeHome)
		t.Fatalf("herdr server socket not ready after 10s at %s", sockPath)
	}

	capturedHome := probeHome
	capturedSess := sessionName
	t.Cleanup(func() {
		stopOut, stopErr := herdrRun(capturedHome, capturedSess, "server", "stop")
		if stopErr != nil {
			t.Logf("herdr server stop: %v\n%s", stopErr, stopOut)
		}
		time.Sleep(300 * time.Millisecond)
		delOut, delErr := herdrRun(capturedHome, "", "session", "delete", capturedSess)
		if delErr != nil {
			t.Logf("herdr session delete: %v\n%s", delErr, delOut)
		}
		os.RemoveAll(capturedHome)
	})

	return probeHome, sessionName
}

func fakeReleaseServer(t *testing.T, binaryContent []byte) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(binaryContent)
	checksum := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/nexus3-linux-amd64"):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(binaryContent)
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			fmt.Fprintf(w, "%s  nexus3-linux-amd64\n", checksum)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func buildNexus3Binary(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/nexus3")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		liveSkip(t, "go build ./cmd/nexus3 failed: %v\n%s", err, out)
	}
	return binary
}

func buildNexus3BinaryVersioned(t *testing.T, ver string) string {
	t.Helper()
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3")
	ldflag := fmt.Sprintf("-X github.com/IniZio/nexus3/internal/cli.version=%s", ver)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-ldflags", ldflag, "-o", binary, "./cmd/nexus3")
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
		"NEXUS3_RELEASE_BASE_URL=" + baseURL,
		"INSTALL_DIR=" + installDir,
		"NEXUS3_SHIM_DIR=" + shimDir,
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
    echo "nexus3 %s (go1.26.6)"
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

// TestHerdrRefusalGuard proves isRealConfigRoot fires when probeHome would
// collide with the operator's real herdr config root (T5-AC3).
func TestHerdrRefusalGuard(t *testing.T) {
	realHome := os.Getenv("HOME")
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
// D1: herdr plugin link does not run [[build]]; build.sh is invoked directly.
// runBuildSh sets HERDR_PLUGIN_ROOT to match the env herdr passes to the hook.
func TestHerdrPluginInstall_FreshHome(t *testing.T) {
	probeHome, sess := startIsolatedHerdr(t)
	nexus3Bin := buildNexus3Binary(t)

	binaryContent, err := os.ReadFile(nexus3Bin)
	if err != nil {
		t.Fatalf("read nexus3 binary: %v", err)
	}

	srv := fakeReleaseServer(t, binaryContent)
	installDir := t.TempDir()
	shimDir := t.TempDir()

	out, err := runBuildSh(t, probeHome, installDir, shimDir, srv.URL)
	if err != nil {
		t.Fatalf("build.sh failed: %v\n%s", err, out)
	}
	t.Logf("build.sh output:\n%s", out)

	installedBin := filepath.Join(installDir, "nexus3")
	fi, err := os.Stat(installedBin)
	if err != nil {
		t.Fatalf("installed binary not found at %s: %v", installedBin, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("installed binary %s is not executable (mode %o)", installedBin, fi.Mode())
	}

	shimPath := filepath.Join(shimDir, "nexus3-shim.sh")
	if _, err := os.Stat(shimPath); err != nil {
		t.Fatalf("nexus3-shim.sh not found at %s: %v", shimPath, err)
	}

	outStr := string(out)
	if !strings.Contains(outStr, "nexus3 plugin: installed") && !strings.Contains(outStr, "nexus3 plugin: shim written") {
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
	assertPluginListed(t, listOut, "nexus3")
}

// TestHerdrPluginInstall_Upgrade tests build.sh skip-if-newer behaviour with
// three stubs: older (exit 10 → download), dev (exit 12 → keep), newer (exit 11 → keep).
func TestHerdrPluginInstall_Upgrade(t *testing.T) {
	probeHome, _ := startIsolatedHerdr(t)
	nexus3Bin := buildNexus3Binary(t)

	binaryContent, err := os.ReadFile(nexus3Bin)
	if err != nil {
		t.Fatalf("read nexus3 binary: %v", err)
	}

	srv := fakeReleaseServer(t, binaryContent)

	t.Run("A_UpgradeOlder", func(t *testing.T) {
		installDir := t.TempDir()
		shimDir := t.TempDir()
		stubPath := filepath.Join(installDir, "nexus3")
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
		if !strings.Contains(outStr, "nexus3 plugin: installed") {
			t.Errorf("sub-test A: 'nexus3 plugin: installed' not found: %s", outStr)
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
		writeStubBinary(t, filepath.Join(installDir, "nexus3"), "v99.99.99-dev+abc", int(vcoDev))

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
		writeStubBinary(t, filepath.Join(installDir, "nexus3"), "v999.0.0", int(vcoInstalledNewer))

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

// TestHerdrPluginStartup_SkewNotice verifies that nexus3 herdr version-check
// emits herdrUpdateRemedy (exit 10) when the pin file names a newer release.
//
// The binary is built with -ldflags version=0.1.0 so the comparison is a
// proper semver check rather than the always-exit-12 dev-build fast-path.
func TestHerdrPluginStartup_SkewNotice(t *testing.T) {
	probeHome, _ := startIsolatedHerdr(t)
	nexus3Bin := buildNexus3Binary(t)

	binaryContent, err := os.ReadFile(nexus3Bin)
	if err != nil {
		t.Fatalf("read nexus3 binary: %v", err)
	}

	versionedBin := buildNexus3BinaryVersioned(t, "0.1.0")

	srv := fakeReleaseServer(t, binaryContent)
	installDir := t.TempDir()
	shimDir := t.TempDir()

	out, err := runBuildSh(t, probeHome, installDir, shimDir, srv.URL)
	if err != nil {
		t.Fatalf("build.sh failed: %v\n%s", err, out)
	}

	installedBin := filepath.Join(installDir, "nexus3")
	versionedContent, err := os.ReadFile(versionedBin)
	if err != nil {
		t.Fatalf("read versioned binary: %v", err)
	}
	if err := os.WriteFile(installedBin, versionedContent, 0o755); err != nil {
		t.Fatalf("replace installed binary: %v", err)
	}

	pinFile := filepath.Join(t.TempDir(), "nexus3-version")
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
