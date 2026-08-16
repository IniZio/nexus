package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveKernelPath returns the path to the pinned guest kernel, validating
// that the file exists. It must be called before any expensive work (workspace
// capture, shadow disk creation, builder VM) so that a misconfiguration is
// caught immediately with a legible error.
//
// Search order:
//  1. NEXUS3_KERNEL_PATH environment variable (always used if set; file must exist).
//  2. <binary-dir>/images/kernel/vmlinux-x86_64  (installed binary layout).
//  3. <cwd>/images/kernel/vmlinux-x86_64          ("go run ./cmd/nexus3" from repo root).
//
// If none resolve, the error names NEXUS3_KERNEL_PATH and lists every searched
// path so the operator can act without reading source.
//
// All sandbox-creation entry points must call this function before expensive
// work. See AC4 note at the bottom of this file for enforceability limitations.
func resolveKernelPath() (string, error) {
	// Env override is always honoured but validated: a typo in NEXUS3_KERNEL_PATH
	// is caught here rather than after an expensive workspace capture.
	if k := os.Getenv("NEXUS3_KERNEL_PATH"); k != "" {
		if _, err := os.Stat(k); err != nil {
			return "", fmt.Errorf(
				"kernel not found: NEXUS3_KERNEL_PATH=%q: no such file\n"+
					"  Correct the path or unset NEXUS3_KERNEL_PATH to use the binary-relative default",
				k)
		}
		return k, nil
	}

	var searched []string

	// Binary-relative: works when the nexus3 binary is installed alongside images/.
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "images", "kernel", "vmlinux-x86_64")
		searched = append(searched, p)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	// CWD-relative: works for "go run ./cmd/nexus3" executed from the repo root,
	// where images/kernel/vmlinux-x86_64 exists relative to the working directory.
	if cwd, err := os.Getwd(); err == nil {
		p := filepath.Join(cwd, "images", "kernel", "vmlinux-x86_64")
		searched = append(searched, p)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf(
		"kernel not found: set NEXUS3_KERNEL_PATH to the vmlinux image path\n"+
			"  searched (NEXUS3_KERNEL_PATH not set):\n    %s",
		strings.Join(searched, "\n    "))
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
