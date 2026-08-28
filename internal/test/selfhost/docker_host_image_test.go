//go:build integration

package selfhost

// docker_host_image_test.go — KVM-gated full-boot test for the
// examples/nexus3-in-docker example image.
//
// Proves that the nexus3 host daemon runs correctly inside a Docker container,
// boots a microVM, builds a trivial Containerfile, and executes a command in
// the guest — the same end-to-end path documented in examples/nexus3-in-docker/README.md.
//
// # Skip conditions
//
//   - /dev/kvm absent or inaccessible
//   - docker not in PATH
//   - virtiofsd not in PATH (required by build.sh)
//   - testing.Short() — multi-minute docker build + VM boot
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration \
//	    -run TestExampleNexus3InDocker_BootsMicroVM \
//	    ./internal/test/selfhost/ -v -timeout 30m

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestExampleNexus3InDocker_BootsMicroVM builds the nexus3-host image using
// examples/nexus3-in-docker/build.sh, runs the documented full-boot recipe
// (fresh container-owned volumes, minimal caps), and asserts that
// `nexus3 exec` returns "built" and "Linux" from inside the guest.
func TestExampleNexus3InDocker_BootsMicroVM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: -short set — docker build + VM boot takes several minutes")
	}

	// ── skip guards ────────────────────────────────────────────────────────────

	skipUnlessKVMSH(t)

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("skipping: docker not in PATH")
	}
	if _, err := exec.LookPath("virtiofsd"); err != nil {
		t.Skip("skipping: virtiofsd not in PATH — required by examples/nexus3-in-docker/build.sh")
	}

	// ── locate repo root ───────────────────────────────────────────────────────

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}

	// ── test-scoped Docker resource names ─────────────────────────────────────

	const (
		imageTag    = "nexus3-host:citest"
		stateVol    = "n3state_citest"
		workVol     = "n3work_citest"
		containerID = "nexus3-dockertest"
	)

	// Clean up all Docker resources on exit, even on failure.
	t.Cleanup(func() {
		runDockerClean(t, "rm", "-f", containerID)
		runDockerClean(t, "volume", "rm", "-f", stateVol)
		runDockerClean(t, "volume", "rm", "-f", workVol)
	})

	// ── build the image via the example's build.sh ─────────────────────────────

	t.Log("building nexus3-host image via examples/nexus3-in-docker/build.sh …")

	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancelBuild()

	// build.sh hardcodes the tag nexus3-host:latest; re-tag after the build.
	buildCmd := exec.CommandContext(buildCtx, "bash",
		fmt.Sprintf("%s/examples/nexus3-in-docker/build.sh", repoRoot),
	)
	buildCmd.Dir = repoRoot
	var buildOut bytes.Buffer
	buildCmd.Stdout = &buildOut
	buildCmd.Stderr = &buildOut
	if err := buildCmd.Run(); err != nil {
		t.Logf("build.sh output:\n%s", buildOut.String())
		t.Fatalf("build.sh failed: %v", err)
	}
	t.Logf("build.sh succeeded")

	// Re-tag nexus3-host:latest → test-scoped tag so a concurrent real user's
	// nexus3-host:latest is not clobbered.
	tagCmd := exec.Command("docker", "tag", "nexus3-host:latest", imageTag)
	if out, err := tagCmd.CombinedOutput(); err != nil {
		t.Fatalf("docker tag nexus3-host:latest %s: %v\n%s", imageTag, err, out)
	}

	// ── create fresh volumes ───────────────────────────────────────────────────

	for _, vol := range []string{stateVol, workVol} {
		out, err := exec.Command("docker", "volume", "create", vol).CombinedOutput()
		if err != nil {
			t.Fatalf("docker volume create %s: %v\n%s", vol, err, out)
		}
	}

	// ── full boot: build + start + exec ───────────────────────────────────────

	t.Log("running nexus3 create / start / exec inside container …")

	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancelBoot()

	// The shell script mirrors the documented "full boot" run recipe from
	// examples/nexus3-in-docker/README.md.
	// Use /bin/bash explicitly — /bin/sh on debian:bookworm-slim is dash,
	// which does not support -o pipefail.
	bootScript := `
set -euo pipefail
mkdir -p /work/src/.nexus /work/tmp /work/rt
printf 'FROM alpine:3.20\nRUN echo built > /marker\n' > /work/src/.nexus/Containerfile
nexus3 create ephemeral/demo --file /work/src
nexus3 start ephemeral/demo
nexus3 exec ephemeral/demo -- sh -c 'cat /marker && uname -sr'
`

	runArgs := []string{
		"run", "--rm",
		"--name", containerID,
		"--device", "/dev/kvm",
		"--device", "/dev/net/tun",
		"--cap-add", "NET_ADMIN",
		"--cap-add", "SYS_ADMIN",
		"-e", "TMPDIR=/work/tmp",
		"-e", "XDG_RUNTIME_DIR=/work/rt",
		"-v", stateVol + ":/root/.local/state/nexus3",
		"-v", workVol + ":/work",
		"--entrypoint", "/bin/bash",
		imageTag,
		"-c", bootScript,
	}

	bootCmd := exec.CommandContext(bootCtx, "docker", runArgs...)
	var bootOut bytes.Buffer
	bootCmd.Stdout = &bootOut
	bootCmd.Stderr = &bootOut

	bootErr := bootCmd.Run()
	output := bootOut.String()
	t.Logf("container output:\n%s", output)

	if bootErr != nil {
		t.Fatalf("container run failed: %v", bootErr)
	}

	// ── assertions ────────────────────────────────────────────────────────────

	if !strings.Contains(output, "built") {
		t.Errorf("expected 'built' in exec output; got:\n%s", output)
	}
	if !strings.Contains(output, "Linux") {
		t.Errorf("expected 'Linux' in exec output (from uname -sr); got:\n%s", output)
	}

	t.Log("TestExampleNexus3InDocker_BootsMicroVM PASSED")
}

// runDockerClean runs a docker command, ignoring errors (cleanup best-effort).
func runDockerClean(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("cleanup: docker %v: %v\n%s", args, err, out)
	}
}
