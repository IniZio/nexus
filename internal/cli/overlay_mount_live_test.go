//go:build herdr_live

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

func overlayCmd(binary string, args ...string) *exec.Cmd {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "HOME" || k == "XDG_CONFIG_HOME" || k == "XDG_STATE_HOME" {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "HOME="+herdrLiveIsolatedHome)
	out = append(out, "XDG_STATE_HOME="+filepath.Join(herdrLiveIsolatedHome, ".local", "state"))
	cmd := exec.Command(binary, args...)
	cmd.Env = out
	return cmd
}

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
	if os.Getenv("NEXUS_LIVE_REQUIRED") == "" {
		t.Skip("set NEXUS_LIVE_REQUIRED=1 to run live tests (requires KVM + built images)")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		liveSkip(t, "overlay: /dev/kvm not available: %v", err)
	}
	if os.Getenv("NEXUS_KERNEL_PATH") == "" {
		liveSkip(t, "overlay: NEXUS_KERNEL_PATH is not set; set it to a vmlinux image to run this test")
	}

	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus-overlay")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		liveSkip(t, "overlay: nexus binary cannot be built: %v\n%s", err, out)
	}

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
	image := os.Getenv("NEXUS_OVERLAY_IMAGE")
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
			t.Logf("cleanup: nexus rm %s: %v\n%s", handle, rmErr, rmOut)
		} else {
			t.Logf("cleanup: nexus rm %s: %s", handle, rmOut)
		}
	})

	createOut, err := overlayCmd(binary, "create", handle,
		"--image", image,
		"--mount", curatedDir+":"+guestMount+":ro",
	).CombinedOutput()
	if err != nil {
		s := string(createOut)
		if strings.Contains(s, "DENIED") || strings.Contains(s, "pull OCI image") {
			liveSkip(t, "overlay: base image %q not cached and registry access denied; pre-pull the image first", image)
		}
		if strings.Contains(s, "below the") && strings.Contains(s, "floor") {
			liveSkip(t, "overlay: insufficient free disk space to create sandbox: %s", s[strings.LastIndex(s, "error:"):])
		}
		t.Fatalf("nexus create: %v\n%s\n(check NEXUS_KERNEL_PATH and that %q is a cached image)", err, createOut, image)
	}
	t.Logf("nexus create: %s", createOut)

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
	t.Logf("nexus exec output:\n%s", execOut)
	if err != nil {
		t.Fatalf("nexus exec script failed: %v\n%s", err, execOut)
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
