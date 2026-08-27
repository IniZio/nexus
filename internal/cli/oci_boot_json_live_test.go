//go:build herdr_live

// Package cli — live end-to-end proof that OCI ENTRYPOINT/Cmd/WorkingDir/Env
// written to boot.json by the buildkit Solve seam survive ext4 packaging and
// are actually executed as a supervised child by the nexus3-agent at PID-1
// boot.
//
// # What this test proves
//
// The advisor's uncovered finding #2: neither the boot.json file surviving into
// the packaged ext4 image, nor the agent actually running the declared task at
// PID-1 boot, had been exercised on a real boot until this test.
//
//  1. boot.json content — cat /etc/nexus3/boot.json inside the booted sandbox
//     parses to a Spec with Tasks[0].Argv matching the Containerfile ENTRYPOINT,
//     Cwd matching WORKDIR, and Env containing the declared ENV.
//
//  2. Boot-task execution — /tmp/nexus-boot-marker exists with content "booted"
//     because the agent ran the ENTRYPOINT command as a supervised background
//     goroutine at PID-1 boot time.
//
// # Build seam under test
//
// `nexus3 create --file <workspace>` → builder VM → BuildInGuestImage →
// CaptureBootSpecFromContainerfile (Dockerfile-parse of ContainerfileBytes,
// no buildkitd metadata) writes <rootfsOutDir>/etc/nexus3/boot.json →
// ext4 packaging → sandbox boot →
// nexus3-agent runBootTasks reads /etc/nexus3/boot.json → runBootTask.
//
// # Run
//
//	TMPDIR=/tmp NEXUS3_KERNEL_PATH=$(pwd)/images/kernel/vmlinux-x86_64 \
//	  NEXUS3_LIVE_REQUIRED=1 \
//	  go test -tags herdr_live ./internal/cli/ \
//	  -run TestOCIBootJSON_Live_BootspecSurvivesAndExecutes -v -count=1 \
//	  -timeout 30m
//
// Prerequisites:
//   - /dev/kvm available
//   - NEXUS3_KERNEL_PATH set to a vmlinux image
//   - nexus3-agent-base cached (used to build the builder VM)
//   - docker.io/library/alpine:3.19 reachable (or already in buildkit cache)
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/bootspec"
)

// ociBootCmd is worktreeLiveCmd's analogue for the OCI-boot live tests:
// strips XDG_STATE_HOME from the environment so the real operator store is
// used by the subprocess (the package TestMain clobbers XDG_STATE_HOME to an
// empty temp dir to isolate unit tests from live state).
func ociBootCmd(binary string, args ...string) *exec.Cmd {
	realState := worktreeLiveStateHome() // defined in herdr_worktree_sandbox_live_test.go
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "XDG_STATE_HOME=") {
			continue
		}
		env = append(env, kv)
	}
	if realState != "" {
		env = append(env, "XDG_STATE_HOME="+realState)
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	return cmd
}

// createOCIBootWorkspace creates a minimal workspace with a .nexus/Containerfile
// whose ENTRYPOINT writes /tmp/nexus-boot-marker and then sleeps forever,
// WORKDIR is /srv, and ENV BOOT_E2E=yes.
//
// The ENTRYPOINT runs as a background goroutine (Background=true via
// bootspec.FromOCIImageConfig) so the agent proceeds to bind its control
// plane — exec into the sandbox works — while the marker appears shortly
// after boot.
func createOCIBootWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	nexusDir := filepath.Join(dir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0o755); err != nil {
		t.Fatalf("mkdir .nexus: %v", err)
	}
	cf := `FROM docker.io/library/alpine:3.19
WORKDIR /srv
ENV BOOT_E2E=yes
ENTRYPOINT ["/bin/sh","-c","echo booted > /tmp/nexus-boot-marker && exec sleep infinity"]
`
	if err := os.WriteFile(filepath.Join(nexusDir, "Containerfile"), []byte(cf), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}
	return dir
}

func TestOCIBootJSON_Live_BootspecSurvivesAndExecutes(t *testing.T) {
	// ── 0. Skip guards ────────────────────────────────────────────────────────
	if os.Getenv("NEXUS3_LIVE_REQUIRED") == "" {
		t.Skip("set NEXUS3_LIVE_REQUIRED=1 to run live tests (requires KVM + built images + alpine pull)")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		liveSkip(t, "oci-boot-json-live: /dev/kvm not available: %v", err)
	}
	if os.Getenv("NEXUS3_KERNEL_PATH") == "" {
		liveSkip(t, "oci-boot-json-live: NEXUS3_KERNEL_PATH not set; set it to a vmlinux image")
	}
	// The --file path needs a builder rootfs; that is derived from nexus3-agent-base.
	// Skip cleanly rather than failing when the operator hasn't yet built it.
	if !baseImageCached(t, herdrDefaultImage) {
		liveSkip(t, "oci-boot-json-live: %q not cached — run `go run ./cmd/rebuild-agent-base`", herdrDefaultImage)
	}

	// ── 1. Build nexus3 binary from this branch ───────────────────────────────
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3-oci-boot-live")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus3")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		liveSkip(t, "oci-boot-json-live: nexus3 binary cannot be built: %v\n%s", err, out)
	}
	t.Logf("built nexus3 binary: %s", binary)

	// ── 2. Fixture workspace ──────────────────────────────────────────────────
	workspace := createOCIBootWorkspace(t)
	t.Logf("fixture workspace: %s", workspace)

	// ── 3. Create sandbox via --file path ─────────────────────────────────────
	handle := fmt.Sprintf("ocibtest/oci-boot-%d", time.Now().UnixMilli())
	t.Cleanup(func() {
		rmOut, rmErr := ociBootCmd(binary, "rm", handle).CombinedOutput()
		if rmErr != nil {
			t.Logf("cleanup: nexus3 rm %s: %v\n%s", handle, rmErr, rmOut)
		} else {
			t.Logf("cleanup: nexus3 rm %s: %s", handle, rmOut)
		}
	})

	// create --file drives the full OCI-config→boot.json seam:
	//   builder VM → BuildInGuestImage → Solve → captureBootSpec
	createOut, createErr := ociBootCmd(binary,
		"create", handle,
		"--file", workspace,
	).CombinedOutput()
	t.Logf("nexus3 create --file:\n%s", createOut)
	if createErr != nil {
		t.Fatalf("nexus3 create --file: %v\n%s\n(check NEXUS3_KERNEL_PATH, %q cached, and network for alpine pull)",
			createErr, createOut, herdrDefaultImage)
	}

	// ── 4. Assert boot.json is present and well-formed ────────────────────────
	//
	// PROOF 1: boot.json survived ext4 packaging.
	bootJSONOut, bootJSONErr := ociBootCmd(binary, "exec", handle, "--",
		"/bin/sh", "-c", "cat /etc/nexus3/boot.json",
	).CombinedOutput()
	t.Logf("nexus3 exec cat /etc/nexus3/boot.json:\n%s", bootJSONOut)
	if bootJSONErr != nil {
		// Also try to list the directory so we can diagnose the gap.
		lsOut, _ := ociBootCmd(binary, "exec", handle, "--",
			"/bin/sh", "-c", "ls -la /etc/nexus3/ 2>&1 || echo 'dir missing'",
		).CombinedOutput()
		t.Logf("/etc/nexus3/ listing:\n%s", lsOut)
		t.Fatalf("exec cat /etc/nexus3/boot.json: %v — boot.json absent in-guest (REAL FINDING: file lost in ext4 packaging or Solve seam)", bootJSONErr)
	}

	var spec bootspec.Spec
	// Strip any trailing whitespace/newlines exec might add.
	jsonBytes := bytes.TrimSpace(bootJSONOut)
	if err := json.Unmarshal(jsonBytes, &spec); err != nil {
		t.Fatalf("boot.json does not parse as bootspec.Spec: %v\nraw: %s", err, bootJSONOut)
	}
	if len(spec.Tasks) == 0 {
		t.Fatalf("boot.json Tasks slice is empty; expected 1 task from ENTRYPOINT\nraw: %s", bootJSONOut)
	}

	task0 := spec.Tasks[0]
	wantArgv := []string{"/bin/sh", "-c", "echo booted > /tmp/nexus-boot-marker && exec sleep infinity"}
	if len(task0.Argv) != len(wantArgv) {
		t.Errorf("Tasks[0].Argv length mismatch: got %d want %d\n  got:  %v\n  want: %v",
			len(task0.Argv), len(wantArgv), task0.Argv, wantArgv)
	} else {
		for i, w := range wantArgv {
			if task0.Argv[i] != w {
				t.Errorf("Tasks[0].Argv[%d] = %q, want %q", i, task0.Argv[i], w)
			}
		}
	}
	if task0.Cwd != "/srv" {
		t.Errorf("Tasks[0].Cwd = %q, want %q", task0.Cwd, "/srv")
	}

	var hasBOOT_E2E bool
	for _, kv := range task0.Env {
		if strings.HasPrefix(kv, "BOOT_E2E=") {
			hasBOOT_E2E = true
			if kv != "BOOT_E2E=yes" {
				t.Errorf("Tasks[0].Env contains %q, want BOOT_E2E=yes", kv)
			}
		}
	}
	if !hasBOOT_E2E {
		t.Errorf("Tasks[0].Env does not contain BOOT_E2E=yes; Env=%v", task0.Env)
	}
	if !task0.Background {
		t.Errorf("Tasks[0].Background = false, want true (OCI ENTRYPOINT must be background)")
	}

	t.Logf("PROOF 1 PASS: boot.json present in-guest with correct Argv/Cwd/Env/Background")

	// ── 5. Assert boot task ran (marker file present) ─────────────────────────
	//
	// PROOF 2: the agent actually executed the declared boot task.
	// The task runs in a background goroutine at boot; give it a few seconds to
	// complete before polling. A single exec with a brief sleep+retry is fine
	// because the marker write is fast (echo + redirect).
	const markerPollScript = `
for i in $(seq 1 10); do
  if [ -f /tmp/nexus-boot-marker ]; then
    cat /tmp/nexus-boot-marker
    exit 0
  fi
  sleep 1
done
echo "MARKER_ABSENT_AFTER_10S" >&2
exit 1
`
	markerOut, markerErr := ociBootCmd(binary, "exec", handle, "--",
		"/bin/sh", "-c", markerPollScript,
	).CombinedOutput()
	t.Logf("nexus3 exec marker poll:\n%s", markerOut)
	if markerErr != nil {
		// Extra diagnostics: check if the boot task left any console output
		// (agent logs to /dev/console; we can't read that in-guest but can
		// at least confirm the process tree).
		psOut, _ := ociBootCmd(binary, "exec", handle, "--",
			"/bin/sh", "-c", "ps aux 2>/dev/null || ps",
		).CombinedOutput()
		t.Logf("in-guest ps:\n%s", psOut)
		t.Fatalf("boot marker /tmp/nexus-boot-marker absent after 10s — REAL FINDING: boot task was NOT executed at PID-1 boot\nmarker poll output: %s", markerOut)
	}

	markerContent := strings.TrimSpace(string(markerOut))
	if markerContent != "booted" {
		t.Errorf("marker content = %q, want %q", markerContent, "booted")
	}

	t.Logf("PROOF 2 PASS: /tmp/nexus-boot-marker = %q — boot task ran as supervised child at PID-1 boot", markerContent)
	t.Log("VM-boot e2e LIVE-PROVEN: boot.json survives ext4 packaging and the declared ENTRYPOINT task executes at guest boot")
}
