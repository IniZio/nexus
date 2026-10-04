//go:build integration

package selfhost

// Regression: a builder VM killed mid-build (SIGKILL of its cloud-hypervisor)
// must never poison later builds. The dirty buildkit cache slot is quarantined
// and the next build starts from an empty cache and succeeds.
//
//	TMPDIR=/var/tmp go test -tags integration -run TestBuilderVMKilledMidBuild \
//	  ./internal/test/selfhost/ -v -timeout 30m

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/builder"
	"github.com/IniZio/nexus/internal/core/builder/builderimage"
	"github.com/IniZio/nexus/internal/core/image"
)

func killWorkspace(t *testing.T, containerfile string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".nexus", "Containerfile"), []byte(containerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// findBuilderCH returns the PID of the cloud-hypervisor started by the build
// labelled label (its socket dir is /tmp/g8-<label>-*), or 0.
func findBuilderCH(label string) int {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		cmd := strings.ReplaceAll(string(raw), "\x00", " ")
		if strings.Contains(cmd, "cloud-hypervisor") && strings.Contains(cmd, "/tmp/g8-"+label+"-") {
			return pid
		}
	}
	return 0
}

func TestBuilderVMKilledMidBuild_NextBuildSucceeds(t *testing.T) {
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)
	agentBin := skipUnlessAgentBinG8(t, repoRoot)
	agentBytes, err := os.ReadFile(agentBin)
	if err != nil {
		t.Fatal(err)
	}

	storeRoot := t.TempDir()
	imgCache, err := image.NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 28*time.Minute)
	defer cancel()

	builderRootfs, err := builderimage.EnsureBuilderImage(ctx, storeRoot, agentBytes)
	if err != nil {
		t.Fatalf("EnsureBuilderImage: %v", err)
	}

	const base = "FROM alpine:3.20\nRUN apk add --no-cache curl && echo base-ok\n"
	build := func(label, containerfile string) error {
		disks, leases, err := builder.SelectCacheDisks(ctx, storeRoot, []string{"buildkit"})
		if err != nil {
			t.Fatalf("SelectCacheDisks: %v", err)
		}
		defer builder.ReleaseCacheDiskLeases(leases)
		_, _, _, berr := buildOneImageE(t, ctx, chBin, kernelPath, builderRootfs, imgCache,
			storeRoot, killWorkspace(t, containerfile), disks, label)
		return berr
	}

	// 1. Clean build populates the cache slot and marks it clean.
	if err := build("killclean", base); err != nil {
		t.Fatalf("baseline build: %v", err)
	}

	// 2. Heavy build; SIGKILL its cloud-hypervisor once the guest is working.
	killed := make(chan int, 1)
	go func() {
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			if pid := findBuilderCH("killme"); pid != 0 {
				time.Sleep(25 * time.Second) // let buildkitd start writing snapshots
				if pid2 := findBuilderCH("killme"); pid2 == pid {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					killed <- pid
					return
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		killed <- 0
	}()
	heavy := base + "RUN apk add --no-cache build-base git && dd if=/dev/urandom of=/blob bs=1M count=400 && sync\n"
	if err := build("killme", heavy); err == nil {
		t.Fatal("heavy build unexpectedly succeeded; VM was not killed in time")
	}
	if pid := <-killed; pid == 0 {
		t.Fatal("builder cloud-hypervisor never found/killed")
	} else {
		t.Logf("SIGKILLed builder cloud-hypervisor pid %d", pid)
	}

	// 3. Slot must be dirty-fenced after the unclean death.
	slot := filepath.Join(storeRoot, "caches", "buildkit.ext4")
	if _, err := os.Stat(slot + ".dirty"); err != nil {
		t.Fatalf("slot not fenced dirty after SIGKILL: %v", err)
	}

	// 4. Next build must pass and the dirty slot must be quarantined.
	if err := build("killnext", base+"RUN apk add --no-cache git\n"); err != nil {
		t.Fatalf("build after unclean builder death failed: %v", err)
	}
	if q, _ := filepath.Glob(slot + ".quarantine-*"); len(q) != 1 {
		t.Fatalf("quarantined copies = %v, want exactly 1", q)
	}
}
