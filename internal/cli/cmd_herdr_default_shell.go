package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/store"
)

// herdrSidecarSuffix is the filename extension appended to the installed
// binary path to form the companion sidecar. A single source-of-truth constant
// prevents the writer, reader, and tests from silently diverging.
const herdrSidecarSuffix = ".nexus3bin"

// herdrExecFn is the syscall.Exec signature used by herdrDefaultShellCore.
// Replaced in tests so the test drives the real decision logic without
// actually exec-replacing the test process.
type herdrExecFn func(argv0 string, argv []string, envv []string) error

// sandboxDialer is an optional extension of sandboxGetter that can probe
// whether a guest is reachable via vsock. *service.Service implements it;
// simple test fakes that only exercise the record-fetch path do not need to.
// herdrDefaultShellCore performs a type-assertion and skips the check when
// the concrete svc does not implement this interface.
type sandboxDialer interface {
	sandboxGetter
	DialGuest(ctx context.Context, ref string, port uint32) (net.Conn, error)
}

// herdrGuestShellExecFn is the exec seam for RunHerdrGuestShell and
// herdrGuestShellFallback. Production code uses syscall.Exec; tests replace
// this to capture exec calls without replacing the test process.
var herdrGuestShellExecFn herdrExecFn = syscall.Exec

// herdrGuestShellExitFn is the os.Exit seam for RunHerdrGuestShell.
// Tests replace this to prevent the test process from exiting.
var herdrGuestShellExitFn = os.Exit

// herdrSkipInstallProbeForTest disables the install-time probe in
// runHerdrInstallDefaultShell. Set to true in tests where the installed binary
// is the test runner and lacks the argv[0] dispatch.
var herdrSkipInstallProbeForTest bool

// herdrAutoCreateTimeout is the outer deadline for the auto-create subprocess
// launched by herdrDefaultShellCore when a workspace has no binding yet.
// The subprocess itself runs "nexus3 herdr worktree-sandbox --auto <wsID>"
// which has its own 90s create timeout; 120s gives it headroom plus the 2s
// herdr list probe.
const herdrAutoCreateTimeout = 120 * time.Second

// herdrDefaultShellAutoCreateFn is the seam for the auto-create subprocess.
// Replaced in tests to prevent live herdr/nexus3 calls.
var herdrDefaultShellAutoCreateFn = herdrDefaultShellAutoCreate

// herdrWtChildRunnerFn runs the guest shell as a supervised child process for
// wt/ (auto-bound worktree) panes and waits for it to exit.  The child
// inherits stdin/stdout/stderr so it holds the controlling TTY and is the
// terminal foreground.  Replaced in tests.
var herdrWtChildRunnerFn func(ctx context.Context, nexus3Bin string, argv []string) error = herdrWtChildRunner

// herdrWtPaneListerFn returns the count of panes in workspaceID that are NOT
// ownPaneID.  Returns (0, nil) when no other panes remain.  Replaced in tests.
var herdrWtPaneListerFn func(ctx context.Context, workspaceID, ownPaneID string) (int, error) = herdrWtPaneLister

// herdrWtSandboxRemoverFn removes the sandbox by handle.  Called only when
// all panes in the workspace have closed.  Replaced in tests.
var herdrWtSandboxRemoverFn func(ctx context.Context, handle string) error = herdrWtSandboxRemover

// herdrAutoCreatePredicateFn gates auto-create attempts in herdrDefaultShellCore.
//
// Returns true only when (b) the current working directory is inside a linked
// worktree AND (c) a binding in the file carries a RepoRoot matching that
// linked worktree's main repo. Bindings with empty RepoRoot are NO MATCH.
// Both conditions must hold; false on any I/O error (FAIL-OPEN toward host shell).
//
// Replaced in tests to avoid filesystem fixtures in integration tests.
var herdrAutoCreatePredicateFn = func(allBindings []HerdrSpaceBinding) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	return herdrAutoCreatePredicateWith(cwd, allBindings, os.Stat, os.ReadFile)
}

// herdrAutoCreatePredicateWith is the injectable core of herdrAutoCreatePredicateFn.
// Accepts cwd and fs functions so unit tests can drive it with t.TempDir() fixtures.
//
// (b) Walks up from cwd, bounded to herdrGitSearchDepth levels, looking for a
// .git entry. A linked worktree's .git is a regular FILE ("gitdir: <path>");
// a main checkout's .git is a DIRECTORY. Anything else → false.
//
// (c) Only when (b) holds: the main repo path derived from the .git file must
// match the RepoRoot of at least one binding. Bindings with an empty RepoRoot
// are treated as NO MATCH (legacy bindings, or bindings from non-worktree
// flows). False on any I/O error (FAIL-OPEN toward host shell).
func herdrAutoCreatePredicateWith(
	cwd string,
	allBindings []HerdrSpaceBinding,
	statFn func(string) (os.FileInfo, error),
	readFileFn func(string) ([]byte, error),
) bool {
	// Fast-path: no bindings → no nexus3 worktree sandboxes on this machine.
	if len(allBindings) == 0 {
		return false
	}
	// (b) Walk up from cwd looking for .git.
	const herdrGitSearchDepth = 8
	dir := cwd
	for i := 0; i < herdrGitSearchDepth; i++ {
		candidate := filepath.Join(dir, ".git")
		fi, err := statFn(candidate)
		if err == nil {
			if fi.Mode().IsRegular() {
				// .git is a regular file → linked worktree. Parse the gitdir
				// target to derive the main repo root path.
				data, rerr := readFileFn(candidate)
				if rerr != nil {
					return false
				}
				mainRepo := herdrMainRepoFromGitdir(string(data))
				if mainRepo == "" {
					return false
				}
				// (c) Repo-scoped check: delegate to the single shared
				// mechanism herdrRepoHasBoundSandbox (predicate c).
				return herdrRepoHasBoundSandbox(mainRepo, allBindings)
			}
			// .git is a directory (main checkout) or something unexpected.
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}
		dir = parent
	}
	return false // no .git found within depth limit
}

// herdrMainRepoFromGitdir parses a linked worktree's .git file and returns the
// main repo root path.
//
// The file contains a single line: "gitdir: <main>/.git/worktrees/<name>"
// Returns "" if the content doesn't match the expected shape.
//
// Known limitation: newer Git can write relative gitdir: paths
// (worktree.useRelativePaths). A relative path returned here will never match
// an absolute RepoRoot, so the predicate silently returns false and falls
// through to the host shell. This is fail-open (safe) but means auto-create
// will not engage on repos configured that way.
func herdrMainRepoFromGitdir(content string) string {
	line := strings.TrimSpace(content)
	const prefix = "gitdir: "
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	target := strings.TrimSpace(line[len(prefix):])
	// Expect: <main>/.git/worktrees/<name>
	// Trim the last three path components to reach <main>.
	worktreesDir := filepath.Dir(target) // <main>/.git/worktrees
	gitDir := filepath.Dir(worktreesDir) // <main>/.git
	if filepath.Base(worktreesDir) != "worktrees" || filepath.Base(gitDir) != ".git" {
		return ""
	}
	return filepath.Dir(gitDir) // <main>
}

// herdrDefaultShellAutoCreate runs "nexus3 herdr worktree-sandbox --auto <wsID>"
// as a subprocess, waits for it to finish, then re-reads the binding store.
// Returns the binding and true on success; (zero, false) on any error so the
// caller falls through to execHostShell (FAIL-OPEN).
func herdrDefaultShellAutoCreate(ctx context.Context, storeRoot, wsID, nexus3Bin string, w io.Writer) (HerdrSpaceBinding, bool) {
	if nexus3Bin == "" {
		return HerdrSpaceBinding{}, false
	}
	fmt.Fprintf(w, "nexus3-guest-shell: linked worktree detected — auto-creating sandbox (may take ~1 min)...\n")
	autoCtx, cancel := context.WithTimeout(ctx, herdrAutoCreateTimeout)
	defer cancel()
	cmd := herdrExecCommandContext(autoCtx, nexus3Bin, "herdr", "worktree-sandbox", "--auto", wsID)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "nexus3-guest-shell: auto-create: %v\n", err)
		return HerdrSpaceBinding{}, false
	}
	b, ok, _ := herdrDefaultShellLookup(storeRoot, wsID)
	return b, ok
}

// herdrDefaultShellCore is the testable core of "nexus3 herdr default-shell".
//
// It reads HERDR_WORKSPACE_ID from getenv, resolves the binding, determines
// the guest cwd, then calls execFn to replace the current process with either
// the guest shell (inside the nexus3 sandbox) or the host shell (on any error
// path).
//
// FAIL-OPEN contract: every code path that cannot confirm a live sandbox
// binding returns execHostShell. All failure modes reach it through explicit
// early returns — any new check must call return execHostShell() explicitly.
// The guest exec is inside an explicit block near the end of the function;
// if it fails, execHostShell() is returned immediately.
func herdrDefaultShellCore(
	ctx context.Context,
	getenv func(string) string,
	storeRoot string,
	svc sandboxGetter, // nil → skip state check and cwd resolution, use /root
	nexus3Bin string, // path to the nexus3 binary for re-exec
	execFn herdrExecFn,
) error {
	// execHostShell replaces the current process with the operator's host shell.
	// Prefer $SHELL; fall back to /bin/sh. Never returns on success.
	execHostShell := func() error {
		sh := getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		return execFn(sh, []string{sh}, os.Environ())
	}

	// Escape hatch: operator forces host shell regardless of workspace binding.
	if getenv("NEXUS3_HOST_SHELL") != "" {
		return execHostShell()
	}

	// No workspace ID → not in a herdr pane or not a nexus3 space.
	wsID := getenv("HERDR_WORKSPACE_ID")
	if wsID == "" {
		return execHostShell()
	}

	// Store root unavailable → cannot locate bindings file.
	if storeRoot == "" {
		return execHostShell()
	}

	// Read all bindings from disk once. Any parse error → fall through.
	// A missing file is not an error (no bindings yet → empty slice).
	allBindings, readErr := herdrSpaceReadAll(storeRoot)
	if readErr != nil {
		return execHostShell()
	}

	// Find this workspace's binding in the full list.
	var binding HerdrSpaceBinding
	found := false
	for _, b := range allBindings {
		if b.HerdrWorkspaceID == wsID {
			binding = b
			found = true
			break
		}
	}

	if !found {
		// Gate: only engage auto-create when (b) the cwd is inside a linked
		// worktree AND (c) a binding already carries a RepoRoot matching that
		// linked worktree's main repo. Both checks are pure local filesystem
		// reads — no subprocess, no herdr call. False on any error
		// (FAIL-OPEN toward host shell).
		if !herdrAutoCreatePredicateFn(allBindings) {
			return execHostShell()
		}
		var ok bool
		binding, ok = herdrDefaultShellAutoCreateFn(ctx, storeRoot, wsID, nexus3Bin, os.Stderr)
		if !ok {
			return execHostShell()
		}
	}

	// Guard: empty nexus3Bin means the delivery mechanism could not locate the
	// nexus3 binary (e.g. sidecar missing after install). Fall back rather than
	// exec'ing an empty path into a dead pane. (CRITICAL 1)
	if nexus3Bin == "" {
		return execHostShell()
	}

	// Verify sandbox is running and dialable before the exec point of no return.
	// After syscall.Exec the process is replaced; a non-running sandbox
	// (paused/stopped/error) cannot attach, so the pane would be dead.
	//
	// CRITICAL: "State == Running" is not the condition that fails. The real
	// failure is in substrate/driver resolution inside nexus3 exec — if PATH
	// lacks the hypervisor binary (e.g. a systemd unit with a lean PATH), nexus3
	// exec returns `driver "none" does not support guest dialing` AFTER replacing
	// the process, leaving a dead pane with no shell to recover from. The dial
	// check here catches that before the point of no return.
	//
	// When svc is nil (daemon unreachable), skip both checks and use /root cwd —
	// the existing fail-open behaviour for daemon-unreachable is preserved.
	// (CRITICAL 4 + CRITICAL 5)
	cwd := "/root"
	if svc != nil {
		sb, sbErr := svc.Get(ctx, binding.SandboxHandle)
		if sbErr != nil || sb.State != domain.Running {
			return execHostShell()
		}
		// Probe actual dialability: attempt a vsock connection with a short
		// timeout. If substrate/driver resolution fails the error is immediate
		// (no I/O roundtrip); if the guest is up the connection is local and
		// fast. Only performed when svc implements sandboxDialer — real service
		// does, simple test fakes do not.
		if d, ok := svc.(sandboxDialer); ok {
			dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			conn, dialErr := d.DialGuest(dialCtx, binding.SandboxHandle, driver.AgentControlPort)
			cancel()
			if dialErr != nil {
				slog.Warn("nexus3-guest-shell: guest not dialable; falling back to host shell", "err", dialErr)
				return execHostShell()
			}
			conn.Close()
		}
		cwd = herdrShellCwdFromSandbox(sb)
	}

	// Guest exec: all checks above passed.
	//
	// Re-exec this process as "nexus3 exec --pty ..." so the new nexus3
	// process manages the PTY and the pane lifecycle is driven by the guest
	// shell's exit, not ours.
	//
	// Guest shell: nexus3 guest images carry bash; /bin/bash --login sources
	// the login profile. This matches the assumption in cmd_shell.go and
	// pane.sh's final exec leg.
	//
	// FAIL-OPEN: execFn is syscall.Exec in production. If it returns — either
	// because exec itself failed, or because a test seam replaced it — the
	// process was NOT replaced. An exec failure is logged and execHostShell is
	// returned immediately. A test seam returning nil falls through to return nil.
	argv := []string{nexus3Bin, "exec", "--pty", "--cwd", cwd, binding.SandboxHandle, "/bin/bash", "--login"}

	// Worktree-bound panes use supervised mode: the parent survives the
	// pane-close SIGHUP (sent to the process group by herdr) so it can run
	// last-pane teardown after the child exits.
	//
	// Non-worktree panes keep the original exec-replace behaviour: syscall.Exec
	// replaces this process entirely so no cleanup code ever runs.
	if binding.IsWorktreeManaged() {
		return herdrWtSupervisedShell(ctx, nexus3Bin, binding, argv)
	}

	if err := execFn(nexus3Bin, argv, os.Environ()); err != nil {
		slog.Warn("nexus3-guest-shell: exec failed; falling back to host shell", "err", err)
		return execHostShell()
	}
	// Reached only by test seams that return nil (production syscall.Exec
	// replaced the process and this line is unreachable in the live path).
	return nil
}

// herdrShellCwdFromSandbox extracts the guest cwd from an already-fetched
// domain.Sandbox, avoiding a second svc.Get call. Mirrors herdrShellCwd
// priority: first live mount, then mounted volume, then /root.
func herdrShellCwdFromSandbox(sb domain.Sandbox) string {
	for _, m := range sb.LiveMounts {
		if m.GuestPath != "" {
			return m.GuestPath
		}
	}
	for _, v := range sb.MountedVolumes {
		if v.GuestPath != "" {
			return v.GuestPath
		}
	}
	return "/root"
}

// herdrDefaultShellLookup reads the bindings file and returns the binding for
// wsID. Uses (HerdrSpaceBinding{}, false, nil) when the binding is absent;
// (HerdrSpaceBinding{}, false, err) on file or parse errors; (b, true, nil)
// when found.
func herdrDefaultShellLookup(storeRoot, wsID string) (HerdrSpaceBinding, bool, error) {
	path := filepath.Join(storeRoot, "herdr-space-bindings.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return HerdrSpaceBinding{}, false, nil // no bindings yet — not an error
	}
	if err != nil {
		return HerdrSpaceBinding{}, false, fmt.Errorf("default-shell: read bindings: %w", err)
	}
	var bindings []HerdrSpaceBinding
	if err := json.Unmarshal(data, &bindings); err != nil {
		return HerdrSpaceBinding{}, false, fmt.Errorf("default-shell: parse bindings: %w", err)
	}
	for _, b := range bindings {
		if b.HerdrWorkspaceID == wsID {
			return b, true, nil
		}
	}
	return HerdrSpaceBinding{}, false, nil
}

// RunHerdrGuestShell is the argv[0]-dispatched entry point when the nexus3
// binary is hard-linked as "nexus3-guest-shell" by
// "nexus3 herdr install-default-shell". It wraps the resolution logic with a
// top-level panic recovery (CRITICAL 3): a panic anywhere in the resolution
// path is caught here and the operator always gets a working host shell.
//
// New checks inside herdrDefaultShellCore must still call return execHostShell()
// explicitly — see that function's FAIL-OPEN contract comment.
func RunHerdrGuestShell() {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("nexus3-guest-shell: panic; falling back to host shell", "panic", r)
			sh := os.Getenv("SHELL")
			if sh == "" {
				sh = "/bin/sh"
			}
			_ = herdrGuestShellExecFn(sh, []string{sh}, os.Environ())
			herdrGuestShellExitFn(0)
			return
		}
	}()

	ctx := context.Background()
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		storeRoot = ""
	}

	// Read the sidecar for the real nexus3 binary path and the stamped kernel
	// path. CRITICAL 1: the hard link installs into ~/.local/bin which has no
	// images/ sibling, so resolveKernelPath falls through to cwd — which is
	// /home/<user> when herdr opens a pane, not the repo root. Setting
	// NEXUS3_KERNEL_PATH from the install-time-stamped value in the sidecar
	// makes substrate selection (SelectSubstrate → resolveKernelPath) succeed
	// regardless of the pane's starting cwd.
	nexus3Bin, kernelPath := herdrReadSidecar()
	herdrApplyKernelPath(kernelPath, os.Getenv, os.Setenv)

	var svc sandboxGetter
	if s, err := newSandboxService(); err == nil {
		svc = s
	}

	if err := herdrDefaultShellCore(ctx, os.Getenv, storeRoot, svc, nexus3Bin, herdrGuestShellExecFn); err != nil {
		slog.Warn("nexus3-guest-shell: exec failed; retrying host shell", "err", err)
	}
	// Reached only when syscall.Exec failed to replace the process (rare).
	// herdrDefaultShellCore already attempted execHostShell; try once more.
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	_ = herdrGuestShellExecFn(sh, []string{sh}, os.Environ())
	herdrGuestShellExitFn(0)
}

// herdrApplyKernelPath publishes the install-time-stamped kernel path into the
// environment so substrate selection succeeds regardless of the pane's cwd.
//
// This is the CRITICAL 1 fix in isolated form. It exists as a separate function
// because the inline version had no test: disabling it left the entire suite
// green while the feature was dead from herdr's actual pane cwd (/home/<user>),
// and the only thing that caught it was a live run from the right directory.
//
// An explicit NEXUS3_KERNEL_PATH always wins — the operator's override must not
// be clobbered by the stamped value.
func herdrApplyKernelPath(kernelPath string, getenv func(string) string, setenv func(string, string) error) {
	if kernelPath == "" {
		return
	}
	if getenv("NEXUS3_KERNEL_PATH") != "" {
		return
	}
	_ = setenv("NEXUS3_KERNEL_PATH", kernelPath)
}

// herdrReadSidecar reads the companion sidecar written by
// "nexus3 herdr install-default-shell" and returns (nexus3Bin, kernelPath).
//
// Sidecar format (two newline-separated lines):
//
//	<real nexus3 binary path>
//	<kernel image path stamped at install time>   ← may be absent (old sidecar)
//
// The sidecar is necessary because os.Executable() inside the hard-linked
// binary returns the hard link's own path. Using that as nexus3Bin would cause
// an exec loop (argv[0] == "nexus3-guest-shell" → RunHerdrGuestShell again).
// The kernel path is stamped at install time to break the cwd dependency in
// resolveKernelPath (CRITICAL 1): herdr opens panes from the user's home dir,
// not the repo root, so the cwd fallback in resolveKernelPath always misses.
//
// Returns ("", "") on any error; callers fall back to host shell on empty nexus3Bin.
func herdrReadSidecar() (nexus3Bin, kernelPath string) {
	self, err := os.Executable()
	if err != nil {
		return "", ""
	}
	data, err := os.ReadFile(self + herdrSidecarSuffix)
	if err != nil {
		return "", ""
	}
	lines := strings.SplitN(strings.TrimRight(string(data), "\n"), "\n", 2)
	if len(lines) >= 1 {
		nexus3Bin = strings.TrimSpace(lines[0])
	}
	if len(lines) >= 2 {
		kernelPath = strings.TrimSpace(lines[1])
	}
	if nexus3Bin == "" {
		return "", ""
	}
	if _, err := os.Stat(nexus3Bin); err != nil {
		return "", "" // sidecar path stale; fail-open
	}
	return nexus3Bin, kernelPath
}

// runHerdrDefaultShell is the production entry point for
// "nexus3 herdr default-shell". Used when invoked directly via the CLI;
// the preferred path is the argv[0] dispatch through RunHerdrGuestShell.
// Every failure path falls through to the host shell.
func runHerdrDefaultShell(ctx context.Context, _ []string, _ *Output) error {
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		storeRoot = "" // soft error: core falls back to host shell
	}

	// Connect to the nexus3 daemon for cwd resolution and sandbox state check.
	// A failure means svc=nil; core skips state check and uses /root cwd.
	var svc sandboxGetter
	if s, err := newSandboxService(); err == nil {
		svc = s
	}

	nexus3Bin, err := os.Executable()
	if err != nil {
		nexus3Bin = "" // guard in core falls to host shell
	}

	return herdrDefaultShellCore(ctx, os.Getenv, storeRoot, svc, nexus3Bin, syscall.Exec)
}

// runHerdrInstallDefaultShell hard-links the nexus3 binary to
// ~/.local/bin/nexus3-guest-shell and writes a companion sidecar file with the
// real nexus3 binary path. herdr's [terminal] default_shell is set to the
// install path.
//
// Hard link vs wrapper script:
//   - No PATH lookup at runtime — the installed path IS the binary (CRITICAL 1).
//   - Hard link survives rebuilds: "go build" creates a new inode; the hard
//     link holds the installed inode until re-install (CRITICAL 2).
//   - The binary's argv[0] dispatch (main.go) routes to RunHerdrGuestShell
//     which has a top-level panic recovery (CRITICAL 3).
//   - The sidecar stores the real nexus3 binary path so the exec leg does not
//     loop back into the guest-shell dispatch (avoids execve self-loop).
func runHerdrInstallDefaultShell(_ context.Context, _ []string, out *Output) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("install-default-shell: resolve home: %w", err)
	}
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("install-default-shell: create ~/.local/bin: %w", err)
	}

	// Resolve the real path of this binary. EvalSymlinks is required before
	// os.Link — hard links cannot cross symlink boundaries.
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("install-default-shell: resolve own binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("install-default-shell: resolve own binary symlinks: %w", err)
	}

	installPath := filepath.Join(binDir, "nexus3-guest-shell")
	_ = os.Remove(installPath) // remove old file/link before installing

	// Hard-link this binary to the install path. A hard link means the
	// installed entry point IS this binary's inode: no PATH lookup, no stale
	// subcommand, survives rebuilds until re-install.
	if err := os.Link(self, installPath); err != nil {
		// Cross-device or unsupported filesystem: copy the binary instead.
		if err2 := herdrCopyBinary(self, installPath); err2 != nil {
			return fmt.Errorf("install-default-shell: install binary (link: %v; copy: %w)", err, err2)
		}
	}

	// Sidecar: two-line file storing the real nexus3 binary path (line 1) and
	// the kernel image path (line 2). The kernel path is stamped at install time
	// so RunHerdrGuestShell can set NEXUS3_KERNEL_PATH before substrate selection,
	// breaking the cwd dependency in resolveKernelPath (CRITICAL 1). If kernel
	// resolution fails at install time (kernel not yet present), line 2 is empty
	// and the pane falls back to host shell — fail-open is preserved.
	//
	// Version skew: the hard link freezes the DECISION binary at install time;
	// the sidecar always names the latest-built nexus3 for the EXEC leg. If the
	// exec verb's CLI surface changes after a rebuild, the stale decision binary
	// builds an argv that the new exec binary rejects — AFTER syscall.Exec
	// replaces the process. The symptom is a visible error in the pane (not a
	// silent dead pane), and recovery is "nexus3 herdr install-default-shell".
	// A build-ID stamp was considered and rejected: it requires embedding a
	// timestamp (complicates reproducibility) or computing a content hash
	// (startup cost), for an error that is visible and self-documenting.
	sidecarPath := installPath + herdrSidecarSuffix
	kernelLine := ""
	if kp, kErr := resolveKernelPath(); kErr == nil {
		kernelLine = kp
	}
	sidecarContent := self + "\n" + kernelLine + "\n"
	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o644); err != nil {
		_ = os.Remove(installPath)
		return fmt.Errorf("install-default-shell: write sidecar: %w", err)
	}

	// Probe: run the installed binary as "nexus3-guest-shell" with
	// NEXUS3_HOST_SHELL=1 and SHELL=/bin/true. A working install exec-replaces
	// itself with /bin/true (exit 0). A stale binary without the argv[0]
	// dispatch routes to the CLI and exits non-zero.
	if !herdrSkipInstallProbeForTest {
		if err := herdrInstallProbeCmd(installPath).Run(); err != nil {
			_ = os.Remove(installPath)
			_ = os.Remove(sidecarPath)
			return fmt.Errorf("install-default-shell: probe failed — binary may be too old; rebuild nexus3 and retry: %w", err)
		}
	}

	fmt.Fprintf(out.w, "Installed: %s\n\n", installPath)
	fmt.Fprintf(out.w, "Add to ~/.config/herdr/config.toml:\n\n")
	fmt.Fprintf(out.w, "[terminal]\ndefault_shell = %q\n", installPath)
	return nil
}

// herdrInstallProbeCmd returns the probe command used by
// runHerdrInstallDefaultShell to verify the installed binary responds to the
// argv[0] dispatch. Extracted so tests can inspect the command structure
// without running it.
//
// The probe runs installPath as "nexus3-guest-shell" with NEXUS3_HOST_SHELL=1
// and SHELL=/bin/true. A correctly installed binary exec-replaces itself with
// /bin/true (exit 0). The key invariants:
//   - Args[0] == "nexus3-guest-shell" triggers the argv[0] dispatch in main.go
//   - NEXUS3_HOST_SHELL=1 causes herdrDefaultShellCore to exec $SHELL immediately
//   - SHELL=/bin/true exits 0, confirming the dispatch and escape-hatch work
func herdrInstallProbeCmd(installPath string) *osexec.Cmd {
	return &osexec.Cmd{
		Path: installPath,
		Args: []string{"nexus3-guest-shell"},
		Env:  append(os.Environ(), "NEXUS3_HOST_SHELL=1", "SHELL=/bin/true"),
	}
}

// herdrCopyBinary copies the file at src to dst with the same permissions.
// Used as a cross-device fallback when os.Link fails.
func herdrCopyBinary(src, dst string) error {
	srcF, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcF.Close()
	info, err := srcF.Stat()
	if err != nil {
		return err
	}
	dstF, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer dstF.Close()
	_, err = io.Copy(dstF, srcF)
	return err
}

// ── wt/ supervised shell (Mechanism 2) ───────────────────────────────────────

// herdrWtSupervisedShell runs the guest shell as a supervised child for an
// auto-bound worktree pane.  It replaces the exec-replace path so the parent
// process survives pane-close (herdr sends SIGHUP to the process group) and
// can run last-pane teardown.
//
// FAIL-OPEN contract: every error after the child exits is logged and
// swallowed — a bug here would freeze every new pane on the machine, which is
// strictly worse than leaking a sandbox VM (Mechanism 1 / prune backstop
// catches those).
func herdrWtSupervisedShell(ctx context.Context, nexus3Bin string, binding HerdrSpaceBinding, argv []string) error {
	// Ignore SIGHUP so the pane-close signal from herdr (sent to the process
	// group) does not kill us.  The child is in the same process group and
	// receives SIGHUP, which terminates nexus3 exec / the guest shell.
	signal.Ignore(syscall.SIGHUP)

	// Run the guest shell as a child.  The child inherits stdin/stdout/stderr
	// so it holds the controlling TTY; job control and Ctrl-C pass through.
	if err := herdrWtChildRunnerFn(ctx, nexus3Bin, argv); err != nil {
		// Non-fatal: the child can exit non-zero (user typed "exit 1", agent
		// aborted, SIGHUP from pane-close, etc.).  Continue to the last-pane
		// check regardless.
		slog.Warn("nexus3-guest-shell: wt/ child exited with error", "handle", binding.SandboxHandle, "err", err)
	}

	// Last-pane check: if other panes remain in this workspace, the user is
	// still active — skip teardown.
	remaining, err := herdrWtPaneListerFn(ctx, binding.HerdrWorkspaceID, binding.GuestPaneID)
	if err != nil {
		// FAIL-OPEN: herdr unreachable, parse error, etc.  The prune backstop
		// (Mechanism 1) will reap the sandbox on its next run.
		slog.Warn("nexus3-guest-shell: wt/ pane-list failed; skipping teardown (prune will catch it)",
			"handle", binding.SandboxHandle, "err", err)
		return nil
	}
	if remaining > 0 {
		// Other panes still alive — not the last one.
		return nil
	}

	// Last pane: tear down the space (VM + workspace + binding) atomically.
	// FAIL-OPEN: any error is logged by herdrSpaceTeardown (failOpen:true) and
	// swallowed here — a teardown bug must never freeze a new pane.
	storeRoot, storeErr := store.DefaultRoot()
	herdrBin, _ := resolveHerdrBin()
	if storeErr != nil {
		// storeRoot unavailable: fall back to VM-only removal.
		slog.Warn("nexus3-guest-shell: wt/ store root unavailable; falling back to VM-only reap",
			"handle", binding.SandboxHandle, "err", storeErr)
		if err := herdrWtSandboxRemoverFn(ctx, binding.SandboxHandle); err != nil {
			slog.Warn("nexus3-guest-shell: wt/ sandbox rm failed; prune will catch it",
				"handle", binding.SandboxHandle, "err", err)
		}
		return nil
	}
	herdrWtTeardownFn(ctx, storeRoot, binding.SandboxHandle, herdrBin, binding.SandboxID)
	return nil
}

// herdrWtTeardownFn performs the atomic wt/ space teardown after the last pane
// closes.  Replaced in tests to avoid live store/herdr calls.
var herdrWtTeardownFn = func(ctx context.Context, storeRoot, handle, herdrBin, sandboxID string) {
	deps := txnDeps{
		svcRemove: func(ctx context.Context, ref string) error {
			return herdrWtSandboxRemoverFn(ctx, ref)
		},
		workspaceClose: func(ctx context.Context, wsID string) error {
			return herdrWorkspaceClose(ctx, herdrBin, wsID)
		},
		bindingDelete: func(ctx context.Context, label string) error {
			return HerdrSpaceDelete(ctx, storeRoot, label)
		},
	}
	if err := herdrSpaceTeardown(ctx, storeRoot, handle, deps, teardownOpts{failOpen: true, expectedSandboxID: sandboxID}); err != nil {
		// herdrSpaceTeardown with failOpen:true never returns non-nil; belt-and-suspenders.
		slog.Warn("nexus3-guest-shell: wt/ teardown error (swallowed)", "handle", handle, "err", err)
	}
}

// herdrWtChildRunner is the production implementation of herdrWtChildRunnerFn.
// It runs argv[0] with argv[1:] as a child process, inheriting the controlling
// TTY, and waits for it to exit.
func herdrWtChildRunner(_ context.Context, _ string, argv []string) error {
	cmd := osexec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Wait()
}

// herdrWtPaneLister is the production implementation of herdrWtPaneListerFn.
// It calls `herdr pane list --workspace <workspaceID>` and counts panes whose
// pane_id is not ownPaneID.
func herdrWtPaneLister(ctx context.Context, workspaceID, ownPaneID string) (int, error) {
	herdrBin, err := resolveHerdrBin()
	if err != nil {
		return 0, fmt.Errorf("wt/ pane-list: resolve herdr: %w", err)
	}
	if herdrBin == "" {
		return 0, fmt.Errorf("wt/ pane-list: herdr binary not available (HERDR_BIN_PATH unset and herdr not on PATH)")
	}
	// NOTE: the workspace is passed via --workspace; herdr rejects a bare
	// positional ("unknown option"). Output is {"result":{"panes":[...]}} on
	// success and {"error":{"code":...}} on failure, both with the error body
	// on stdout even when the exit status is non-zero — so parse before trusting
	// cmdErr.
	out, cmdErr := osexec.CommandContext(ctx, herdrBin, "pane", "list", "--workspace", workspaceID).CombinedOutput()
	return parseWtPaneListRemaining(out, cmdErr, ownPaneID)
}

// parseWtPaneListRemaining interprets `herdr pane list --workspace` output into
// the count of panes OTHER than ownPaneID.
//
// A workspace_not_found error means the workspace is gone — which is the common
// "closed the whole worktree workspace" case — so it maps to 0 remaining panes
// (the sandbox SHOULD be reaped), NOT to a fail-open error. Any other command
// failure (herdr unreachable, unparseable output) propagates as an error so the
// caller fails open and leaves the sandbox for the prune backstop.
func parseWtPaneListRemaining(out []byte, cmdErr error, ownPaneID string) (int, error) {
	var resp struct {
		Result struct {
			Panes []struct {
				PaneID string `json:"pane_id"`
			} `json:"panes"`
		} `json:"result"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	// herdr prints a JSON body (result or error) on both success and failure.
	if jsonErr := json.Unmarshal(out, &resp); jsonErr == nil {
		if resp.Error != nil {
			if resp.Error.Code == "workspace_not_found" {
				return 0, nil // workspace gone → no panes remain → reap
			}
			return 0, fmt.Errorf("wt/ pane-list: herdr error: %s", resp.Error.Code)
		}
		count := 0
		for _, p := range resp.Result.Panes {
			if p.PaneID != ownPaneID {
				count++
			}
		}
		return count, nil
	}
	// Unparseable output: if the command also failed, surface that (herdr down);
	// otherwise report the parse failure.
	if cmdErr != nil {
		return 0, fmt.Errorf("wt/ pane-list: herdr pane list: %w: %s", cmdErr, strings.TrimSpace(string(out)))
	}
	return 0, fmt.Errorf("wt/ pane-list: parse output: %q", strings.TrimSpace(string(out)))
}

// herdrWtSandboxRemover is the production implementation of herdrWtSandboxRemoverFn.
// It removes the sandbox VM via the local service.
func herdrWtSandboxRemover(ctx context.Context, handle string) error {
	svc, err := newSandboxService()
	if err != nil {
		return fmt.Errorf("wt/ sandbox rm: open service: %w", err)
	}
	return svc.Remove(ctx, handle)
}
