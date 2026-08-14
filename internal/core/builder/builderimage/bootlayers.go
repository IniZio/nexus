//go:build linux

package builderimage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// nexus3AgentInstallPath is the in-rootfs path for the nexus3-agent binary.
// The agent runs as PID 1 inside the builder VM via the builder-init shim.
const nexus3AgentInstallPath = "/usr/local/bin/nexus3-agent"

// builderInitScriptInstallPath is the in-rootfs path for the builder-init shim.
const builderInitScriptInstallPath = "/usr/local/bin/builder-init.sh"

// builderInitScript is the PID-1 shim executed inside the builder VM.
// It sets a minimal PATH, marks the environment as a builder VM, and
// execs nexus3-agent which then starts buildkitd and handles agent RPC.
const builderInitScript = `#!/bin/sh
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export NEXUS_VMBUILDER=1
exec ` + nexus3AgentInstallPath + "\n"

// addBootLayers adds VM-boot infrastructure to the extracted rootfs staging
// directory so that the moby/buildkit container image can boot as a
// cloud-hypervisor VM rootfs under nexus3.
//
// It injects:
//  1. builder-init.sh — the PID-1 shim that execs nexus3-agent.
//  2. /sbin/init — a one-liner that delegates to builder-init.sh.
//  3. /etc/securetty — appended with "ttyS0" so the serial console works.
//  4. /workspace — ext4 virtio-blk disk mount point (virtiofs measured and
//     rejected ~20× metadata penalty; see spike/virtiofs/).
//  5. /run/buildkit — buildkitd socket directory.
//  6. /var/lib/buildkit — scratch block device mount point.
//  7. nexus3-agent binary at nexus3AgentInstallPath.
//  8. /etc/resolv.conf — seeded with public DNS; guest agent overwrites later.
func addBootLayers(stagingDir string, agentBytes []byte) error {
	if len(agentBytes) == 0 {
		return fmt.Errorf("addBootLayers: agentBytes must not be empty")
	}

	// 1. builder-init.sh
	initScriptPath := filepath.Join(stagingDir, filepath.FromSlash(builderInitScriptInstallPath[1:]))
	if err := os.MkdirAll(filepath.Dir(initScriptPath), 0o755); err != nil {
		return fmt.Errorf("mkdir for builder-init.sh: %w", err)
	}
	_ = os.Remove(initScriptPath)
	if err := os.WriteFile(initScriptPath, []byte(builderInitScript), 0o755); err != nil {
		return fmt.Errorf("write builder-init.sh: %w", err)
	}

	// 2. /sbin/init — overwrite any existing file or symlink so our shim wins.
	sbinInit := filepath.Join(stagingDir, "sbin", "init")
	if err := os.MkdirAll(filepath.Dir(sbinInit), 0o755); err != nil {
		return fmt.Errorf("mkdir /sbin: %w", err)
	}
	_ = os.Remove(sbinInit)
	initContent := "#!/bin/sh\nexec " + builderInitScriptInstallPath + "\n"
	if err := os.WriteFile(sbinInit, []byte(initContent), 0o755); err != nil {
		return fmt.Errorf("write /sbin/init: %w", err)
	}

	// 3. /etc/securetty — add ttyS0 for serial console login.
	securetty := filepath.Join(stagingDir, "etc", "securetty")
	if err := os.MkdirAll(filepath.Dir(securetty), 0o755); err != nil {
		return fmt.Errorf("mkdir /etc for securetty: %w", err)
	}
	existing, _ := os.ReadFile(securetty)
	if !strings.Contains(string(existing), "ttyS0") {
		f, err := os.OpenFile(securetty, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open /etc/securetty: %w", err)
		}
		_, _ = f.WriteString("ttyS0\n")
		_ = f.Close()
	}

	// 4. /workspace — ext4 virtio-blk disk mount point (virtiofs rejected; see spike/virtiofs/).
	if err := os.MkdirAll(filepath.Join(stagingDir, "workspace"), 0o755); err != nil {
		return fmt.Errorf("mkdir /workspace: %w", err)
	}

	// 5. /run/buildkit — buildkitd socket directory.
	if err := os.MkdirAll(filepath.Join(stagingDir, "run", "buildkit"), 0o755); err != nil {
		return fmt.Errorf("mkdir /run/buildkit: %w", err)
	}

	// 6. /var/lib/buildkit — mount point for the scratch block device.
	if err := os.MkdirAll(filepath.Join(stagingDir, "var", "lib", "buildkit"), 0o711); err != nil {
		return fmt.Errorf("mkdir /var/lib/buildkit: %w", err)
	}

	// 7. nexus3-agent binary.
	agentDst := filepath.Join(stagingDir, filepath.FromSlash(nexus3AgentInstallPath[1:]))
	if err := os.MkdirAll(filepath.Dir(agentDst), 0o755); err != nil {
		return fmt.Errorf("mkdir for nexus3-agent: %w", err)
	}
	_ = os.Remove(agentDst)
	if err := os.WriteFile(agentDst, agentBytes, 0o755); err != nil {
		return fmt.Errorf("write nexus3-agent into rootfs: %w", err)
	}

	// 7b. /sbin/nexus3-agent — second copy of the agent binary.
	// The default cloud-hypervisor disk-boot cmdline is "init=/sbin/nexus3-agent"
	// and vmbuilder.guestBuild execs "/sbin/nexus3-agent --builder-role". The
	// builder rootfs places the primary agent at /usr/local/bin; writing a second
	// copy to /sbin avoids any symlink-resolution ambiguity at kernel exec time
	// (the Linux kernel does NOT follow symlinks for init=).
	sbinAgent := filepath.Join(stagingDir, "sbin", "nexus3-agent")
	_ = os.Remove(sbinAgent)
	if err := os.WriteFile(sbinAgent, agentBytes, 0o755); err != nil {
		return fmt.Errorf("write /sbin/nexus3-agent into rootfs: %w", err)
	}

	// 8. /etc/resolv.conf — ensure it is a real file (not a symlink).
	//    The guest agent will overwrite it with live resolvers after boot.
	etcDir := filepath.Join(stagingDir, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("mkdir /etc: %w", err)
	}
	resolvConfDst := filepath.Join(etcDir, "resolv.conf")
	_ = os.Remove(resolvConfDst) // remove any pre-existing symlink from base image
	const resolvContents = "# seeded by nexus3 builderimage — agent overwrites after boot\nnameserver 8.8.8.8\nnameserver 1.1.1.1\n"
	if err := os.WriteFile(resolvConfDst, []byte(resolvContents), 0o644); err != nil {
		return fmt.Errorf("write /etc/resolv.conf: %w", err)
	}

	return nil
}
