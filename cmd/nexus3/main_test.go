//go:build linux

package main

// TestMain_Argv0GuestShellDispatch is the CRITICAL 2 end-to-end delivery test.
//
// It builds the nexus3 binary, hard-links it as "nexus3-guest-shell", and runs
// it using the same Args/Env pattern as herdrInstallProbeCmd. A correctly wired
// dispatch fires RunHerdrGuestShell, sees NEXUS3_HOST_SHELL=1 and SHELL=/bin/true,
// and exec-replaces itself with /bin/true (exit 0).
//
// Mutation proof: change `== "nexus3-guest-shell"` to `== "nexus3-guest-shell-MUTANT"`
// in main.go → dispatch never fires → the CLI parser runs → non-zero exit → RED.
// This mutation compiles cleanly and the whole unit suite stays green, confirming
// the delivery layer was previously untested.
//
// Lives in package main (same package as the file under test) so it is compiled
// with the binary and avoids import-cycle issues.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMain_Argv0GuestShellDispatch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("argv[0] dispatch test requires Linux (hard-link + exec semantics)")
	}

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "nexus3")

	// Build the binary. This exercises the full link step so the test covers
	// the exact binary that runs in production, not a surrogate.
	build := exec.Command("go", "build", "-o", binPath, "github.com/IniZio/nexus3/cmd/nexus3")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// Hard-link as nexus3-guest-shell. The hard link is what main.go checks
	// via filepath.Base(os.Args[0]) == "nexus3-guest-shell".
	linkPath := filepath.Join(tmpDir, "nexus3-guest-shell")
	if err := os.Link(binPath, linkPath); err != nil {
		t.Fatalf("hard link: %v", err)
	}

	// Run with the same probe pattern as herdrInstallProbeCmd:
	//   argv[0] = "nexus3-guest-shell"   → triggers argv[0] dispatch in main.go
	//   NEXUS3_HOST_SHELL=1              → escape hatch in herdrDefaultShellCore
	//   SHELL=/bin/true                  → exec-replaces with /bin/true (exit 0)
	//
	// If dispatch fires:  /bin/true replaces the process → exit 0.
	// If dispatch misses: CLI parser runs, unknown command/flags → exit non-zero.
	cmd := &exec.Cmd{
		Path: linkPath,
		Args: []string{"nexus3-guest-shell"},
		Env:  append(os.Environ(), "NEXUS3_HOST_SHELL=1", "SHELL=/bin/true"),
	}
	if err := cmd.Run(); err != nil {
		t.Errorf("argv[0] dispatch via nexus3-guest-shell exited non-zero: %v\n"+
			"  Want: dispatch fires → RunHerdrGuestShell → NEXUS3_HOST_SHELL → /bin/true exits 0\n"+
			"  Got:  dispatch likely missed (main.go string mismatch) or RunHerdrGuestShell panicked",
			err)
	}
}
