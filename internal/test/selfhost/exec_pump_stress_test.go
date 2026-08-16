//go:build integration

// Package selfhost — REPRO harness for the S-NESTED-BUILD "read frame: EOF"
// transport failure (agent: pump: read frame: EOF) during a long, nearly-silent
// in-guest exec.
//
// This is NOT a permanent test. It isolates the failing mechanism from the full
// ~3-min nested pipeline onto a SINGLE non-nested outer guest so we can observe,
// with instrumentation, WHY the vsock frame stream ends mid-exec:
//
//	H1  guest OOM kills PID-1 agent → kernel panic → vsock dies (serial shows panic)
//	H2  host OOM kills the cloud-hypervisor VMM (or its netns/pump child) →
//	    vsock unix socket closes → host reads EOF (guest serial clean)
//	H3  a wire/framing/backpressure robustness limit in exec.go/wire (idle pump)
//
// Two staged Execs on one boot give the deciding measurement:
//
//	Stage 1 (faithful): format /dev/vdb, cp the ~1.2 GB many-small-files Go module
//	        cache into it — the literal step-6 pattern (cp -a of many small files
//	        to the /dev/vdb scratch disk). Silent, high-file-count, disk-bound.
//	Stage 2 (host-pressure probe): force the 8 GiB guest to actually CONSUME its
//	        RAM (tmpfs writes → host anon), pushing the host toward OOM. If the
//	        host OOM-kills the VMM, THIS exec returns EOF — reproducing the exact
//	        transport error via a minimal mechanism and confirming H2.
//
// A host goroutine samples host /proc/meminfo and the live cloud-hypervisor
// process count throughout, so a VMM death is directly observed.
package selfhost

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// hostMemSample reads a few key host /proc/meminfo fields (MiB).
func hostMemAvailableMiB() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemAvailable:") {
			var kb int64
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				fmt.Sscanf(fields[1], "%d", &kb)
			}
			return kb / 1024
		}
	}
	return -1
}

func liveCHCount() int {
	// pgrep -fc uses full command-line match; -c alone matches comm names which
	// are truncated to 15 chars on Linux — "cloud-hypervisor" (16 chars) never
	// matched, causing liveCHCount to always return 0.
	out, err := exec.Command("pgrep", "-fc", "cloud-hypervisor").Output()
	if err != nil {
		// pgrep exits 1 when no processes match; treat as zero, not an error.
		return 0
	}
	var n int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

// TestExecPumpStressRepro reproduces the read-frame-EOF transport failure by
// running a long, nearly-silent, resource-heavy exec on a single outer guest.
func TestExecPumpStressRepro(t *testing.T) {
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	t.Log("building selfhost base image (cached: fast) ...")
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer imgCancel()
	img, err := BuildSelfHostBaseImage(imgCtx, cache)
	if err != nil {
		if err == ErrDockerUnavailable {
			t.Skip("docker unavailable")
		}
		t.Fatalf("BuildSelfHostBaseImage: %v", err)
	}
	t.Logf("base image: digest=%s size=%.2f GiB", img.Digest, float64(img.Size)/(1<<30))

	socketDir, err := os.MkdirTemp("/tmp", "exec-pump-stress-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	serialPath := filepath.Join(socketDir, "outer-serial.log")
	t.Cleanup(func() {
		if content, rerr := os.ReadFile(serialPath); rerr == nil && len(content) > 0 {
			t.Logf("=== outer guest serial (tail 4KiB) ===\n%s", tail(content, 4096))
		}
		os.RemoveAll(socketDir) //nolint:errcheck
	})

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}
	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin, SocketDir: socketDir, KernelPath: kernelPath,
		StartTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New(svc): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(ext4Path string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		var chExtra []cloudhypervisor.ExtraDisk
		for _, ed := range extraDisks {
			chExtra = append(chExtra, cloudhypervisor.ExtraDisk{Path: ed.Path})
		}
		var ferr error
		bootDrv, ferr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    ext4Path,
			MemoryMiB:        8192, // faithful to S-NESTED-BUILD outer guest
			VCPUs:            6,
			SerialOutputPath: serialPath,
			StartTimeout:     90 * time.Second,
			ExtraDisks:       chExtra,
		})
		return bootDrv, ferr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	// Sparse 20 GiB scratch disk → /dev/vdb (faithful).
	scratchDiskPath := filepath.Join(socketDir, "buildkit-scratch.raw")
	if err := func() error {
		f, err := os.Create(scratchDiskPath)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Truncate(20 * 1024 * 1024 * 1024)
	}(); err != nil {
		t.Fatalf("create scratch disk: %v", err)
	}

	t.Logf("host MemAvailable at boot: %d MiB; live CH procs: %d", hostMemAvailableMiB(), liveCHCount())

	bootCtx, bootCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer bootCancel()

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID == (domain.SandboxID{}) {
			return
		}
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = svc.Remove(rmCtx, sandboxID.String())
	})

	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"exec-pump-stress", fmt.Sprintf("exec-pump-stress-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 90 * time.Second,
			ExtraDisks:          []service.ExtraDisk{{Path: scratchDiskPath}},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("outer sandbox booted: %s", sb.ID)

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// ── Host monitor goroutine ────────────────────────────────────────────────
	var minAvail atomic.Int64
	minAvail.Store(1 << 40)
	var vmmDied atomic.Bool
	stopMon := make(chan struct{})
	monDone := make(chan struct{})
	go func() {
		defer close(monDone)
		tk := time.NewTicker(1500 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stopMon:
				return
			case <-tk.C:
				a := hostMemAvailableMiB()
				ch := liveCHCount()
				if a >= 0 && a < minAvail.Load() {
					minAvail.Store(a)
				}
				if ch == 0 {
					vmmDied.Store(true)
				}
				t.Logf("[hostmon] MemAvailable=%d MiB  liveCH=%d", a, ch)
			}
		}
	}()

	runExec := func(label, script string, timeout time.Duration) (int32, error, string) {
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		code, xerr := agentClient.Exec(ctx, agent.ExecOptions{
			Argv: []string{"/bin/sh", "-c", script},
			Env: map[string]string{
				"PATH": "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				"HOME": "/root",
			},
			Stdout: &out,
			Stderr: &out,
		})
		t.Logf("=== %s output (%d bytes) ===\n%s", label, out.Len(), out.String())
		return code, xerr, out.String()
	}

	// ── Stage 1: faithful many-files cp of the module cache to /dev/vdb ────────
	stage1 := `set -u
echo STAGE1_START
grep -E 'MemTotal|MemFree|MemAvailable' /proc/meminfo
echo "vdb: formatting ext4"
mkfs.ext4 -F -q /dev/vdb 2>&1 | tail -2 || echo "mkfs.ext4 failed/absent"
mkdir -p /mnt/vdb
mount /dev/vdb /mnt/vdb 2>&1 || echo "mount failed"
echo "src size:"; du -sh /usr/local/gopath/pkg/mod 2>/dev/null | cut -f1
echo "src file count:"; find /usr/local/gopath/pkg/mod -type f 2>/dev/null | wc -l
echo "cp -a many small files → /dev/vdb ..."
cp -a /usr/local/gopath/pkg/mod/. /mnt/vdb/ && echo STAGE1_CP_DONE || echo STAGE1_CP_FAIL
grep -E 'MemFree|MemAvailable' /proc/meminfo
du -sh /mnt/vdb 2>/dev/null | cut -f1
echo STAGE1_END`
	c1, e1, o1 := runExec("stage1-cp", stage1, 20*time.Minute)
	t.Logf("STAGE1 result: exit=%d err=%v host-min-avail=%d MiB vmmDied=%v",
		c1, e1, minAvail.Load(), vmmDied.Load())
	stage1EOF := e1 != nil
	stage1Done := strings.Contains(o1, "STAGE1_END")

	// ── Stage 2: host-anon-pressure probe (only if stage 1 survived) ──────────
	var c2 int32
	var e2 error
	var o2 string
	if !stage1EOF && stage1Done {
		// Force the guest to actually consume its 8 GiB RAM via tmpfs writes,
		// which the host must back with anon pages. Grow in 512 MiB steps up to
		// ~7 GiB, logging guest MemAvailable so we see the guest itself is fine
		// while the HOST is squeezed. If the host OOM-kills the VMM, this exec
		// returns EOF — the exact transport error, reproduced minimally.
		stage2 := `set -u
echo STAGE2_START
mkdir -p /mnt/ram
mount -t tmpfs -o size=8g tmpfs /mnt/ram 2>&1 || echo "tmpfs mount failed"
i=0
while [ $i -lt 14 ]; do
  dd if=/dev/zero of=/mnt/ram/blob.$i bs=1M count=512 2>/dev/null || { echo "dd failed at $i"; break; }
  ga=$(grep MemAvailable /proc/meminfo | awk '{print $2}')
  echo "anon +512MiB step=$i guestMemAvailKB=$ga"
  sync
  i=$((i+1))
done
echo STAGE2_END`
		c2, e2, o2 = runExec("stage2-anon", stage2, 10*time.Minute)
		t.Logf("STAGE2 result: exit=%d err=%v host-min-avail=%d MiB vmmDied=%v",
			c2, e2, minAvail.Load(), vmmDied.Load())
		_ = o2
	}

	close(stopMon)
	<-monDone

	// ── Verdict ───────────────────────────────────────────────────────────────
	stage2EOF := e2 != nil
	t.Logf("VERDICT: stage1EOF=%v stage2EOF=%v vmmDied=%v host-min-MemAvailable=%d MiB",
		stage1EOF, stage2EOF, vmmDied.Load(), minAvail.Load())

	if stage1EOF {
		t.Logf("REPRODUCED at stage 1 (faithful cp). err=%v", e1)
	}
	if stage2EOF {
		t.Logf("REPRODUCED at stage 2 (host-anon pressure). err=%v", e2)
	}
	if !stage1EOF && !stage2EOF {
		t.Logf("NOT reproduced on this host/config; host never fell below %d MiB avail", minAvail.Load())
	}
	// This repro harness never fails the build; it only reports observations.
}

func tail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return "..." + string(b[len(b)-n:])
}
