//go:build herdr_live

// T0-OVERLAY-TEST: codifies the proven live experiment that overlayfs-on-virtiofs
// works in a real nexus3 sandbox.
//
// # What is under test
//
// A read-only virtiofs share is mounted into the guest at /mnt/roconfig. The
// test then mounts an overlay filesystem on top of that virtiofs lower layer.
// This proves:
//
//  1. virtiofs ro mount lands at the expected path (grep /proc/mounts).
//  2. overlayfs can use a virtiofs share as its lower dir (mount -t overlay).
//  3. Lower-dir content is visible through the merged dir (cat CLAUDE.md).
//  4. Writes into the overlay merged dir work (new file + copy-up of existing file).
//  5. Host curated dir is BYTE-IDENTICAL after the sandbox is removed —
//     the guest's copy-up lands in the tmpfs upper and never touches the host.
//
// Run with:
//
//	TMPDIR=/tmp NEXUS3_KERNEL_PATH=$(pwd)/images/kernel/vmlinux-x86_64 \
//	  NEXUS3_LIVE_REQUIRED=1 \
//	  go test -count=1 -tags herdr_live ./internal/cli/ -run TestOverlayOnVirtiofs -v -timeout 300s
//
// Prerequisites:
//   - /dev/kvm must be present
//   - NEXUS3_KERNEL_PATH must be set to a vmlinux image
//   - nexus3-agent-base image must be locally cached (or set NEXUS3_OVERLAY_IMAGE)
package cli

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// overlayCmd builds a nexus3 subprocess with the live-state environment
// (XDG_STATE_HOME unredirected so the real sandbox store is used).
func overlayCmd(binary string, args ...string) *exec.Cmd {
	env := os.Environ()
	out := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "XDG_STATE_HOME=") {
			continue
		}
		out = append(out, kv)
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = out
	return cmd
}

// sha256Dir returns a deterministic map[relpath]hexsum for every regular file
// under root. Used to assert the host curated dir is untouched after the run.
func sha256Dir(root string) (map[string]string, error) {
	sums := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		sums[rel] = fmt.Sprintf("%x", h.Sum(nil))
		return nil
	})
	return sums, err
}

func TestOverlayOnVirtiofs(t *testing.T) {
	// --- 0. Prerequisites: skip, never fail, when absent. ---
	if _, err := os.Stat("/dev/kvm"); err != nil {
		liveSkip(t, "overlay: /dev/kvm not available: %v", err)
	}
	if os.Getenv("NEXUS3_KERNEL_PATH") == "" {
		liveSkip(t, "overlay: NEXUS3_KERNEL_PATH is not set; set it to a vmlinux image to run this test")
	}

	// --- 1. Build the nexus3 binary. ---
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3-overlay")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus3")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		liveSkip(t, "overlay: nexus3 binary cannot be built: %v\n%s", err, out)
	}

	// --- 2. Host curated dir with known content. ---
	curatedDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(curatedDir, "CLAUDE.md"), []byte("GLOBAL INSTRUCTIONS v1\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	skillsDir := filepath.Join(curatedDir, "skills", "demo")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills/demo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte("demo skill\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	// Snapshot host content BEFORE the sandbox run.
	beforeSums, err := sha256Dir(curatedDir)
	if err != nil {
		t.Fatalf("sha256Dir before: %v", err)
	}

	// --- 3. Create sandbox. ---
	image := os.Getenv("NEXUS3_OVERLAY_IMAGE")
	if image == "" {
		image = herdrDefaultImage
	}
	// Unique handle per run — never collide with live operator sandboxes.
	handle := fmt.Sprintf("ovltest/overlay-%d", time.Now().UnixMilli())
	guestMount := "/mnt/roconfig"

	// Register cleanup BEFORE create so a t.Fatal still tears down whatever got created.
	t.Cleanup(func() {
		rmOut, rmErr := overlayCmd(binary, "rm", handle).CombinedOutput()
		if rmErr != nil {
			t.Logf("cleanup: nexus3 rm %s: %v\n%s", handle, rmErr, rmOut)
		} else {
			t.Logf("cleanup: nexus3 rm %s: %s", handle, rmOut)
		}
	})

	createOut, err := overlayCmd(binary, "create", handle,
		"--image", image,
		"--mount", curatedDir+":"+guestMount+":ro",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("nexus3 create: %v\n%s\n(check NEXUS3_KERNEL_PATH and that %q is a cached image)", err, createOut, image)
	}
	t.Logf("nexus3 create: %s", createOut)

	// --- 4. In-guest overlay script. ---
	//
	// The script is written as a single -c argument to /bin/bash. It:
	//  a) asserts /mnt/roconfig is a virtiofs ro mount via /proc/mounts
	//  b) mounts a tmpfs base, creates upper/work/merged dirs
	//  c) mounts overlay with virtiofs as lowerdir
	//  d) reads the lower content through merged
	//  e) writes a new file into merged (verifies overlay is writable)
	//  f) appends to CLAUDE.md through merged (exercises copy-up)
	//  g) prints OVERLAY_TRACER_OK and the lower content as the assertion token
	//
	// Any step failure causes exit 1 (set -e). The token never appears in the
	// command line itself so matching it in stdout proves the script ran to
	// completion, not just that the shell echoed the command.
	script := `
set -euo pipefail

# a. Verify virtiofs ro mount is present.
if ! grep -q 'virtiofs' /proc/mounts; then
  echo "FAIL: /mnt/roconfig is not a virtiofs mount" >&2
  exit 1
fi
if ! grep -E '\s/mnt/roconfig\s' /proc/mounts | grep -q 'ro'; then
  echo "FAIL: /mnt/roconfig virtiofs mount is not read-only" >&2
  exit 1
fi

# b. Set up overlay base in tmpfs.
mount -t tmpfs tmpfs /mnt/ovlbase 2>/dev/null || {
  mkdir -p /mnt/ovlbase
  mount -t tmpfs tmpfs /mnt/ovlbase
}
mkdir -p /mnt/ovlbase/upper /mnt/ovlbase/work /mnt/ovlbase/merged

# c. Mount overlay with virtiofs as lowerdir.
mount -t overlay overlay \
  -o lowerdir=/mnt/roconfig,upperdir=/mnt/ovlbase/upper,workdir=/mnt/ovlbase/work \
  /mnt/ovlbase/merged

# d. Lower content is visible through merged.
LOWER_CONTENT=$(cat /mnt/ovlbase/merged/CLAUDE.md)
if [ -z "$LOWER_CONTENT" ]; then
  echo "FAIL: CLAUDE.md empty through merged dir" >&2
  exit 1
fi

# e. Write a new file into merged (overlay writable).
echo "overlay-new" > /mnt/ovlbase/merged/NEW.md

# f. Append to CLAUDE.md via merged (exercises copy-up).
echo "appended-by-overlay" >> /mnt/ovlbase/merged/CLAUDE.md

# g. Confirm lower is UNCHANGED (the append landed in upper, not lower).
LOWER_NOW=$(cat /mnt/roconfig/CLAUDE.md)
if [ "$LOWER_NOW" != "$LOWER_CONTENT" ]; then
  echo "FAIL: lower dir was mutated by overlay write (copy-up broke)" >&2
  exit 1
fi

echo "OVERLAY_TRACER_OK"
echo "LOWER_CONTENT:${LOWER_CONTENT}"
`

	execOut, err := overlayCmd(binary, "exec", "--cwd", "/root", handle,
		"/bin/bash", "-c", script,
	).CombinedOutput()
	t.Logf("nexus3 exec output:\n%s", execOut)
	if err != nil {
		t.Fatalf("nexus3 exec script failed: %v\n%s", err, execOut)
	}

	// --- 5. Assert success token and lower content. ---
	outStr := string(execOut)
	if !strings.Contains(outStr, "OVERLAY_TRACER_OK") {
		t.Fatalf("output missing OVERLAY_TRACER_OK; full output:\n%s", outStr)
	}
	if !strings.Contains(outStr, "GLOBAL INSTRUCTIONS v1") {
		t.Fatalf("output missing lower-dir content (GLOBAL INSTRUCTIONS v1); full output:\n%s", outStr)
	}

	// --- 6. Remove sandbox, then assert host curated dir is byte-identical to BEFORE. ---
	rmOut, rmErr := overlayCmd(binary, "rm", handle).CombinedOutput()
	if rmErr != nil {
		t.Logf("rm (pre-host-check): %v\n%s", rmErr, rmOut)
	}

	afterSums, err := sha256Dir(curatedDir)
	if err != nil {
		t.Fatalf("sha256Dir after: %v", err)
	}

	// Compare before vs after: every file must match exactly.
	var hostDirMutated bool
	for rel, before := range beforeSums {
		after, ok := afterSums[rel]
		if !ok {
			t.Errorf("host file deleted by sandbox run: %s", rel)
			hostDirMutated = true
		} else if before != after {
			t.Errorf("host file mutated by sandbox run: %s (before=%s after=%s)", rel, before, after)
			hostDirMutated = true
		}
	}
	for rel := range afterSums {
		if _, ok := beforeSums[rel]; !ok {
			t.Errorf("host file created by sandbox run: %s", rel)
			hostDirMutated = true
		}
	}
	if hostDirMutated {
		t.Fatal("host curated dir was mutated — overlay copy-up leaked to the host mount (virtiofs ro boundary broken)")
	}
	t.Log("host-unchanged assertion PASSED: curated dir is byte-identical before and after")
}
