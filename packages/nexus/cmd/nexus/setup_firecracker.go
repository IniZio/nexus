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
	tmp, err := os.CreateTemp("", "nexus-tap-helper-*")
	if err != nil {
		return "", fmt.Errorf("create temp file for nexus-tap-helper: %w", err)
	}
	dest := tmp.Name()
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp file for nexus-tap-helper: %w", err)
	}

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

// setupExtractAgentFn extracts the nexus-firecracker-agent binary and returns
// its path.  Overridable in tests.
//
// Preference order:
//  1. Extract from embeddedAgent (set at build time via //go:embed).
//  2. Build from Go source if the module root can be located (dev fallback).
var setupExtractAgentFn = func() (string, error) {
	tmp, err := os.CreateTemp("", "nexus-firecracker-agent-*")
	if err != nil {
		return "", fmt.Errorf("create temp file for nexus-firecracker-agent: %w", err)
	}
	dest := tmp.Name()
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp file for nexus-firecracker-agent: %w", err)
	}

	// Fast path: extract the binary that was embedded at build time.
	if len(embeddedAgent) > 0 {
		if err := os.WriteFile(dest, embeddedAgent, 0o755); err != nil {
			return "", fmt.Errorf("extract embedded nexus-firecracker-agent: %w", err)
		}
		return dest, nil
	}

	// Fallback: build from source (works only when running from the module
	// root, e.g. during `go run ./cmd/nexus` in a dev checkout).
	root := moduleRoot()
	localSrc := root + "/cmd/nexus-firecracker-agent"
	if _, err := os.Stat(localSrc); err != nil {
		return "", fmt.Errorf(
			"nexus-firecracker-agent not embedded and source not found at %s\n"+
				"Rebuild nexus with: cd packages/nexus && go generate ./cmd/nexus && go build ./cmd/nexus",
			localSrc,
		)
	}
	cmd := exec.Command("go", "build", "-o", dest, "./cmd/nexus-firecracker-agent/")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build nexus-firecracker-agent: %w", err)
	}
	return dest, nil
}

// setupRunScriptFn runs the privileged setup bash script.  Overridable in
// tests.
var setupRunScriptFn = runSetupScript

// setupVerifyFn verifies that the setup completed correctly.  Overridable in
// tests.
var setupVerifyFn = verifyFirecrackerSetup

// setupSudoReexecFn reruns the current nexus command under sudo so users can
// complete privileged setup steps in one command invocation. Overridable in tests.
var setupSudoReexecFn = func(commandPath string) error {
	args := append([]string{commandPath}, os.Args[1:]...)
	cmd := exec.Command("sudo", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// errKVMGroupRefreshNeeded indicates setup is complete but current session
// still lacks active /dev/kvm group access.
var errKVMGroupRefreshNeeded = errors.New("kvm group refresh needed")

const setupKVMGroupReexecEnv = "NEXUS_SETUP_KVM_GROUP_REEXEC"

// setupKVMGroupReexecFn re-runs the current nexus command under `sg kvm` so
// group membership takes effect without requiring a full logout/login cycle.
var setupKVMGroupReexecFn = func(commandPath string) error {
	parts := make([]string, 0, len(os.Args))
	parts = append(parts, shellQuote(commandPath))
	for _, arg := range os.Args[1:] {
		parts = append(parts, shellQuote(arg))
	}
	cmd := exec.Command("sg", "kvm", "-c", strings.Join(parts, " "))
	cmd.Env = append(os.Environ(), setupKVMGroupReexecEnv+"=1")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

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
ConfigureWithoutCarrier=yes
IgnoreCarrierLoss=yes
`

// tapNetworkContent is the .network unit that attaches nexus-* tap devices.
const tapNetworkContent = `[Match]
Name=nexus-*

[Network]
Bridge=nexusbr0
`

// vmAssetsDir is the directory where VM assets (kernel, rootfs) are stored.
const vmAssetsDir = "/var/lib/nexus"

// vmKernelURL is the S3 URL for the Firecracker-compatible Linux kernel.
const vmKernelURL = "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.13/x86_64/vmlinux-5.10.239"

// vmSquashfsURL is the S3 URL for the Ubuntu 24.04 squashfs rootfs.
const vmSquashfsURL = "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.13/x86_64/ubuntu-24.04.squashfs"

// vmKernelLocalCachePath is the optional local kernel cache used to avoid
// network downloads when the asset was pre-fetched.
const vmKernelLocalCachePath = "/tmp/nexus-vmlinux.bin"

// vmSquashfsLocalCachePath is the optional local squashfs cache used to avoid
// network downloads when the asset was pre-fetched.
const vmSquashfsLocalCachePath = "/tmp/nexus-ubuntu.squashfs"

// DefaultVMKernelPath is the default kernel path used by nexus doctor / run.
const DefaultVMKernelPath = vmAssetsDir + "/vmlinux.bin"

// DefaultVMRootfsPath is the default rootfs path used by nexus doctor / run.
const DefaultVMRootfsPath = vmAssetsDir + "/rootfs.ext4"

// buildSetupScript returns an idempotent bash script that installs
// nexus-tap-helper, configures systemd-networkd for Firecracker networking,
// and provisions the VM kernel + rootfs (with the agent injected as PID1).
//
// tapHelperSrc is the path to the pre-extracted tap-helper binary.
// agentSrc is the path to the pre-extracted nexus-firecracker-agent binary.
func buildSetupScript(tapHelperSrc, agentSrc string) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("copy_bin_with_libs() {\n")
	b.WriteString("  local host_bin=\"$1\"\n")
	b.WriteString("  [ -n \"$host_bin\" ] || return 0\n")
	b.WriteString("  [ -x \"$host_bin\" ] || return 0\n")
	b.WriteString("  local guest_bin=\"$ROOTFS_MOUNT$host_bin\"\n")
	b.WriteString("  mkdir -p \"$(dirname \"$guest_bin\")\"\n")
	b.WriteString("  cp \"$host_bin\" \"$guest_bin\"\n")
	b.WriteString("  chmod 755 \"$guest_bin\"\n")
	b.WriteString("  while IFS= read -r lib_path; do\n")
	b.WriteString("    [ -n \"$lib_path\" ] || continue\n")
	b.WriteString("    [ -f \"$lib_path\" ] || continue\n")
	b.WriteString("    mkdir -p \"$ROOTFS_MOUNT$(dirname \"$lib_path\")\"\n")
	b.WriteString("    cp \"$lib_path\" \"$ROOTFS_MOUNT$lib_path\"\n")
	b.WriteString("  done < <(ldd \"$host_bin\" 2>/dev/null | awk '{for (i=1; i<=NF; i++) if ($i ~ /^\\//) print $i}' | sort -u)\n")
	b.WriteString("}\n\n")

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

	// Apply bridge networking immediately so setup works even when
	// systemd-networkd is unavailable or not managing links yet.
	b.WriteString("ip link add nexusbr0 type bridge 2>/dev/null || true\n")
	b.WriteString("ip addr replace 172.26.0.1/16 dev nexusbr0\n")
	b.WriteString("ip link set nexusbr0 up\n\n")

	// Wait for nexusbr0 to come up (15 retries, 1s each)
	b.WriteString("retries=15\n")
	b.WriteString("while [ $retries -gt 0 ]; do\n")
	b.WriteString("  if ! ip route show dev nexusbr0 | grep -q 'linkdown'; then\n")
	b.WriteString("    break\n")
	b.WriteString("  fi\n")
	b.WriteString("  retries=$((retries - 1))\n")
	b.WriteString("  sleep 1\n")
	b.WriteString("done\n\n")
	b.WriteString("if ip route show dev nexusbr0 | grep -q 'linkdown'; then\n")
	b.WriteString("  echo 'WARN: nexusbr0 route still linkdown after setup; check TAP attach path'\n")
	b.WriteString("fi\n\n")

	// Enable IP forwarding
	b.WriteString("sysctl -w net.ipv4.ip_forward=1\n")
	b.WriteString("printf 'net.ipv4.ip_forward = 1\\n' > /etc/sysctl.d/99-nexus-ip-forward.conf\n\n")

	// Ensure guest egress NAT and forwarding rules exist.
	b.WriteString("if command -v iptables >/dev/null 2>&1; then\n")
	b.WriteString("  iptables -t nat -C POSTROUTING -s 172.26.0.0/16 ! -d 172.26.0.0/16 -j MASQUERADE >/dev/null 2>&1 || \\\n")
	b.WriteString("    iptables -t nat -A POSTROUTING -s 172.26.0.0/16 ! -d 172.26.0.0/16 -j MASQUERADE\n")
	b.WriteString("  iptables -C FORWARD -i nexusbr0 -j ACCEPT >/dev/null 2>&1 || iptables -A FORWARD -i nexusbr0 -j ACCEPT\n")
	b.WriteString("  iptables -C FORWARD -o nexusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT >/dev/null 2>&1 || \\\n")
	b.WriteString("    iptables -A FORWARD -o nexusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT\n")
	b.WriteString("fi\n\n")

	// Tailscale and other policy-routing daemons can install catch-all rules
	// that route 172.26.0.0/16 traffic away from nexusbr0. Add explicit
	// high-priority policy rules so guest/bridge traffic always uses main.
	b.WriteString("ip rule show | grep -q '^5190:.* to 172.26.0.0/16 .* lookup main' || ip rule add pref 5190 to 172.26.0.0/16 lookup main\n")
	b.WriteString("ip rule show | grep -q '^5191:.* from 172.26.0.0/16 .* lookup main' || ip rule add pref 5191 from 172.26.0.0/16 lookup main\n\n")

	// Ensure the invoking user has kvm group membership for /dev/kvm access.
	b.WriteString("if getent group kvm >/dev/null 2>&1; then\n")
	b.WriteString("  if [ -n \"${SUDO_USER:-}\" ]; then\n")
	b.WriteString("    if ! id -nG \"$SUDO_USER\" | tr ' ' '\\n' | grep -qx kvm; then\n")
	b.WriteString("      usermod -aG kvm \"$SUDO_USER\"\n")
	b.WriteString("      echo \"==> Added $SUDO_USER to kvm group\"\n")
	b.WriteString("    fi\n")
	b.WriteString("  fi\n")
	b.WriteString("fi\n\n")

	// ------------------------------------------------------------------
	// VM assets: kernel and rootfs
	// ------------------------------------------------------------------
	fmt.Fprintf(&b, "mkdir -p %s\n\n", vmAssetsDir)

	// Kernel: idempotent download
	fmt.Fprintf(&b, "if [ ! -f %s ]; then\n", DefaultVMKernelPath)
	fmt.Fprintf(&b, "  if [ -f %s ]; then\n", vmKernelLocalCachePath)
	b.WriteString("    echo '==> Using local Firecracker kernel cache...'\n")
	fmt.Fprintf(&b, "    cp %s %s\n", vmKernelLocalCachePath, DefaultVMKernelPath)
	b.WriteString("  else\n")
	b.WriteString("    echo '==> Downloading Firecracker kernel...'\n")
	fmt.Fprintf(&b, "    wget -q -O %s %s\n", DefaultVMKernelPath, vmKernelURL)
	b.WriteString("  fi\n")
	b.WriteString("fi\n\n")

	// Rootfs: always ensure current agent is present, rebuilding if needed.
	b.WriteString("ROOTFS_REBUILD=0\n")
	fmt.Fprintf(&b, "if [ ! -f %s ]; then\n", DefaultVMRootfsPath)
	b.WriteString("  ROOTFS_REBUILD=1\n")
	b.WriteString("fi\n\n")

	b.WriteString("if [ \"$ROOTFS_REBUILD\" -eq 1 ]; then\n")
	b.WriteString("  echo '==> Building Firecracker rootfs...'\n")
	b.WriteString("  SQUASHFS_TMP=$(mktemp -d)\n")
	b.WriteString("  ROOTFS_MOUNT=$(mktemp -d)\n")
	b.WriteString("  trap 'umount \"$ROOTFS_MOUNT\" 2>/dev/null || true; rm -rf \"$SQUASHFS_TMP\" \"$ROOTFS_MOUNT\"' EXIT\n\n")

	fmt.Fprintf(&b, "  if [ -f %s ]; then\n", vmSquashfsLocalCachePath)
	b.WriteString("    echo '  -> Using local squashfs rootfs cache...'\n")
	fmt.Fprintf(&b, "    cp %s \"$SQUASHFS_TMP/rootfs.squashfs\"\n", vmSquashfsLocalCachePath)
	b.WriteString("  else\n")
	b.WriteString("    echo '  -> Downloading squashfs rootfs...'\n")
	b.WriteString("    wget -q -O \"$SQUASHFS_TMP/rootfs.squashfs\" \\\n")
	fmt.Fprintf(&b, "      %s\n", vmSquashfsURL)
	b.WriteString("  fi\n\n")

	b.WriteString("  echo '  -> Extracting squashfs...'\n")
	b.WriteString("  unsquashfs -d \"$SQUASHFS_TMP/rootfs\" \"$SQUASHFS_TMP/rootfs.squashfs\"\n\n")

	fmt.Fprintf(&b, "  echo '  -> Creating ext4 image at %s...'\n", DefaultVMRootfsPath)
	fmt.Fprintf(&b, "  dd if=/dev/zero of=%s bs=1M count=4096 status=none\n", DefaultVMRootfsPath)
	fmt.Fprintf(&b, "  mkfs.ext4 -F -q %s\n\n", DefaultVMRootfsPath)

	fmt.Fprintf(&b, "  mount -o loop %s \"$ROOTFS_MOUNT\"\n\n", DefaultVMRootfsPath)

	b.WriteString("  echo '  -> Copying rootfs tree...'\n")
	b.WriteString("  rsync -a \"$SQUASHFS_TMP/rootfs/\" \"$ROOTFS_MOUNT/\"\n\n")
	b.WriteString("  echo '  -> Seeding container runtime/toolchain binaries into rootfs...'\n")
	b.WriteString("  for candidate in docker dockerd containerd containerd-shim-runc-v2 ctr runc docker-init docker-proxy iptables ip6tables make; do\n")
	b.WriteString("    host_bin=$(command -v \"$candidate\" || true)\n")
	b.WriteString("    copy_bin_with_libs \"$host_bin\"\n")
	b.WriteString("  done\n")
	b.WriteString("  docker_compose_plugin=''\n")
	b.WriteString("  for plugin_path in /usr/libexec/docker/cli-plugins/docker-compose /usr/lib/docker/cli-plugins/docker-compose /usr/local/lib/docker/cli-plugins/docker-compose; do\n")
	b.WriteString("    if [ -x \"$plugin_path\" ]; then docker_compose_plugin=\"$plugin_path\"; break; fi\n")
	b.WriteString("  done\n")
	b.WriteString("  if [ -n \"$docker_compose_plugin\" ]; then\n")
	b.WriteString("    mkdir -p \"$ROOTFS_MOUNT/usr/libexec/docker/cli-plugins\"\n")
	b.WriteString("    cp \"$docker_compose_plugin\" \"$ROOTFS_MOUNT/usr/libexec/docker/cli-plugins/docker-compose\"\n")
	b.WriteString("    chmod 755 \"$ROOTFS_MOUNT/usr/libexec/docker/cli-plugins/docker-compose\"\n")
	b.WriteString("  fi\n\n")

	b.WriteString("  echo '  -> Injecting nexus-firecracker-agent as PID1...'\n")
	b.WriteString("  mkdir -p \"$ROOTFS_MOUNT/usr/local/bin\"\n")
	b.WriteString("  mkdir -p \"$ROOTFS_MOUNT/workspace\"\n")
	b.WriteString("  for candidate in docker dockerd containerd containerd-shim-runc-v2 ctr runc docker-init docker-proxy iptables ip6tables make; do\n")
	b.WriteString("    host_bin=$(command -v \"$candidate\" || true)\n")
	b.WriteString("    copy_bin_with_libs \"$host_bin\"\n")
	b.WriteString("  done\n")
	b.WriteString("  docker_compose_plugin=''\n")
	b.WriteString("  for plugin_path in /usr/libexec/docker/cli-plugins/docker-compose /usr/lib/docker/cli-plugins/docker-compose /usr/local/lib/docker/cli-plugins/docker-compose; do\n")
	b.WriteString("    if [ -x \"$plugin_path\" ]; then docker_compose_plugin=\"$plugin_path\"; break; fi\n")
	b.WriteString("  done\n")
	b.WriteString("  if [ -n \"$docker_compose_plugin\" ]; then\n")
	b.WriteString("    mkdir -p \"$ROOTFS_MOUNT/usr/libexec/docker/cli-plugins\"\n")
	b.WriteString("    cp \"$docker_compose_plugin\" \"$ROOTFS_MOUNT/usr/libexec/docker/cli-plugins/docker-compose\"\n")
	b.WriteString("    chmod 755 \"$ROOTFS_MOUNT/usr/libexec/docker/cli-plugins/docker-compose\"\n")
	b.WriteString("  fi\n")
	fmt.Fprintf(&b, "  cp %s \"$ROOTFS_MOUNT/usr/local/bin/nexus-firecracker-agent\"\n", agentSrc)
	b.WriteString("  chmod 755 \"$ROOTFS_MOUNT/usr/local/bin/nexus-firecracker-agent\"\n")
	b.WriteString("  printf '#!/bin/sh\\nexec /usr/local/bin/nexus-firecracker-agent\\n' > \"$ROOTFS_MOUNT/sbin/init\"\n")
	b.WriteString("  chmod 755 \"$ROOTFS_MOUNT/sbin/init\"\n")
	b.WriteString("  ln -sf /sbin/init \"$ROOTFS_MOUNT/init\" 2>/dev/null || cp \"$ROOTFS_MOUNT/sbin/init\" \"$ROOTFS_MOUNT/init\"\n\n")

	b.WriteString("  umount \"$ROOTFS_MOUNT\"\n")
	b.WriteString("  trap - EXIT\n")
	b.WriteString("  rm -rf \"$SQUASHFS_TMP\" \"$ROOTFS_MOUNT\"\n")
	b.WriteString("  echo '  -> rootfs built successfully.'\n")
	b.WriteString("fi\n")
	b.WriteString("\n")

	b.WriteString("if [ \"$ROOTFS_REBUILD\" -eq 0 ]; then\n")
	b.WriteString("  echo '==> Updating Firecracker rootfs agent payload...'\n")
	b.WriteString("  ROOTFS_MOUNT=$(mktemp -d)\n")
	b.WriteString("  trap 'umount \"$ROOTFS_MOUNT\" 2>/dev/null || true; rm -rf \"$ROOTFS_MOUNT\"' EXIT\n")
	fmt.Fprintf(&b, "  mount -o loop %s \"$ROOTFS_MOUNT\"\n", DefaultVMRootfsPath)
	b.WriteString("  mkdir -p \"$ROOTFS_MOUNT/usr/local/bin\"\n")
	b.WriteString("  mkdir -p \"$ROOTFS_MOUNT/workspace\"\n")
	fmt.Fprintf(&b, "  cp %s \"$ROOTFS_MOUNT/usr/local/bin/nexus-firecracker-agent\"\n", agentSrc)
	b.WriteString("  chmod 755 \"$ROOTFS_MOUNT/usr/local/bin/nexus-firecracker-agent\"\n")
	b.WriteString("  printf '#!/bin/sh\\nexec /usr/local/bin/nexus-firecracker-agent\\n' > \"$ROOTFS_MOUNT/sbin/init\"\n")
	b.WriteString("  chmod 755 \"$ROOTFS_MOUNT/sbin/init\"\n")
	b.WriteString("  ln -sf /sbin/init \"$ROOTFS_MOUNT/init\" 2>/dev/null || cp \"$ROOTFS_MOUNT/sbin/init\" \"$ROOTFS_MOUNT/init\"\n")
	b.WriteString("  umount \"$ROOTFS_MOUNT\"\n")
	b.WriteString("  trap - EXIT\n")
	b.WriteString("  rm -rf \"$ROOTFS_MOUNT\"\n")
	b.WriteString("fi\n")

	// Normalize ownership/permissions so non-root Firecracker runs can access
	// VM assets after a sudo setup invocation.
	b.WriteString("if [ -n \"${SUDO_USER:-}\" ]; then\n")
	fmt.Fprintf(&b, "  if [ -f %s ]; then\n", DefaultVMKernelPath)
	fmt.Fprintf(&b, "    chown \"$SUDO_USER\":\"$SUDO_USER\" %s\n", DefaultVMKernelPath)
	fmt.Fprintf(&b, "    chmod 644 %s\n", DefaultVMKernelPath)
	b.WriteString("  fi\n")
	fmt.Fprintf(&b, "  if [ -f %s ]; then\n", DefaultVMRootfsPath)
	fmt.Fprintf(&b, "    chown \"$SUDO_USER\":\"$SUDO_USER\" %s\n", DefaultVMRootfsPath)
	fmt.Fprintf(&b, "    chmod 600 %s\n", DefaultVMRootfsPath)
	b.WriteString("  fi\n")
	b.WriteString("fi\n")

	return b.String()
}

// setupCommandPath returns the command path users should run with sudo.
func setupCommandPath() string {
	if exe, err := os.Executable(); err == nil {
		exe = strings.TrimSpace(exe)
		if exe != "" {
			return exe
		}
	}
	if len(os.Args) > 0 {
		arg0 := strings.TrimSpace(os.Args[0])
		if arg0 != "" {
			if filepath.IsAbs(arg0) {
				return arg0
			}
			if lp, err := exec.LookPath(arg0); err == nil {
				return lp
			}
			return arg0
		}
	}
	return "nexus"
}

// runSetupScript executes the given bash script content under the appropriate
// privilege mode.  For privilegeModeManual it returns errNeedsManual without
// running anything.
func runSetupScript(mode privilegeMode, script string) error {
	switch mode {
	case privilegeModeRoot:
		cmd := exec.Command("bash", "-s")
		cmd.Stdin = strings.NewReader(script)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case privilegeModeSudoN:
		cmd := exec.Command("sudo", "-n", "bash", "-s")
		cmd.Stdin = strings.NewReader(script)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case privilegeModeInteractive:
		cmd := exec.Command("sudo", "bash", "-s")
		cmd.Stdin = strings.NewReader(script)
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
	forceRefresh := strings.TrimSpace(os.Getenv("NEXUS_SETUP_FIRECRACKER_FORCE")) == "1"

	fmt.Fprintln(w, "==> Verifying setup...")
	if err := setupVerifyFn(); err == nil {
		if !forceRefresh {
			fmt.Fprintln(w, "==> Firecracker host setup already configured; skipping setup steps.")
			fmt.Fprintln(w, "==> Firecracker host setup complete.")
			return nil
		}
		fmt.Fprintln(w, "==> Setup already configured; force-refreshing Firecracker VM assets.")
	} else if errors.Is(err, errKVMGroupRefreshNeeded) && os.Getenv(setupKVMGroupReexecEnv) != "1" {
		cmdPath := setupCommandPath()
		fmt.Fprintln(w, "==> Setup already configured; refreshing kvm group in current session...")
		if rgErr := setupKVMGroupReexecFn(cmdPath); rgErr == nil {
			return nil
		}
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "To refresh /dev/kvm access without logging out, run:")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "  newgrp kvm")
		fmt.Fprintln(w, "  rerun your previous nexus command")
		fmt.Fprintln(w, "")
		return fmt.Errorf("setup is configured but /dev/kvm group refresh is required: %w", err)
	}

	mode := resolvePrivilegeMode()
	if mode == privilegeModeManual {
		cmdPath := setupCommandPath()
		fmt.Fprintln(w, "==> Requesting sudo to complete setup...")
		if err := setupSudoReexecFn(cmdPath); err == nil {
			fmt.Fprintln(w, "==> Verifying setup...")
			if err := setupVerifyFn(); err != nil {
				if errors.Is(err, errKVMGroupRefreshNeeded) && os.Getenv(setupKVMGroupReexecEnv) != "1" {
					fmt.Fprintln(w, "==> Refreshing kvm group in current session...")
					if rgErr := setupKVMGroupReexecFn(cmdPath); rgErr == nil {
						return nil
					}
					fmt.Fprintln(w, "")
					fmt.Fprintln(w, "To refresh /dev/kvm access without logging out, run:")
					fmt.Fprintln(w, "")
					fmt.Fprintln(w, "  newgrp kvm")
					fmt.Fprintln(w, "  rerun your previous nexus command")
					fmt.Fprintln(w, "")
				}
				return fmt.Errorf("setup verification failed after sudo setup: %w", err)
			}
			fmt.Fprintln(w, "==> Firecracker host setup complete.")
			return nil
		}

		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Run the following command to prepare firecracker prerequisites:")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "  cd <repo-path>")
		fmt.Fprintln(w, "  sudo -E nexus init --force")
		fmt.Fprintln(w, "")
		return fmt.Errorf("manual privileged step required — run the sudo nexus init command above")
	}

	// ---------- step 1: extract nexus-tap-helper (no privilege needed) ----------
	fmt.Fprintln(w, "==> Extracting nexus-tap-helper...")
	tapHelperPath, err := setupBuildTapHelperFn()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "    extracted: %s\n", tapHelperPath)

	// ---------- step 2: extract nexus-firecracker-agent (no privilege needed) ----------
	fmt.Fprintln(w, "==> Extracting nexus-firecracker-agent...")
	agentPath, err := setupExtractAgentFn()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "    extracted: %s\n", agentPath)

	// ---------- step 3: generate idempotent setup script ----------
	script := buildSetupScript(tapHelperPath, agentPath)

	// ---------- step 4: run (or print) the script ----------
	fmt.Fprintln(w, "==> Running Firecracker host setup script...")
	if err := setupRunScriptFn(mode, script); err != nil {
		if errors.Is(err, errNeedsManual) {
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Run the following command to prepare firecracker prerequisites:")
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "  cd <repo-path>")
			fmt.Fprintln(w, "  sudo -E nexus init --force")
			fmt.Fprintln(w, "")
			return fmt.Errorf("manual privileged step required — run the sudo nexus init command above")
		}
		return fmt.Errorf("setup script failed: %w", err)
	}

	// ---------- step 5: verify ----------
	fmt.Fprintln(w, "==> Verifying setup...")
	if err := setupVerifyFn(); err != nil {
		if errors.Is(err, errKVMGroupRefreshNeeded) && os.Getenv(setupKVMGroupReexecEnv) != "1" {
			cmdPath := setupCommandPath()
			fmt.Fprintln(w, "==> Refreshing kvm group in current session...")
			if rgErr := setupKVMGroupReexecFn(cmdPath); rgErr == nil {
				return nil
			}
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "To refresh /dev/kvm access without logging out, run:")
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "  newgrp kvm")
			fmt.Fprintln(w, "  rerun your previous nexus command")
			fmt.Fprintln(w, "")
		}
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
	routeOut, err := exec.Command("ip", "route", "show", "dev", "nexusbr0").CombinedOutput()
	if err != nil {
		return fmt.Errorf("unable to inspect nexusbr0 route: %w", err)
	}
	if strings.Contains(string(routeOut), "linkdown") {
		// linkdown is expected before any TAP device is attached; setup should
		// still be treated as successful if bridge and assets are in place.
	}

	// Verify VM assets
	if _, err := os.Stat(DefaultVMKernelPath); err != nil {
		return fmt.Errorf("VM kernel not found at %s: %w", DefaultVMKernelPath, err)
	}
	kernelFD, err := os.Open(DefaultVMKernelPath)
	if err != nil {
		return fmt.Errorf("VM kernel not readable at %s: %w", DefaultVMKernelPath, err)
	}
	_ = kernelFD.Close()
	if _, err := os.Stat(DefaultVMRootfsPath); err != nil {
		return fmt.Errorf("VM rootfs not found at %s: %w", DefaultVMRootfsPath, err)
	}
	rootfsFD, err := os.OpenFile(DefaultVMRootfsPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("VM rootfs not read/write accessible at %s: %w", DefaultVMRootfsPath, err)
	}
	_ = rootfsFD.Close()

	fd, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w: current session lacks read/write access to /dev/kvm", errKVMGroupRefreshNeeded)
		}
		return fmt.Errorf("unable to open /dev/kvm: %w", err)
	}
	_ = fd.Close()
	return nil
}
