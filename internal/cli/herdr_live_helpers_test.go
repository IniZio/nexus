//go:build herdr_live

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	herdrLiveRealHome        string
	herdrLiveIsolatedHome    string
	herdrLiveIsolatedSession string
	herdrLiveProbeHomes      []string
	herdrLiveProbeHomesMu    sync.Mutex
)

func initHerdrLiveEnv() {
	herdrLiveRealHome = os.Getenv("HOME")
	if os.Getenv("NEXUS3_TEST_SIGHUP_CHILD") == "1" {
		return
	}

	var gomodcache, gocache, gopath string
	if out, err := exec.Command("go", "env", "GOMODCACHE", "GOCACHE", "GOPATH").Output(); err == nil {
		if parts := strings.SplitN(strings.TrimSpace(string(out)), "\n", 3); len(parts) == 3 {
			gomodcache, gocache, gopath = parts[0], parts[1], parts[2]
		}
	}

	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "HERDR_") {
			if err := os.Unsetenv(k); err != nil {
				fmt.Fprintf(os.Stderr, "herdr_live: unsetenv %s: %v\n", k, err)
				os.Exit(1)
			}
		}
	}

	isolatedHome, err := os.MkdirTemp("/tmp", "nexus3-henv-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "herdr_live: create isolated home: %v\n", err)
		os.Exit(1)
	}

	if isRealConfigRoot(isolatedHome, herdrLiveRealHome) {
		fmt.Fprintf(os.Stderr,
			"herdr_live REFUSAL: isolated home %q collides with real herdr "+
				"config root; set TMPDIR outside HOME\n", isolatedHome)
		os.RemoveAll(isolatedHome)
		os.Exit(2)
	}

	herdrLiveIsolatedHome = isolatedHome
	mustSetenv := func(k, v string) {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintf(os.Stderr, "herdr_live: set %s: %v\n", k, err)
			os.Exit(1)
		}
	}
	mustSetenv("HOME", isolatedHome)
	mustSetenv("XDG_CONFIG_HOME", filepath.Join(isolatedHome, ".config"))
	mustSetenv("XDG_STATE_HOME", filepath.Join(isolatedHome, ".local", "state"))

	if gomodcache != "" {
		mustSetenv("GOMODCACHE", gomodcache)
		mustSetenv("GOCACHE", gocache)
		mustSetenv("GOPATH", gopath)
	}
}

func removeAllForce(path string) error {
	filepath.Walk(path, func(p string, _ os.FileInfo, _ error) error { //nolint:errcheck
		os.Chmod(p, 0o755) //nolint:errcheck
		return nil
	})
	return os.RemoveAll(path)
}

func herdrLiveCleanup() {
	removeAllForce(herdrLiveIsolatedHome) //nolint:errcheck
	herdrLiveProbeHomesMu.Lock()
	homes := herdrLiveProbeHomes
	herdrLiveProbeHomesMu.Unlock()
	for _, h := range homes {
		if err := removeAllForce(h); err != nil {
			fmt.Fprintf(os.Stderr, "herdr_live: cleanup %s: %v\n", h, err)
		}
	}
}

func isRealConfigRoot(probeHome, realHome string) bool {
	return filepath.Clean(probeHome+"/.config/herdr") == filepath.Clean(realHome+"/.config/herdr")
}

func randHexN(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func buildHerdrEnv(env []string, probeHome string) []string {
	out := make([]string, 0, len(env)+3)
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k == "HOME" || k == "XDG_CONFIG_HOME" || k == "XDG_STATE_HOME" || strings.HasPrefix(k, "HERDR_") {
			continue
		}
		out = append(out, e)
	}
	return append(out,
		"HOME="+probeHome,
		"XDG_CONFIG_HOME="+filepath.Join(probeHome, ".config"),
		"XDG_STATE_HOME="+filepath.Join(probeHome, ".local", "state"),
	)
}

func herdrCmd(probeHome, sessionName string, args ...string) *exec.Cmd {
	var fullArgs []string
	if sessionName != "" {
		fullArgs = append(fullArgs, "--session", sessionName)
	}
	fullArgs = append(fullArgs, args...)
	cmd := exec.Command("herdr", fullArgs...)
	cmd.Env = buildHerdrEnv(os.Environ(), probeHome)
	return cmd
}

func herdrRun(probeHome, sessionName string, args ...string) ([]byte, error) {
	return herdrCmd(probeHome, sessionName, args...).CombinedOutput()
}

func herdrExec(args ...string) *exec.Cmd {
	return herdrCmd(os.Getenv("HOME"), herdrLiveIsolatedSession, args...)
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

	probeHome, err = os.MkdirTemp("/tmp", "hp")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	herdrLiveProbeHomesMu.Lock()
	herdrLiveProbeHomes = append(herdrLiveProbeHomes, probeHome)
	herdrLiveProbeHomesMu.Unlock()

	sessionName = fmt.Sprintf("nexus3-test-%s", randHexN(8))

	if isRealConfigRoot(probeHome, herdrLiveRealHome) {
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

	t.Setenv("HOME", probeHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(probeHome, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(probeHome, ".local", "state"))
	herdrLiveIsolatedSession = sessionName

	capturedHome := probeHome
	capturedSess := sessionName
	t.Cleanup(func() {
		herdrLiveIsolatedSession = ""
		stopOut, stopErr := herdrRun(capturedHome, capturedSess, "server", "stop")
		if stopErr != nil {
			fmt.Fprintf(os.Stderr, "herdr_live: server stop %s: %v\n%s\n", capturedSess, stopErr, stopOut)
		}
		time.Sleep(300 * time.Millisecond)
		delOut, delErr := herdrRun(capturedHome, "", "session", "delete", capturedSess)
		if delErr != nil {
			fmt.Fprintf(os.Stderr, "herdr_live: session delete %s: %v\n%s\n", capturedSess, delErr, delOut)
		}
		if err := removeAllForce(capturedHome); err != nil {
			fmt.Fprintf(os.Stderr, "herdr_live: RemoveAll %s: %v\n", capturedHome, err)
		}
	})

	return probeHome, sessionName
}
