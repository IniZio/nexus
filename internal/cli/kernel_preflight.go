package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveKernelPath returns the path to the guest kernel, searching known
// locations and auto-downloading the pinned release if none is found locally.
// It must be called before any expensive work so that a misconfiguration is
// caught immediately with a legible error.
//
// Search order:
//  1. NEXUS_KERNEL_PATH environment variable (always used if set; file must exist).
//  2. <binary-dir>/images/kernel/vmlinux-x86_64  (installed binary layout).
//  3. $XDG_DATA_HOME/nexus/images/kernel/vmlinux-x86_64 (populated by
//     `nexus kernel install` or a prior auto-fetch).
//  4. <cwd>/images/kernel/vmlinux-x86_64 ("go run ./cmd/nexus" from repo root).
//  5. Auto-download: fetches the pinned release to the XDG path with an INFO
//     log, then continues. Offline or missing pin → error naming
//     NEXUS_KERNEL_PATH and `nexus kernel install`.
func resolveKernelPath() (string, error) {
	return resolveKernelPathWithClient(context.Background(), nil)
}

// resolveKernelPathWithClient is the testable implementation; client nil → default.
func resolveKernelPathWithClient(ctx context.Context, client *http.Client) (string, error) {
	if k := os.Getenv("NEXUS_KERNEL_PATH"); k != "" {
		if _, err := os.Stat(k); err != nil {
			return "", fmt.Errorf(
				"kernel not found: NEXUS_KERNEL_PATH=%q: no such file\n"+
					"  Correct the path or unset NEXUS_KERNEL_PATH to use the binary-relative default",
				k)
		}
		return k, nil
	}

	var searched []string

	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "images", "kernel", "vmlinux-x86_64")
		searched = append(searched, p)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	if p := xdgKernelPath(); p != "" {
		searched = append(searched, p)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		p := filepath.Join(cwd, "images", "kernel", "vmlinux-x86_64")
		searched = append(searched, p)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	dest, fetchErr := autoFetchKernelXDG(ctx, client)
	if fetchErr != nil {
		return "", fmt.Errorf(
			"kernel not found and auto-download failed: %w\n"+
				"  set NEXUS_KERNEL_PATH or run `nexus kernel install`\n"+
				"  searched:\n    %s",
			fetchErr, strings.Join(searched, "\n    "))
	}
	return dest, nil
}

// xdgKernelPath returns the XDG data-dir kernel candidate, or "" when no home
// directory can be resolved.
func xdgKernelPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "nexus", "images", "kernel", "vmlinux-x86_64")
}

// resolveVirtiofsdPath returns the absolute path to the virtiofsd 1.x binary.
//
// Search order:
//  1. NEXUS_VIRTIOFSD_PATH environment variable (always used if set; file must exist).
//  2. exec.LookPath("virtiofsd") — honours the caller's PATH.
//  3. Conventional system install locations: /usr/lib/virtiofsd, /usr/libexec/virtiofsd,
//     /usr/local/bin/virtiofsd.
//
// Returns an error (naming NEXUS_VIRTIOFSD_PATH) only when virtiofsd is not found.
// Called only when live mounts are configured; hosts without virtiofsd and without
// --mount are unaffected.
func resolveVirtiofsdPath() (string, error) {
	if v := os.Getenv("NEXUS_VIRTIOFSD_PATH"); v != "" {
		if _, err := os.Stat(v); err != nil {
			return "", fmt.Errorf(
				"virtiofsd not found: NEXUS_VIRTIOFSD_PATH=%q: no such file\n"+
					"  Correct the path or unset NEXUS_VIRTIOFSD_PATH to use PATH-based resolution",
				v)
		}
		return v, nil
	}

	// PATH lookup: honours any user-installed virtiofsd.
	if p, err := exec.LookPath("virtiofsd"); err == nil {
		return p, nil
	}

	// Conventional system install locations (Debian/Ubuntu/Fedora/RHEL).
	for _, p := range []string{
		"/usr/lib/virtiofsd",
		"/usr/libexec/virtiofsd",
		"/usr/local/bin/virtiofsd",
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf(
		"virtiofsd not found: set NEXUS_VIRTIOFSD_PATH to the virtiofsd binary path\n" +
			"  See https://gitlab.com/virtio-fs/virtiofsd for installation instructions\n" +
			"  virtiofsd is required when --mount is used; sandboxes without --mount are unaffected")
}

// AC4 — enforceability of "all new creation paths must call resolveKernelPath":
//
// There is no compile-time mechanism in Go that enforces this invariant.
//
// Current callers of resolveKernelPath (all entry points now covered):
//   - runSandboxCreate (cmd_sandbox.go) — fixed in B3
//   - mcpService.CreateAndBoot (cmd_mcp.go) — fixed in B6
//   - herdrPluginCreate (cmd_herdr_plugin.go) — fixed in B6
//   - herdrPluginLaunch (cmd_herdr_plugin.go) — fixed in B6
//   - orcaCreate (cmd_orca.go) — fixed in B6
//   - runAllChecks (substrate.go) — fixed in B6 AC3 followup; covers
//     SelectSubstrate → cmd_recover.go and cmd_sandbox.go start paths
//
// kernelPathFor() (the error-swallowing wrapper in cmd_sandbox.go) now has
// zero external callers — all sites that previously used it now call
// resolveKernelPath directly. kernelPathFor() is retained for any future
// callers that need a best-effort path for printing/logging only.
//
// What would be needed to enforce the invariant for new paths:
//   - An integration/e2e test that invokes each entry point with a missing
//     kernel and asserts the error is returned before any filesystem side
//     effect (workspace capture, disk creation, VM launch). Such a test would
//     detect a missing preflight on a new path. Recommended as follow-up.
//   - A go vet / staticcheck custom analyser that flags calls to
//     service.CreateAndBoot or svc.Create inside the cli package that are not
//     preceded by a call to resolveKernelPath in the same function.
//
// Current mitigation: the comment on kernelPathFor() in cmd_sandbox.go and
// the doc on this function both direct new callers to resolveKernelPath. The
// tests in kernel_preflight_test.go and substrate_test.go cover the patched
// paths and serve as regression anchors — if a caller regresses to
// kernelPathFor(), the ordering tests will catch it.
