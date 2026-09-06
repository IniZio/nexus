package supervisor

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// captureOverlayScript calls seedOverlayClaudeConfig with a stub execer that
// captures the shell script passed to /bin/bash, then returns it. The stub
// always reports exit 0.
func captureOverlayScript(t *testing.T, lower string) string {
	t.Helper()
	var script string
	execer := service.GuestExecer(func(_ context.Context, _ domain.SandboxID, argv []string, _ io.Reader) (int32, error) {
		// The script is the last element: ["/bin/bash", "-c", <script>]
		if len(argv) >= 3 {
			script = argv[2]
		}
		return 0, nil
	})
	if err := seedOverlayClaudeConfig(context.Background(), domain.SandboxID{}, lower, execer); err != nil {
		t.Fatalf("seedOverlayClaudeConfig: %v", err)
	}
	return script
}

// TestSeedOverlayClaudeConfig_PersistentUpperDir is the mutation guard for the
// persistent upper-dir design:
//
//	Mutation: change agentCfgUpperDir to a tmpfs path → RED (upperdir wrong)
//	Mutation: add "mount -t tmpfs" for the overlay → RED (tmpfs detected)
//	Mutation: remove "rm -rf" of work dir → RED (work dir not cleaned)
//
// The upper dir must be on the named ext4 volume mounted at
// /var/lib/nexus3/agentcfg (not on root ext4, not on tmpfs) so Claude session
// transcripts survive sandbox stop/start and crash+recover, and so the disk
// governor can grow the volume when the upper layer fills.
// Using tmpfs would discard all in-guest Claude state on every VM restart.
func TestSeedOverlayClaudeConfig_PersistentUpperDir(t *testing.T) {
	const lower = "/run/nexus3/agentcfg-lower"
	script := captureOverlayScript(t, lower)

	// 1. upperdir must be the persistent ext4 path, not any tmpfs path.
	if !strings.Contains(script, "upperdir="+agentCfgUpperDir) {
		t.Errorf("script does not use persistent upperdir %q;\ngot script:\n%s", agentCfgUpperDir, script)
	}

	// 2. workdir must be the paired persistent work dir.
	if !strings.Contains(script, "workdir="+agentCfgWorkDir) {
		t.Errorf("script does not use persistent workdir %q;\ngot script:\n%s", agentCfgWorkDir, script)
	}

	// 3. The overlay must NOT mount a tmpfs for the upper/work base.
	//    Mounting tmpfs here would discard all upper-layer writes on VM exit.
	//    (Note: /tmp itself uses tmpfs, but that is unrelated to the overlay.)
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "mount -t tmpfs") && strings.Contains(line, agentCfgUpperDir[:len("/var/lib")]) {
			t.Errorf("script mounts a tmpfs under /var/lib — upper writes would be discarded on VM exit;\noffending line: %q\nfull script:\n%s", line, script)
		}
		// Any tmpfs mount whose target overlaps the agentcfg dirs is wrong.
		if strings.Contains(line, "mount -t tmpfs") && (strings.Contains(line, "agentcfg") || strings.Contains(line, "/run/nexus3/ovl")) {
			t.Errorf("script mounts tmpfs for the agentcfg overlay — upper writes would not persist;\noffending line: %q\nfull script:\n%s", line, script)
		}
	}

	// 4. Work dir must be rm-rf'd before mkdir so overlayfs gets an empty dir.
	rmIdx := strings.Index(script, "rm -rf "+agentCfgWorkDir)
	if rmIdx < 0 {
		t.Errorf("script does not rm -rf %q before mounting;\noverlayfs requires empty work dir at mount time;\ngot script:\n%s", agentCfgWorkDir, script)
	}
	mkdirIdx := strings.Index(script, "mkdir "+agentCfgWorkDir)
	if mkdirIdx < 0 {
		t.Errorf("script does not mkdir %q after rm -rf;\ngot script:\n%s", agentCfgWorkDir, script)
	}
	if rmIdx > mkdirIdx {
		t.Errorf("rm -rf appears after mkdir for %q — work dir would not be empty at mount time;\ngot script:\n%s", agentCfgWorkDir, script)
	}

	// 5. lowerdir must be the caller-supplied lower path.
	if !strings.Contains(script, "lowerdir="+lower) {
		t.Errorf("script does not use lowerdir=%q;\ngot script:\n%s", lower, script)
	}

	// 6. Target mountpoint must be /root/.claude.
	if !strings.Contains(script, "/root/.claude") {
		t.Errorf("script does not mount onto /root/.claude;\ngot script:\n%s", script)
	}
}

// TestSeedOverlayClaudeConfig_NoPersistentTmpfsForOverlay is a stand-alone
// mutation guard: the script must NOT contain "mount -t tmpfs tmpfs /run/nexus3/ovl"
// (the old ephemeral pattern). If a future edit reverts to tmpfs, this fails RED.
func TestSeedOverlayClaudeConfig_NoPersistentTmpfsForOverlay(t *testing.T) {
	script := captureOverlayScript(t, "/run/nexus3/agentcfg-lower")
	if strings.Contains(script, "/run/nexus3/ovl") {
		t.Errorf("script uses the old /run/nexus3/ovl tmpfs path — upper writes would be discarded on VM exit;\ngot script:\n%s", script)
	}
}

// TestSeedOverlayClaudeConfig_UpperDirOnNamedVolume is the mutation guard for
// the D-RAM-08 Option B decision: both the overlayfs upperdir and workdir must
// live under the named ext4 volume mount point (/var/lib/nexus3/agentcfg/),
// NOT under /var/lib/nexus3/ directly (which is root ext4 and not
// governor-visible).
//
// Mutations that MUST turn this RED:
//   - Change agentCfgUpperDir to /var/lib/nexus3/agentcfg-upper (reverts to root)
//   - Change agentCfgWorkDir to /var/lib/nexus3/agentcfg-work (reverts to root)
//   - Move workdir to a path that does not share the named volume prefix
//     (violates the kernel's same-filesystem requirement for overlayfs)
//
// This test does NOT boot a VM — it checks path constants only.
func TestSeedOverlayClaudeConfig_UpperDirOnNamedVolume(t *testing.T) {
	const namedVolMount = "/var/lib/nexus3/agentcfg"

	if !strings.HasPrefix(agentCfgUpperDir, namedVolMount+"/") {
		t.Errorf("agentCfgUpperDir %q is not under the named volume mount point %q;\n"+
			"moving upperdir off root ext4 (D-RAM-08 Option B) requires both upper and work\n"+
			"to live under %q so the governor can grow the volume",
			agentCfgUpperDir, namedVolMount, namedVolMount)
	}

	if !strings.HasPrefix(agentCfgWorkDir, namedVolMount+"/") {
		t.Errorf("agentCfgWorkDir %q is not under the named volume mount point %q;\n"+
			"overlayfs requires upper and work on the same filesystem — workdir must share\n"+
			"the named volume at %q with upperdir %q",
			agentCfgWorkDir, namedVolMount, namedVolMount, agentCfgUpperDir)
	}

	// upper and work must not be the same path.
	if agentCfgUpperDir == agentCfgWorkDir {
		t.Errorf("agentCfgUpperDir and agentCfgWorkDir must be distinct paths; both are %q", agentCfgUpperDir)
	}

	// The script must still use the (updated) constants — cross-check with the
	// captured script so a hard-coded path in the format string would be caught.
	script := captureOverlayScript(t, "/run/nexus3/agentcfg-lower")
	if !strings.Contains(script, "upperdir="+agentCfgUpperDir) {
		t.Errorf("script upperdir does not match agentCfgUpperDir %q;\ngot script:\n%s", agentCfgUpperDir, script)
	}
	if !strings.Contains(script, "workdir="+agentCfgWorkDir) {
		t.Errorf("script workdir does not match agentCfgWorkDir %q;\ngot script:\n%s", agentCfgWorkDir, script)
	}
}
