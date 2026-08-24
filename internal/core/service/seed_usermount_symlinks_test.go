package service_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// captureExecer captures the script passed to /bin/sh -c for inspection.
func captureExecer(out *string) service.GuestExecer {
	return func(_ context.Context, _ domain.SandboxID, argv []string, _ io.Reader) (int32, error) {
		if len(argv) == 3 && argv[0] == "/bin/sh" && argv[1] == "-c" {
			*out = argv[2]
		}
		return 0, nil
	}
}

// TestSeedGuestUserMounts_SymlinkStepEmitted verifies that the emitted script
// contains the guarded ln -s invocation for each Symlinks entry.
//
// Mutation guard: delete the symlink-emit block from SeedGuestUserMounts → test RED.
func TestSeedGuestUserMounts_SymlinkStepEmitted(t *testing.T) {
	manifest := service.UserMountManifest{
		HostHome: "$HOME",
		Mounts: []service.ResolvedUserMount{
			{
				HostPath:         "$HOME/.local/share/groundwork",
				GuestPath:        "/root/.local/share/groundwork",
				Overlay:          false,
				StagingGuestPath: "/root/.local/share/groundwork",
			},
		},
		Symlinks: []service.ResolvedUserSymlink{
			{
				Link:   "/root/.config/opencode/plugins/groundwork",
				Target: "/root/.local/share/groundwork",
			},
		},
	}

	var script string
	err := service.SeedGuestUserMounts(context.Background(), domain.SandboxID{}, manifest, captureExecer(&script))
	if err != nil {
		t.Fatalf("SeedGuestUserMounts: unexpected error: %v", err)
	}
	if script == "" {
		t.Fatal("execer was not called — SeedGuestUserMounts returned early without running the script")
	}

	wantLn := "ln -s '/root/.local/share/groundwork' '/root/.config/opencode/plugins/groundwork'"
	if !strings.Contains(script, wantLn) {
		t.Errorf("script missing symlink creation line\nwant substring: %q\ngot script:\n%s", wantLn, script)
	}

	wantGuard := "if [ ! -e '/root/.config/opencode/plugins/groundwork' ] && [ ! -L '/root/.config/opencode/plugins/groundwork' ]"
	if !strings.Contains(script, wantGuard) {
		t.Errorf("script missing idempotent guard\nwant substring: %q\ngot script:\n%s", wantGuard, script)
	}

	wantMkdir := "mkdir -p \"$(dirname '/root/.config/opencode/plugins/groundwork')\""
	if !strings.Contains(script, wantMkdir) {
		t.Errorf("script missing parent mkdir\nwant substring: %q\ngot script:\n%s", wantMkdir, script)
	}
}

// TestSeedGuestUserMounts_SymlinksOnlyNotNoop verifies that a manifest with
// Symlinks but no Mounts still triggers the seed (no-op guard fix).
//
// Mutation guard: revert the no-op guard to `len(manifest.Mounts) == 0` alone
// → this test fails RED because the execer is never called.
func TestSeedGuestUserMounts_SymlinksOnlyNotNoop(t *testing.T) {
	manifest := service.UserMountManifest{
		HostHome: "$HOME",
		Mounts:   nil, // deliberately empty — only symlinks
		Symlinks: []service.ResolvedUserSymlink{
			{
				Link:   "/root/.config/opencode/plugins/groundwork",
				Target: "/root/.local/share/groundwork",
			},
		},
	}

	called := false
	execer := func(_ context.Context, _ domain.SandboxID, _ []string, _ io.Reader) (int32, error) {
		called = true
		return 0, nil
	}

	err := service.SeedGuestUserMounts(context.Background(), domain.SandboxID{}, manifest, execer)
	if err != nil {
		t.Fatalf("SeedGuestUserMounts: unexpected error: %v", err)
	}
	if !called {
		t.Fatal("execer was never called — SeedGuestUserMounts no-op'd on a Symlinks-only manifest; guard must be len(Mounts)==0 && len(Symlinks)==0")
	}
}
