//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	localSrc := root + "/cmd/nexus-tap-helper"
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

// setupRunScriptFn runs the privileged setup bash script.  Overridable in
// tests.
var setupRunScriptFn = runSetupScript

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

// errNeedsManual is returned when a privileged step requires manual
// intervention.
var errNeedsManual = errors.New("manual privileged command required")

// moduleRoot returns the Go module root directory of the nexus package.
// It resolves relative to the binary or falls back to the working directory.
func moduleRoot() string {
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

// buildSetupScript returns an idempotent bash script that installs
// nexus-tap-helper and configures systemd-networkd for Firecracker networking.
// tapHelperSrc is the path to the pre-extracted binary (e.g. /tmp/nexus-tap-helper).
func buildSetupScript(tapHelperSrc string) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n\n")

	// Install tap-helper
	fmt.Fprintf(&b, "cp %s /usr/local/bin/nexus-tap-helper\n", tapHelperSrc)
	b.WriteString("chmod 755 /usr/local/bin/nexus-tap-helper\n")
	b.WriteString("setcap cap_net_admin=ep /usr/local/bin/nexus-tap-helper\n\n")

	// Create network directory
	fmt.Fprintf(&b, "mkdir -p %s\n\n", systemdNetworkdDir)

	// Write netdev file
	fmt.Fprintf(&b, "cat > %s/10-nexusbr0.netdev << 'NEXUS_EOF'\n%sNEXUS_EOF\n\n",
		systemdNetworkdDir, netdevContent)

	// Write bridge network file
	fmt.Fprintf(&b, "cat > %s/11-nexusbr0.network << 'NEXUS_EOF'\n%sNEXUS_EOF\n\n",
		systemdNetworkdDir, bridgeNetworkContent)

	// Write tap network file
	fmt.Fprintf(&b, "cat > %s/12-nexus-tap.network << 'NEXUS_EOF'\n%sNEXUS_EOF\n\n",
		systemdNetworkdDir, tapNetworkContent)

	// Enable and restart systemd-networkd
	b.WriteString("systemctl enable systemd-networkd\n")
	b.WriteString("systemctl restart systemd-networkd\n\n")

	// Wait for nexusbr0 to come up (15 retries, 1s each)
	b.WriteString("retries=15\n")
	b.WriteString("while [ $retries -gt 0 ]; do\n")
	b.WriteString("  if ip link show nexusbr0 | grep -q 'state UP'; then\n")
	b.WriteString("    break\n")
	b.WriteString("  fi\n")
	b.WriteString("  retries=$((retries - 1))\n")
	b.WriteString("  sleep 1\n")
	b.WriteString("done\n\n")

	// Enable IP forwarding
	b.WriteString("sysctl -w net.ipv4.ip_forward=1\n")
	b.WriteString("printf 'net.ipv4.ip_forward = 1\\n' > /etc/sysctl.d/99-nexus-ip-forward.conf\n")

	return b.String()
}

// runSetupScript executes the given bash script file under the appropriate
// privilege mode.  For privilegeModeManual it returns errNeedsManual without
// running anything.
func runSetupScript(w interface{ Write([]byte) (int, error) }, mode privilegeMode, scriptPath string) error {
	switch mode {
	case privilegeModeRoot:
		cmd := exec.Command("bash", scriptPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case privilegeModeSudoN:
		cmd := exec.Command("sudo", "-n", "bash", scriptPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case privilegeModeInteractive:
		cmd := exec.Command("sudo", "bash", scriptPath)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case privilegeModeManual:
		return errNeedsManual

	default:
		return fmt.Errorf("unknown privilege mode: %d", mode)
	}
}

// runSetupFirecracker executes the one-time Firecracker host setup.
//
// It writes progress/manual-command output to w.  It returns a non-nil error
// if any step fails, or if manual steps are needed (non-interactive without
// passwordless sudo).
func runSetupFirecracker(w io.Writer) error {
	mode := resolvePrivilegeMode()

	// ---------- step 1: extract nexus-tap-helper (no privilege needed) ----------
	fmt.Fprintln(w, "==> Extracting nexus-tap-helper...")
	tapHelperPath, err := setupBuildTapHelperFn()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "    extracted: %s\n", tapHelperPath)

	// ---------- step 2: generate idempotent setup script ----------
	script := buildSetupScript(tapHelperPath)

	f, err := os.CreateTemp("", "nexus-setup-*.sh")
	if err != nil {
		return fmt.Errorf("create setup script: %w", err)
	}
	scriptPath := f.Name()

	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		_ = os.Remove(scriptPath)
		return fmt.Errorf("write setup script: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return fmt.Errorf("close setup script: %w", err)
	}
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		_ = os.Remove(scriptPath)
		return fmt.Errorf("chmod setup script: %w", err)
	}

	// ---------- step 3: run (or print) the script ----------
	fmt.Fprintln(w, "==> Running Firecracker host setup script...")
	if err := setupRunScriptFn(w, mode, scriptPath); err != nil {
		if errors.Is(err, errNeedsManual) {
			// Leave the script in place so the user can run it.
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Run the following command to complete setup, then re-run `nexus setup firecracker` to verify:")
			fmt.Fprintln(w, "")
			fmt.Fprintf(w, "  sudo bash %s\n", scriptPath)
			fmt.Fprintln(w, "")
			return fmt.Errorf("manual privileged step required — see command above")
		}
		_ = os.Remove(scriptPath)
		return fmt.Errorf("setup script failed: %w", err)
	}

	// Clean up script on success.
	_ = os.Remove(scriptPath)

	// ---------- step 4: verify ----------
	fmt.Fprintln(w, "==> Verifying setup...")
	if err := setupVerifyFn(); err != nil {
		return fmt.Errorf("setup verification failed: %w", err)
	}

	fmt.Fprintln(w, "==> Firecracker host setup complete.")
	return nil
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
