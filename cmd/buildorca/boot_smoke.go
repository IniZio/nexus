// boot_smoke.go — smoke-boot the nexus-orca:latest image.
// Uses the service layer (same as the CLI) but with a 60-second ReachabilityTimeout
// and the full netns re-exec wired in via main()'s NetnsRunEnv check.
//
// Run: NEXUS_KERNEL_PATH=$HOME/.pi/nexus-bin/vmlinux.bin
//
//	TMPDIR=/tmp go run ./cmd/buildorca/ -smoke
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

func smokeboot() error {
	kernelPath := os.Getenv("NEXUS_KERNEL_PATH")
	if kernelPath == "" {
		exe, _ := os.Executable()
		kernelPath = filepath.Join(filepath.Dir(exe), "images", "kernel", "vmlinux-x86_64")
	}
	if _, err := os.Stat(kernelPath); err != nil {
		return fmt.Errorf("kernel not found at %s (set NEXUS_KERNEL_PATH)", kernelPath)
	}
	fmt.Fprintf(os.Stderr, "smokeboot: kernel=%s\n", kernelPath)

	root, err := store.DefaultRoot()
	if err != nil {
		return fmt.Errorf("store root: %w", err)
	}
	cacheRoot := filepath.Join(root, "images")
	fmt.Fprintf(os.Stderr, "smokeboot: cacheRoot=%s\n", cacheRoot)

	c, err := image.NewCache(cacheRoot)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	ctx := context.Background()

	// Verify orca image present
	imgs, err := c.List(ctx)
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	var found bool
	for _, img := range imgs {
		if img.Ref == "nexus-orca:latest" {
			fmt.Fprintf(os.Stderr, "smokeboot: found nexus-orca:latest digest=%s size=%d\n", img.Digest, img.Size)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("nexus-orca:latest not in cache")
	}

	// Store, socket dir, disk dir all in /tmp
	storeRoot, err := os.MkdirTemp("/tmp", "orca-smoke-store-")
	if err != nil {
		return fmt.Errorf("MkdirTemp store: %w", err)
	}
	defer os.RemoveAll(storeRoot)

	diskDir, err := os.MkdirTemp("/tmp", "orca-smoke-disk-")
	if err != nil {
		return fmt.Errorf("MkdirTemp disk: %w", err)
	}
	defer os.RemoveAll(diskDir)

	socketDir, err := os.MkdirTemp("/tmp", "orca-smoke-sock-")
	if err != nil {
		return fmt.Errorf("MkdirTemp sock: %w", err)
	}
	defer os.RemoveAll(socketDir)

	chBin, err := exec.LookPath("cloud-hypervisor")
	if err != nil {
		return fmt.Errorf("cloud-hypervisor not in PATH: %w", err)
	}

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		return fmt.Errorf("NewFileStore: %w", err)
	}

	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
	})
	if err != nil {
		return fmt.Errorf("cloudhypervisor.New svcDrv: %w", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	serialLog := "/tmp/orca-serial.log"
	os.Remove(serialLog)

	newDriver := service.DriverFactory(func(ext4Path string, _ []service.ExtraDisk) (driver.Driver, error) {
		return cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    ext4Path,
			SerialOutputPath: serialLog,
		})
	})

	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		gd, ok := drv.(driver.GuestDialer)
		if !ok {
			return nil
		}
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			conn, err := gd.DialGuest(dialCtx, id, driver.AgentControlPort)
			cancel()
			if err == nil {
				conn.Close()
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
	})

	fmt.Fprintln(os.Stderr, "smokeboot: booting sandbox (60s reachability timeout) …")
	bootCtx, bootCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer bootCancel()

	sb, err := service.CreateAndBoot(
		bootCtx, svc, c, newDriver, probe,
		"smoketest", "orca-smoke",
		service.CreateAndBootOptions{
			RemoveOnExit:        true,
			CacheRoot:           cacheRoot,
			DiskDir:             diskDir,
			ReachabilityTimeout: 120 * time.Second,
			Image: service.ImageSpec{
				Ref: "nexus-orca:latest",
			},
		},
	)
	if err != nil {
		// Print serial log to help diagnose
		fmt.Fprintln(os.Stderr, "\n=== SERIAL LOG ===")
		if data, rerr := os.ReadFile(serialLog); rerr == nil {
			os.Stderr.Write(data)
		} else {
			fmt.Fprintf(os.Stderr, "(could not read %s: %v)\n", serialLog, rerr)
		}
		fmt.Fprintln(os.Stderr, "=== END SERIAL LOG ===")
		return fmt.Errorf("CreateAndBoot: %w", err)
	}
	fmt.Fprintf(os.Stderr, "smokeboot: sandbox %s BOOTED!\n", sb.ID)
	defer func() {
		rmCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_ = svc.Remove(rmCtx, sb.ID.String())
	}()

	// Run in-guest checks via agent client
	gd, ok := interface{}(svcDrv).(driver.GuestDialer)
	if !ok {
		return fmt.Errorf("svcDrv does not implement GuestDialer")
	}
	agentClient := agent.NewClient(gd, sb.ID)

	// PATH needed because nexus-agent runs as PID 1 (init) and the kernel
	// does not set PATH; the guest environment is empty at boot.
	guestEnv := map[string]string{
		"PATH": "/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin",
	}

	checks := []struct {
		label string
		cmd   []string
	}{
		{"command -v git", []string{"/bin/sh", "-c", "command -v git"}},
		{"command -v sshd", []string{"/bin/sh", "-c", "command -v sshd || ls -la /usr/sbin/sshd"}},
		{"command -v claude", []string{"/bin/sh", "-c", "command -v claude"}},
		{"PID1 cmdline", []string{"/bin/sh", "-c", "cat /proc/1/cmdline | tr '\\0' ' '"}},
		{"nexus-agent banner", []string{"/bin/sh", "-c", "dmesg 2>/dev/null | grep -i 'nexus-agent' | head -5 || echo 'no dmesg nexus-agent'"}},
	}

	allPassed := true
	for _, chk := range checks {
		execCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var outBuf strings.Builder
		_, execErr := agentClient.Exec(execCtx, agent.ExecOptions{
			Argv:   chk.cmd,
			Env:    guestEnv,
			Stdout: &outBuf,
			Stderr: &outBuf,
		})
		cancel()
		output := strings.TrimSpace(outBuf.String())
		if execErr != nil {
			fmt.Printf("CHECK [%s]: ERROR: %v\n", chk.label, execErr)
			allPassed = false
		} else {
			fmt.Printf("CHECK [%s]: %q\n", chk.label, output)
		}
	}

	if !allPassed {
		return fmt.Errorf("one or more checks failed")
	}
	fmt.Println("SMOKE_PASS: all in-guest checks succeeded")
	return nil
}

// dialWithTimeout wraps DialGuest with a timeout.
func dialWithTimeout(gd driver.GuestDialer, id domain.SandboxID, port uint32, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := gd.DialGuest(ctx, id, port)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
