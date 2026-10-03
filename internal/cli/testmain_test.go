package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/vmcfg"
)

func TestMain(m *testing.M) {
	initHerdrLiveEnv()
	// Pin a small host so default-ceiling assertions (4096 MiB / 4 vCPU floors)
	// do not depend on the machine running the tests.
	vmcfg.HostCapacityFunc = func() vmcfg.HostCapacity { return vmcfg.HostCapacity{NCPU: 2, RAMMiB: 4096} }
	if proc1comm, err := os.ReadFile("/proc/1/comm"); err == nil {
		if strings.TrimSpace(string(proc1comm)) == "nexus-agent" {
			fmt.Fprintln(os.Stderr, "cli: skipping tests — running inside nexus guest VM (host-side package)")
			os.Exit(0)
		}
	}
	stateRoot, err := os.MkdirTemp("", "nexus-cli-state-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli: create temp state root: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_STATE_HOME", stateRoot); err != nil {
		fmt.Fprintf(os.Stderr, "cli: set XDG_STATE_HOME: %v\n", err)
		os.Exit(1)
	}

	herdrSkipInstallProbeForTest = true

	kernelDir, err2 := os.MkdirTemp("", "nexus-cli-kernel-")
	if err2 != nil {
		fmt.Fprintf(os.Stderr, "cli: create temp kernel dir: %v\n", err2)
		os.Exit(1)
	}
	kernelFile := filepath.Join(kernelDir, "vmlinux")
	if err2 = os.WriteFile(kernelFile, []byte("fake"), 0o600); err2 != nil {
		fmt.Fprintf(os.Stderr, "cli: write fake kernel: %v\n", err2)
		os.Exit(1)
	}
	if err2 = os.Setenv("NEXUS_KERNEL_PATH", kernelFile); err2 != nil {
		fmt.Fprintf(os.Stderr, "cli: set NEXUS_KERNEL_PATH: %v\n", err2)
		os.Exit(1)
	}

	herdrBaseImageListFn = func(_ context.Context) ([]domain.Image, error) {
		return []domain.Image{{Ref: herdrDefaultImage, Size: 1}}, nil
	}
	herdrBaseImageRegistryReachableFn = func(_ string) error { return nil }

	herdrWtSpawnDetachedReapFn = func(HerdrSpaceBinding) error {
		fmt.Fprintln(os.Stderr, "cli: test reached real herdrWtSpawnDetachedReapFn; stub it (fork-bomb guard)")
		os.Exit(3)
		return nil
	}

	code := m.Run()
	_ = os.RemoveAll(stateRoot)
	_ = os.RemoveAll(kernelDir)
	herdrLiveCleanup()
	os.Exit(code)
}
