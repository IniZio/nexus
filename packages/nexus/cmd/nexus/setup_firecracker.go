//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// privilegeMode describes how privileged steps will be executed.
type privilegeMode int

const (
	// privilegeModeRoot: EUID == 0, run commands directly.
	privilegeModeRoot privilegeMode = iota
	// privilegeModeSudoN: passwordless sudo available (CI); use sudo -n.
	privilegeModeSudoN
	// privilegeModeInteractive: stdin is a TTY; run sudo interactively.
	privilegeModeInteractive
	// privilegeModeManual: no privilege path — print commands for the user.
	privilegeModeManual
)

// setupPrivilegeModeOverride, when setupPrivilegeModeOverrideEnabled is true,
// overrides the auto-detected privilege mode.  Tests flip the enabled flag.
var setupPrivilegeModeOverride privilegeMode
var setupPrivilegeModeOverrideEnabled bool

// setupBuildTapHelperFn builds or extracts the nexus-tap-helper binary and
// returns its path.  Overridable in tests.
//
// Preference order:
//  1. Extract from embeddedTapHelper (set at build time via //go:embed).
//  2. Build from Go source if the module root can be located (dev fallback).
var setupBuildTapHelperFn = func() (string, error) {
	dest := "/tmp/nexus-tap-helper"

	// Fast path: extract the binary that was embedded at build time.
	if len(embeddedTapHelper) > 0 {
		if err := os.WriteFile(dest, embeddedTapHelper, 0o755); err != nil {
			return "", fmt.Errorf("extract embedded nexus-tap-helper: %w", err)
		}
		return dest, nil
	}

	// Fallback: build from source (works only when running from the module
	// root, e.g. during `go run ./cmd/nexus` in a dev checkout).
	root := moduleRoot()
	localSrc := filepath.Join(root, "cmd", "nexus-tap-helper")
	if _, err := os.Stat(localSrc); err != nil {
		return "", fmt.Errorf(
			"nexus-tap-helper not embedded and source not found at %s\n"+
				"Rebuild nexus with: cd packages/nexus && go generate ./cmd/nexus && go build ./cmd/nexus",
			localSrc,
		)
	}
	cmd := exec.Command("go", "build", "-o", dest, "./cmd/nexus-tap-helper/")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build nexus-tap-helper: %w", err)
	}
	return dest, nil
}

// setupRunPrivilegedFn runs a single privileged command.  Overridable in
// tests.
var setupRunPrivilegedFn = runPrivilegedStep

// setupVerifyFn verifies that the setup completed correctly.  Overridable in
// tests.
var setupVerifyFn = verifyFirecrackerSetup

// detectPrivilegeMode returns the appropriate privilege escalation strategy
// based on the three boolean inputs.
//
//   - isRoot:      os.Geteuid() == 0
//   - sudoNOK:     `sudo -n true` exits 0
//   - stdinIsTTY:  os.Stdin is a TTY
func detectPrivilegeMode(isRoot, sudoNOK, stdinIsTTY bool) privilegeMode {
	if isRoot {
		return privilegeModeRoot
	}
	if sudoNOK {
		return privilegeModeSudoN
	}
	if stdinIsTTY {
		return privilegeModeInteractive
	}
	return privilegeModeManual
}

// resolvePrivilegeMode probes the current runtime to pick the best strategy.
func resolvePrivilegeMode() privilegeMode {
	if setupPrivilegeModeOverrideEnabled {
		return setupPrivilegeModeOverride
	}
	isRoot := os.Geteuid() == 0
	sudoNOK := exec.Command("sudo", "-n", "true").Run() == nil
	stdinIsTTY := isTerminal(os.Stdin)
	return detectPrivilegeMode(isRoot, sudoNOK, stdinIsTTY)
}

// isTerminal returns true when f refers to a terminal device.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// runPrivilegedStep executes args[0] with args[1:] under the privilege
// strategy indicated by mode.  For privilegeModeManual it writes the command
// to w and returns ErrNeedsManual.
func runPrivilegedStep(w interface{ Write([]byte) (int, error) }, mode privilegeMode, args ...string) error {
	switch mode {
	case privilegeModeRoot:
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case privilegeModeSudoN:
		sudoArgs := append([]string{"-n"}, args...)
		cmd := exec.Command("sudo", sudoArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case privilegeModeInteractive:
		sudoArgs := append([]string{}, args...)
		cmd := exec.Command("sudo", sudoArgs...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case privilegeModeManual:
		line := "  " + strings.Join(args, " ") + "\n"
		_, _ = w.Write([]byte(line))
		return errNeedsManual

	default:
		return fmt.Errorf("unknown privilege mode: %d", mode)
	}
}

// errNeedsManual is returned when a privileged step requires manual
// intervention.
var errNeedsManual = errors.New("manual privileged command required")

// moduleRoot returns the Go module root directory of the nexus package.
// It resolves relative to the binary or falls back to the working directory.
func moduleRoot() string {
	// During tests / development the module root is the packages/nexus directory.
	// We locate it by walking up until we find a go.mod.
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

// systemdNetworkdDir is the path where systemd-networkd unit files are written.
const systemdNetworkdDir = "/etc/systemd/network"

// netdevContent is the .netdev unit that creates the nexusbr0 bridge.
const netdevContent = `[NetDev]
Name=nexusbr0
Kind=bridge
`

// bridgeNetworkContent is the .network unit that configures the bridge.
const bridgeNetworkContent = `[Match]
Name=nexusbr0

[Network]
Address=172.26.0.1/16
IPForward=yes
IPMasquerade=ipv4
`

// tapNetworkContent is the .network unit that attaches nexus-* tap devices.
const tapNetworkContent = `[Match]
Name=nexus-*

[Network]
Bridge=nexusbr0
`

// runSetupFirecracker executes the one-time Firecracker host setup.
//
// It writes progress/manual-command output to w.  It returns a non-nil error
// if any step fails, or if manual steps are needed (non-interactive without
// passwordless sudo).
func runSetupFirecracker(w io.Writer) error {
	mode := resolvePrivilegeMode()

	// Collect all manual-mode errors rather than stopping at first.
	var manualCmds []string
	needsManual := false

	type stepFn func() error

	// ---------- step 1: build nexus-tap-helper (no privilege needed) ----------
	fmt.Fprintln(w, "==> Building nexus-tap-helper...")
	builtPath, err := setupBuildTapHelperFn()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "    built: %s\n", builtPath)

	// ---------- step 2: install + setcap (privileged) ----------
	installAndSetcap := func() error {
		installArgs := []string{"cp", builtPath, "/usr/local/bin/nexus-tap-helper"}
		if err := setupRunPrivilegedFn(w, mode, installArgs...); err != nil {
			if errors.Is(err, errNeedsManual) {
				manualCmds = append(manualCmds, strings.Join(installArgs, " "))
				needsManual = true
			} else {
				return fmt.Errorf("install nexus-tap-helper: %w", err)
			}
		}
		setcapArgs := []string{"setcap", "cap_net_admin=ep", "/usr/local/bin/nexus-tap-helper"}
		if err := setupRunPrivilegedFn(w, mode, setcapArgs...); err != nil {
			if errors.Is(err, errNeedsManual) {
				manualCmds = append(manualCmds, strings.Join(setcapArgs, " "))
				needsManual = true
			} else {
				return fmt.Errorf("setcap nexus-tap-helper: %w", err)
			}
		}
		return nil
	}

	fmt.Fprintln(w, "==> Installing nexus-tap-helper and setting cap_net_admin...")
	if err := installAndSetcap(); err != nil {
		return err
	}

	// ---------- step 3: write systemd-networkd config files (privileged) ----------
	type networkFile struct {
		name    string
		content string
	}
	networkFiles := []networkFile{
		{"10-nexusbr0.netdev", netdevContent},
		{"11-nexusbr0.network", bridgeNetworkContent},
		{"12-nexus-tap.network", tapNetworkContent},
	}

	fmt.Fprintln(w, "==> Writing systemd-networkd configuration...")
	for _, nf := range networkFiles {
		dest := systemdNetworkdDir + "/" + nf.name
		writeArgs := []string{"tee", dest}
		// In non-manual mode we use a helper that pipes content; in manual mode
		// we emit a heredoc-style hint.
		if mode == privilegeModeManual {
			hint := fmt.Sprintf("tee %s << 'EOF'\n%sEOF", dest, nf.content)
			manualCmds = append(manualCmds, hint)
			needsManual = true
			_ = writeArgs // suppress unused warning
		} else {
			if err := setupWriteFileFn(mode, dest, []byte(nf.content)); err != nil {
				return fmt.Errorf("write %s: %w", dest, err)
			}
		}
	}

	// ---------- step 4: enable + start systemd-networkd (privileged) ----------
	fmt.Fprintln(w, "==> Enabling systemd-networkd...")
	enableArgs := []string{"systemctl", "enable", "--now", "systemd-networkd"}
	if err := setupRunPrivilegedFn(w, mode, enableArgs...); err != nil {
		if errors.Is(err, errNeedsManual) {
			manualCmds = append(manualCmds, strings.Join(enableArgs, " "))
			needsManual = true
		} else {
			return fmt.Errorf("enable systemd-networkd: %w", err)
		}
	}

	// If we collected manual commands, print them as a block and return error.
	if needsManual {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "The following commands require elevated privileges.")
		fmt.Fprintln(w, "Run them manually, then re-run `nexus setup firecracker` to verify:")
		fmt.Fprintln(w, "")
		for _, cmd := range manualCmds {
			fmt.Fprintf(w, "  sudo %s\n", cmd)
		}
		fmt.Fprintln(w, "")
		return fmt.Errorf("manual privileged steps required — see commands above")
	}

	// ---------- step 5: verify ----------
	fmt.Fprintln(w, "==> Verifying setup...")
	if err := setupVerifyFn(); err != nil {
		return fmt.Errorf("setup verification failed: %w", err)
	}

	fmt.Fprintln(w, "==> Firecracker host setup complete.")
	return nil
}

// setupWriteFileFn writes a file with the given privilege mode.  Overridable
// in tests.
var setupWriteFileFn = writeFilePrivileged

// writeFilePrivileged writes content to dest using a privileged `tee` invocation.
func writeFilePrivileged(mode privilegeMode, dest string, content []byte) error {
	var sudoArgs []string
	switch mode {
	case privilegeModeRoot:
		return os.WriteFile(dest, content, 0o644)
	case privilegeModeSudoN:
		sudoArgs = []string{"sudo", "-n", "tee", dest}
	case privilegeModeInteractive:
		sudoArgs = []string{"sudo", "tee", dest}
	default:
		return errNeedsManual
	}
	cmd := exec.Command(sudoArgs[0], sudoArgs[1:]...)
	cmd.Stdin = strings.NewReader(string(content))
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// verifyFirecrackerSetup checks that the setup succeeded.
func verifyFirecrackerSetup() error {
	path, err := exec.LookPath("nexus-tap-helper")
	if err != nil {
		return fmt.Errorf("nexus-tap-helper not found: %w", err)
	}
	out, err := exec.Command("getcap", path).Output()
	if err != nil {
		return fmt.Errorf("getcap failed: %w", err)
	}
	if !strings.Contains(string(out), "cap_net_admin") {
		return fmt.Errorf("nexus-tap-helper at %s lacks cap_net_admin", path)
	}
	ipOut, err := exec.Command("ip", "link", "show", "nexusbr0").CombinedOutput()
	if err != nil {
		return fmt.Errorf("bridge nexusbr0 not found: %w", err)
	}
	if !strings.Contains(string(ipOut), "UP") {
		return fmt.Errorf("bridge nexusbr0 exists but is not UP")
	}
	return nil
}
