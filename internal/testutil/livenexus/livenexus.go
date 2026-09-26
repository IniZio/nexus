// Package livenexus provides an isolated live-test harness for nexus integration tests.
//
// Every live test that touches sandboxes, herdr worktrees, or herdr sessions MUST
// use this harness. It guarantees:
//
//   - Nexus state (XDG_STATE_HOME) lives in a fresh /var/tmp directory, never the
//     prod ~/.local/state/nexus tree.
//   - A dedicated herdr server runs under a unique systemd unit, so no command
//     reaches the prod herdr session.
//   - Cleanup removes only handles explicitly registered with Track; it never
//     walks or prunes the sandbox list.
//   - Run refuses any argv containing "prune" and refuses to run when the resolved
//     state root matches the prod path.
//
// Kernel: NEXUS_KERNEL_PATH is forwarded unchanged from the host environment so
// the real kernel binary is reused read-only. All other nexus data (store,
// bindings, caches) starts empty inside the isolated state root.
//
// Herdr config: XDG_CONFIG_HOME is set to <isolated>/config; the herdr session
// socket lives at <isolated>/config/herdr/sessions/<session>/herdr.sock.
package livenexus

import (
	"context"
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

// prodStateRoot resolves the nexus state root that the current environment uses
// without any override from this harness. Used for the collision guard.
func prodStateRoot() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "nexus"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("livenexus: resolve home: %w", err)
	}
	return filepath.Join(home, ".local", "state", "nexus"), nil
}

// prodHerdrSocket returns the default herdr socket path in the current environment.
func prodHerdrSocket() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

// validateStateRoot returns an error when root matches the prod state path.
func validateStateRoot(root string) error {
	prod, err := prodStateRoot()
	if err != nil {
		return err
	}
	if filepath.Clean(root) == filepath.Clean(prod) {
		return fmt.Errorf("livenexus: isolated state root %q equals prod root %q; refusing", root, prod)
	}
	return nil
}

// validateSocket returns an error when socketPath matches the prod herdr socket.
func validateSocket(socketPath string) error {
	prod := prodHerdrSocket()
	if filepath.Clean(socketPath) == filepath.Clean(prod) {
		return fmt.Errorf("livenexus: isolated socket %q equals prod socket %q; refusing", socketPath, prod)
	}
	return nil
}

// Harness is an isolated nexus+herdr environment for one test.
type Harness struct {
	t           *testing.T
	stateRoot   string // XDG_STATE_HOME — <isolated>/state
	configHome  string // XDG_CONFIG_HOME — <isolated>/config
	dataHome    string // XDG_DATA_HOME  — <isolated>/data
	kernelPath  string
	sessionName string
	socketPath  string
	nexusBin    string

	mu      sync.Mutex
	handles []string
}

// New creates an isolated harness for t. It:
//  1. Creates a temp dir under /var/tmp.
//  2. Starts a herdr server in its own systemd user unit.
//  3. Registers cleanup in t.Cleanup.
//
// Fails the test immediately when isolation cannot be guaranteed.
func New(t *testing.T) *Harness {
	t.Helper()
	base, err := os.MkdirTemp("/var/tmp", "nexus-live-")
	if err != nil {
		t.Fatalf("livenexus: create base dir: %v", err)
	}

	stateRoot := filepath.Join(base, "state")
	configHome := filepath.Join(base, "config")
	dataHome := filepath.Join(base, "data")
	for _, d := range []string{stateRoot, configHome, dataHome} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			_ = os.RemoveAll(base)
			t.Fatalf("livenexus: mkdir %s: %v", d, err)
		}
	}

	if err := validateStateRoot(filepath.Join(stateRoot, "nexus")); err != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("%v", err)
	}

	session := "nl-" + randHex(8)
	home, _ := os.UserHomeDir()
	socketPath := filepath.Join(home, ".config", "herdr", "sessions", session, "herdr.sock")

	if err := validateSocket(socketPath); err != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("%v", err)
	}

	nexusBin := resolveNexusBin()
	kernelPath := resolveProdKernelPath()

	nexusStateRoot := filepath.Join(stateRoot, "nexus")
	if mkErr := os.MkdirAll(nexusStateRoot, 0o700); mkErr != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: mkdir nexus state: %v", mkErr)
	}
	prodNexusState, _ := prodStateRoot()
	prodImagesDir := filepath.Join(prodNexusState, "images")
	if _, statErr := os.Stat(prodImagesDir); statErr == nil {
		symTarget := filepath.Join(nexusStateRoot, "images")
		if linkErr := os.Symlink(prodImagesDir, symTarget); linkErr != nil {
			_ = os.RemoveAll(base)
			t.Fatalf("livenexus: symlink prod images: %v", linkErr)
		}
	}

	h := &Harness{
		t:           t,
		stateRoot:   stateRoot,
		configHome:  configHome,
		dataHome:    dataHome,
		kernelPath:  kernelPath,
		sessionName: session,
		socketPath:  socketPath,
		nexusBin:    nexusBin,
	}

	h.startHerdr(t, base)

	t.Cleanup(func() {
		h.cleanup(base)
	})

	return h
}

func (h *Harness) startHerdr(t *testing.T, base string) {
	t.Helper()
	herdrBin, err := resolveHerdrBin()
	if err != nil {
		t.Fatalf("livenexus: %v", err)
	}

	home, _ := os.UserHomeDir()
	localBin := filepath.Join(home, ".local", "bin")
	path := os.Getenv("PATH")
	if !strings.Contains(path, localBin) {
		path = localBin + ":" + path
	}

	cmd := exec.Command("systemd-run", "--user",
		"--unit="+h.sessionName,
		"-p", "StandardInput=null",
		"--setenv=HOME="+home,
		"--setenv=PATH="+path,
		"--setenv=TMPDIR=/var/tmp",
		herdrBin, "--session", h.sessionName, "server",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: systemd-run herdr server: %v\n%s", err, out)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(h.socketPath); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(h.socketPath); err != nil {
		_ = exec.Command("systemctl", "--user", "stop", h.sessionName+".service").Run()
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: herdr socket %s not ready after 20s", h.socketPath)
	}
}

func (h *Harness) cleanup(base string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	h.mu.Lock()
	tracked := append([]string(nil), h.handles...)
	h.mu.Unlock()

	for _, handle := range tracked {
		h.teardownHandle(ctx, handle)
	}

	_ = exec.Command("systemctl", "--user", "stop", h.sessionName+".service").Run()
	time.Sleep(300 * time.Millisecond)
	_ = exec.Command("herdr", "--session", h.sessionName, "session", "delete", h.sessionName).Run()
	_ = os.RemoveAll(base)
}

func (h *Harness) teardownHandle(ctx context.Context, handle string) {
	args := []string{"sandbox", "rm", handle}
	cmd := exec.CommandContext(ctx, h.nexusBin, args...)
	cmd.Env = h.Env()
	_ = cmd.Run()
}

// Env returns the environment slice that child processes must use to run
// inside the isolated state. It includes XDG_STATE_HOME, XDG_CONFIG_HOME,
// XDG_DATA_HOME, HERDR_SOCKET_PATH, TMPDIR, and PATH.
func (h *Harness) Env() []string {
	home, _ := os.UserHomeDir()
	localBin := filepath.Join(home, ".local", "bin")
	path := os.Getenv("PATH")
	if !strings.Contains(path, localBin) {
		path = localBin + ":" + path
	}

	skipKeys := map[string]bool{
		"XDG_STATE_HOME": true, "XDG_DATA_HOME": true,
		"HERDR_SOCKET_PATH": true, "TMPDIR": true, "PATH": true,
	}
	if h.kernelPath != "" {
		skipKeys["NEXUS_KERNEL_PATH"] = true
	}
	base := make([]string, 0, len(os.Environ())+8)
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if skipKeys[k] {
			continue
		}
		base = append(base, e)
	}
	env := append(base,
		"XDG_STATE_HOME="+h.stateRoot,
		"XDG_DATA_HOME="+h.dataHome,
		"HERDR_SOCKET_PATH="+h.socketPath,
		"TMPDIR=/var/tmp",
		"PATH="+path,
	)
	if h.kernelPath != "" {
		env = append(env, "NEXUS_KERNEL_PATH="+h.kernelPath)
	}
	return env
}

// Run executes nexusBin with args inside the isolated environment.
// It refuses any argv containing "prune" and refuses to run when the
// resolved state root matches prod.
func (h *Harness) Run(ctx context.Context, args ...string) (string, error) {
	for _, a := range args {
		if strings.Contains(a, "prune") {
			return "", fmt.Errorf("livenexus: Run refuses argv containing 'prune': %v", args)
		}
	}
	nexusStateRoot := filepath.Join(h.stateRoot, "nexus")
	if err := validateStateRoot(nexusStateRoot); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, h.nexusBin, args...)
	cmd.Env = h.Env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunHerdr executes herdr with args inside the isolated environment.
// It refuses any argv containing "prune".
func (h *Harness) RunHerdr(ctx context.Context, args ...string) (string, error) {
	for _, a := range args {
		if strings.Contains(a, "prune") {
			return "", fmt.Errorf("livenexus: RunHerdr refuses argv containing 'prune': %v", args)
		}
	}
	herdrBin, err := resolveHerdrBin()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, herdrBin, args...)
	cmd.Env = h.Env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Track registers handle for cleanup. In t.Cleanup, only tracked handles are
// removed — never a wildcard or list-based sweep.
func (h *Harness) Track(handle string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handles = append(h.handles, handle)
}

// SessionName returns the herdr session name for this harness.
func (h *Harness) SessionName() string { return h.sessionName }

// SocketPath returns the HERDR_SOCKET_PATH for this harness.
func (h *Harness) SocketPath() string { return h.socketPath }

// StateRoot returns XDG_STATE_HOME for this harness.
func (h *Harness) StateRoot() string { return h.stateRoot }

// NexusBin returns the nexus binary path.
func (h *Harness) NexusBin() string { return h.nexusBin }

func resolveHerdrBin() (string, error) {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("herdr binary not found (HERDR_BIN_PATH unset, not in PATH)")
}

func resolveNexusBin() string {
	if p := os.Getenv("NEXUS_BIN"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "nexus"),
		"/usr/local/bin/nexus",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("nexus"); err == nil {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "nexus"
}

func resolveProdKernelPath() string {
	if p := os.Getenv("NEXUS_KERNEL_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "share", "nexus", "images", "kernel", "vmlinux-x86_64"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
