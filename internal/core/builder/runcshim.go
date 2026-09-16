package builder

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// RuncShimInstallPath is where the runc shim is baked into every `--file`
// guest image. /usr/local/sbin precedes the distro's /usr/sbin and /usr/bin on
// the PATH dockerd, containerd and their shims inherit, so every container
// runtime lookup of "runc" resolves to the shim without any daemon.json edit.
// dockerd's built-in BuildKit resolves "runc" once at daemon start
// (moby daemon/internal/builder-next/executor_linux.go), which is why the shim
// is baked into the image rather than seeded after boot.
const RuncShimInstallPath = "/usr/local/sbin/runc"

// runcShimContextFilename is the shim's filename inside the "nexus3agent"
// named build context.
const runcShimContextFilename = "nexus3-runc"

// runcShimScript is the shim source. It is a fingerprint input
// ([BuildFingerprint]) so a shim change rebuilds cached images.
//
//go:embed nexus3-runc.sh
var runcShimScript []byte

// stageRuncShim writes the shim into agentDir (the "nexus3agent" build
// context) and returns its context-relative filename.
func stageRuncShim(agentDir string) (string, error) {
	if err := os.WriteFile(filepath.Join(agentDir, runcShimContextFilename), runcShimScript, 0755); err != nil {
		return "", fmt.Errorf("buildkit: write runc shim to agent dir: %w", err)
	}
	return runcShimContextFilename, nil
}
