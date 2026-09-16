package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

func TestMain(m *testing.M) {
	initHerdrLiveEnv()
	if proc1comm, err := os.ReadFile("/proc/1/comm"); err == nil {
		if strings.TrimSpace(string(proc1comm)) == "nexus3-agent" {
			fmt.Fprintln(os.Stderr, "cli: skipping tests — running inside nexus3 guest VM (host-side package)")
			os.Exit(0)
		}
	}
	stateRoot, err := os.MkdirTemp("", "nexus3-cli-state-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli: create temp state root: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_STATE_HOME", stateRoot); err != nil {
		fmt.Fprintf(os.Stderr, "cli: set XDG_STATE_HOME: %v\n", err)
		os.Exit(1)
	}

	herdrSkipInstallProbeForTest = true

	kernelDir, err2 := os.MkdirTemp("", "nexus3-cli-kernel-")
	if err2 != nil {
		fmt.Fprintf(os.Stderr, "cli: create temp kernel dir: %v\n", err2)
		os.Exit(1)
	}
	kernelFile := filepath.Join(kernelDir, "vmlinux")
	if err2 = os.WriteFile(kernelFile, []byte("fake"), 0o600); err2 != nil {
		fmt.Fprintf(os.Stderr, "cli: write fake kernel: %v\n", err2)
		os.Exit(1)
	}
	if err2 = os.Setenv("NEXUS3_KERNEL_PATH", kernelFile); err2 != nil {
		fmt.Fprintf(os.Stderr, "cli: set NEXUS3_KERNEL_PATH: %v\n", err2)
		os.Exit(1)
	}

	herdrBaseImageListFn = func(_ context.Context) ([]domain.Image, error) {
		return []domain.Image{{Ref: herdrDefaultImage, Size: 1}}, nil
	}
	herdrBaseImageRegistryReachableFn = func(_ string) error { return nil }

	code := m.Run()
	_ = os.RemoveAll(stateRoot)
	_ = os.RemoveAll(kernelDir)
	herdrLiveCleanup()
	os.Exit(code)
}
