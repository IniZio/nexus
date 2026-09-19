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

	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/store"
)

// binary path to form the companion sidecar. A single source-of-truth constant
const herdrSidecarSuffix = ".nexusbin"

type herdrExecFn func(argv0 string, argv []string, envv []string) error

/** sandboxStarter is the optional Start capability the guest shell uses to resume a stopped worktree sandbox. */
type sandboxStarter interface {
	Start(ctx context.Context, ref string) (domain.Sandbox, error)
}

type sandboxDialer interface {
	sandboxGetter
	DialGuest(ctx context.Context, ref string, port uint32) (net.Conn, error)
}

var herdrGuestShellExecFn herdrExecFn = syscall.Exec

var herdrGuestShellExitFn = os.Exit

var herdrSkipInstallProbeForTest bool

// herdrAutoCreateTimeout is the outer deadline for the auto-create subprocess
const herdrAutoCreateTimeout = herdrWorktreeCreateLockTimeout + 30*time.Second

// sidecar (line 3) by "nexus herdr install-default-shell --next <path>"; an
const herdrGuestShellNextEnv = "NEXUS_GUEST_SHELL_NEXT"

var herdrGuestShellArgsFn = func() []string {
	if filepath.Base(os.Args[0]) != "nexus-guest-shell" {
		return nil
	}
	return os.Args[1:]
}

var herdrDefaultShellAutoCreateFn = herdrDefaultShellAutoCreate

var herdrWtChildRunnerFn func(ctx context.Context, nexusBin string, argv []string) error = herdrWtChildRunner

var herdrWtPaneListerFn func(ctx context.Context, workspaceID, ownPaneID string) (int, error) = herdrWtPaneLister

var herdrWtSandboxRemoverFn func(ctx context.Context, handle string) error = herdrWtSandboxRemover

// False on any I/O error (FAIL-OPEN toward host shell).
var herdrAutoCreatePredicateFn = func(allBindings []HerdrSpaceBinding) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	return herdrAutoCreatePredicateWith(cwd, allBindings, os.Stat, os.ReadFile)
}

// flows). False on any I/O error (FAIL-OPEN toward host shell).
func herdrAutoCreatePredicateWith(
	cwd string,
	allBindings []HerdrSpaceBinding,
	statFn func(string) (os.FileInfo, error),
	readFileFn func(string) ([]byte, error),
) bool {
	worktreeRoot, mainRepo := herdrLinkedWorktreeFromCwd(cwd, statFn, readFileFn)
	if mainRepo == "" {
		return false
	}
	// second arm the FIRST worktree of a repo never engaged here even though
	return herdrRepoHasBoundSandbox(mainRepo, allBindings) || herdrRepoHasNexusConfigWith(worktreeRoot, statFn)
}

func herdrLinkedWorktreeFromCwd(
	cwd string,
	statFn func(string) (os.FileInfo, error),
	readFileFn func(string) ([]byte, error),
) (worktreeRoot, mainRepo string) {
	const herdrGitSearchDepth = 8
	dir := cwd
	for i := 0; i < herdrGitSearchDepth; i++ {
		candidate := filepath.Join(dir, ".git")
		fi, err := statFn(candidate)
		if err == nil {
			if !fi.Mode().IsRegular() {
				return "", ""
			}
			data, rerr := readFileFn(candidate)
			if rerr != nil {
				return "", ""
			}
			mainRepo = herdrMainRepoFromGitdir(string(data))
			if mainRepo == "" {
				return "", ""
			}
			return dir, mainRepo
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", ""
}

func herdrRepoHasNexusConfigWith(dir string, statFn func(string) (os.FileInfo, error)) bool {
	if dir == "" {
		return false
	}
	for _, rel := range []string{config.ConfigRelPath, filepath.Join(".nexus", "Containerfile")} {
		if st, err := statFn(filepath.Join(dir, rel)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

var herdrLinkedWorktreeReasonFn = func() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	_, mainRepo := herdrLinkedWorktreeFromCwd(cwd, os.Stat, os.ReadFile)
	return mainRepo != ""
}

// loop). The self check is by inode: the installed hard link and the build
func herdrGuestShellNext(getenv func(string) string) string {
	next := getenv(herdrGuestShellNextEnv)
	if next == "" {
		return ""
	}
	st, err := os.Stat(next)
	if err != nil || st.IsDir() {
		return ""
	}
	if self, selfErr := os.Executable(); selfErr == nil {
		if selfSt, sErr := os.Stat(self); sErr == nil && os.SameFile(selfSt, st) {
			return ""
		}
	}
	return next
}

// (worktree.useRelativePaths). A relative path returned here will never match
// through to the host shell. This is fail-open (safe) but means auto-create
func herdrMainRepoFromGitdir(content string) string {
	line := strings.TrimSpace(content)
	const prefix = "gitdir: "
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	target := strings.TrimSpace(line[len(prefix):])
	worktreesDir := filepath.Dir(target) // <main>/.git/worktrees
	gitDir := filepath.Dir(worktreesDir) // <main>/.git
	if filepath.Base(worktreesDir) != "worktrees" || filepath.Base(gitDir) != ".git" {
		return ""
	}
	return filepath.Dir(gitDir) // <main>
}

// "nexus herdr worktree-sandbox" output into. The two formulas MUST agree:
func herdrWtCreateLogPath(storeRoot, wsID string) string {
	return filepath.Join(storeRoot, "herdr-wt-create-ws-"+wsID+".log")
}

var herdrWtCreateLogPollInterval = 500 * time.Millisecond

// wide because a cold build can go quiet for a while (image pull) between
const herdrWtCreateLogStaleAfter = 10 * time.Minute

func herdrTailFileUntil(path string, w io.Writer, done <-chan struct{}, interval, staleAfter time.Duration) {
	var off int64 = -1 // -1: not positioned yet
	drain := func() {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return
		}
		if off < 0 {
			off = 0
			if time.Since(st.ModTime()) > staleAfter {
				off = st.Size() // an earlier run's log; show only what is written from now on
			}
		}
		if st.Size() < off {
			off = 0
		}
		if st.Size() == off {
			return
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return
		}
		n, _ := io.Copy(w, f)
		off += n
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			drain()
			return
		case <-t.C:
			drain()
		}
	}
}

// caller falls through to execHostShell (FAIL-OPEN).
func herdrDefaultShellAutoCreate(ctx context.Context, storeRoot, wsID, nexusBin string, w io.Writer) (HerdrSpaceBinding, bool) {
	if nexusBin == "" {
		return HerdrSpaceBinding{}, false
	}
	fmt.Fprintf(w, "nexus-guest-shell: linked worktree detected — waiting for / auto-creating the sandbox for workspace %s (a cold build can take a few minutes; the provisioning log streams below)...\n", wsID)
	autoCtx, cancel := context.WithTimeout(ctx, herdrAutoCreateTimeout)
	defer cancel()
	cmd := herdrExecCommandContext(autoCtx, nexusBin, "herdr", "worktree-sandbox", "--auto", wsID)
	cmd.Stdout = w
	cmd.Stderr = w
	tailStop := make(chan struct{})
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		herdrTailFileUntil(herdrWtCreateLogPath(storeRoot, wsID), w, tailStop, herdrWtCreateLogPollInterval, herdrWtCreateLogStaleAfter)
	}()
	defer func() {
		close(tailStop)
		<-tailDone
	}()
	// A non-zero exit is REPORTED but is not by itself a verdict, because
	// terms: no binding → (zero, false) → execHostShell, exactly as before.
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "nexus-guest-shell: auto-create: %v\n", err)
	}
	b, ok, _ := herdrDefaultShellLookup(storeRoot, wsID)
	return b, ok
}

// the guest cwd, then calls execFn to replace the current process with either
// FAIL-OPEN contract: every code path that cannot confirm a live sandbox
// binding returns execHostShell. All failure modes reach it through explicit
// early returns — any new check must call return execHostShell() explicitly.
// if it fails, execHostShell() is returned immediately.
func herdrDefaultShellCore(
	ctx context.Context,
	getenv func(string) string,
	storeRoot string,
	svc sandboxGetter, // nil → skip state check and cwd resolution, use /root
	nexusBin string, // path to the nexus binary for re-exec
	execFn herdrExecFn,
) error {
	shellArgs := herdrGuestShellArgsFn()

	// execHostShell replaces the current process with the operator's host shell.
	execHostShell := func() error {
		sh := getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		return execFn(sh, append([]string{sh}, shellArgs...), os.Environ())
	}

	execUnboundShell := func() error {
		if next := herdrGuestShellNext(getenv); next != "" {
			if err := execFn(next, append([]string{next}, shellArgs...), os.Environ()); err != nil {
				slog.Warn("nexus-guest-shell: chained guest shell exec failed; falling back to host shell", "next", next, "err", err)
				return execHostShell()
			}
			return nil
		}
		return execHostShell()
	}

	if getenv("NEXUS_HOST_SHELL") != "" {
		return execHostShell()
	}

	wsID := getenv("HERDR_WORKSPACE_ID")
	if wsID == "" {
		return execUnboundShell()
	}

	if storeRoot == "" {
		return execUnboundShell()
	}

	allBindings, readErr := herdrSpaceReadAll(storeRoot)
	if readErr != nil {
		return execUnboundShell()
	}

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
		// error (FAIL-OPEN toward the unbound shell).
		if !herdrAutoCreatePredicateFn(allBindings) {
			if herdrLinkedWorktreeReasonFn() {
				fmt.Fprintf(os.Stderr, "nexus-guest-shell: workspace %s has no nexus sandbox binding and its repo is neither nexus-bound nor onboarded (.nexus/config.yaml); not a nexus space\n", wsID)
			}
			return execUnboundShell()
		}
		var ok bool
		binding, ok = herdrDefaultShellAutoCreateFn(ctx, storeRoot, wsID, nexusBin, os.Stderr)
		if !ok {
			fmt.Fprintf(os.Stderr, "nexus-guest-shell: workspace %s still has no sandbox binding; opening host shell (retry: nexus herdr space-open-pane %s)\n", wsID, wsID)
			return execHostShell()
		}
	}

	// nexus binary (e.g. sidecar missing after install). Fall back rather than
	// exec'ing an empty path into a dead pane. (CRITICAL 1)
	if nexusBin == "" {
		return execHostShell()
	}

	// CRITICAL: "State == Running" is not the condition that fails. The real
	// the existing fail-open behaviour for daemon-unreachable is preserved.
	// (CRITICAL 4 + CRITICAL 5)
	cwd := "/root"
	if svc != nil {
		sb, sbErr := svc.Get(ctx, binding.SandboxHandle)
		if sbErr != nil {
			return execHostShell()
		}
		/**
		 * A Stopped worktree sandbox is the normal state after its last pane
		 * closed (herdrWtTeardownFn stops rather than removes). Opening a pane
		 * again is the operator asking to continue, so start it here; boot
		 * output is shown because it takes seconds, not milliseconds. Any
		 * other non-Running state (paused, error, mid-create) still falls
		 * open to a host shell.
		 */
		if sb.State == domain.Stopped && binding.IsWorktreeManaged() {
			if st, ok := svc.(sandboxStarter); ok {
				fmt.Fprintf(os.Stderr, "nexus-guest-shell: sandbox %s is stopped; starting it ...\n", binding.SandboxHandle)
				started, startErr := st.Start(ctx, binding.SandboxHandle)
				if startErr != nil {
					fmt.Fprintf(os.Stderr, "nexus-guest-shell: start %s: %v; opening host shell\n", binding.SandboxHandle, startErr)
					return execHostShell()
				}
				sb = started
			}
		}
		if sb.State != domain.Running {
			return execHostShell()
		}
		if d, ok := svc.(sandboxDialer); ok {
			dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			conn, dialErr := d.DialGuest(dialCtx, binding.SandboxHandle, driver.AgentControlPort)
			cancel()
			if dialErr != nil {
				slog.Warn("nexus-guest-shell: guest not dialable; falling back to host shell", "err", dialErr)
				return execHostShell()
			}
			conn.Close()
		}
		cwd = herdrShellCwdFromSandbox(sb)
	}

	// FAIL-OPEN: execFn is syscall.Exec in production. If it returns — either
	// because exec itself failed, or because a test seam replaced it — the
	// process was NOT replaced. An exec failure is logged and execHostShell is
	argv := []string{nexusBin, "exec", "--pty", "--cwd", cwd, binding.SandboxHandle, "/bin/bash", "--login"}

	// pane-close SIGHUP (sent to the process group by herdr) so it can run
	if binding.IsWorktreeManaged() {
		return herdrWtSupervisedShell(ctx, nexusBin, binding, argv)
	}

	if err := execFn(nexusBin, argv, os.Environ()); err != nil {
		slog.Warn("nexus-guest-shell: exec failed; falling back to host shell", "err", err)
		return execHostShell()
	}
	return nil
}

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

// RunHerdrGuestShell is the argv[0]-dispatched entry point when the nexus
// binary is hard-linked as "nexus-guest-shell" by
// "nexus herdr install-default-shell". It wraps the resolution logic with a
// top-level panic recovery (CRITICAL 3): a panic anywhere in the resolution
// path is caught here and the operator always gets a working host shell.
//
// New checks inside herdrDefaultShellCore must still call return execHostShell()
// explicitly — see that function's FAIL-OPEN contract comment.
func RunHerdrGuestShell() {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("nexus-guest-shell: panic; falling back to host shell", "panic", r)
			sh := os.Getenv("SHELL")
			if sh == "" {
				sh = "/bin/sh"
			}
			_ = herdrGuestShellExecFn(sh, append([]string{sh}, herdrGuestShellArgsFn()...), os.Environ())
			herdrGuestShellExitFn(0)
			return
		}
	}()

	ctx := context.Background()

	// SIGKILL.  Run teardown and exit — never open a shell.
	if binding, ok := herdrWtReapBindingFromEnv(os.Getenv); ok {
		runHerdrWtDetachedReap(ctx, binding)
		herdrGuestShellExitFn(0)
		return
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		storeRoot = ""
	}

	// Read the sidecar for the real nexus binary path and the stamped kernel
	// path. CRITICAL 1: the hard link installs into ~/.local/bin which has no
	// NEXUS_KERNEL_PATH from the install-time-stamped value in the sidecar
	nexusBin, kernelPath, nextShell := herdrReadSidecar()
	herdrApplyKernelPath(kernelPath, os.Getenv, os.Setenv)
	herdrApplyGuestShellNext(nextShell, os.Getenv, os.Setenv)

	var svc sandboxGetter
	if s, err := newSandboxService(); err == nil {
		svc = s
	}

	if err := herdrDefaultShellCore(ctx, os.Getenv, storeRoot, svc, nexusBin, herdrGuestShellExecFn); err != nil {
		slog.Warn("nexus-guest-shell: exec failed; retrying host shell", "err", err)
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	_ = herdrGuestShellExecFn(sh, append([]string{sh}, herdrGuestShellArgsFn()...), os.Environ())
	herdrGuestShellExitFn(0)
}

// This is the CRITICAL 1 fix in isolated form. It exists as a separate function
// because the inline version had no test: disabling it left the entire suite
// An explicit NEXUS_KERNEL_PATH always wins — the operator's override must not
func herdrApplyKernelPath(kernelPath string, getenv func(string) string, setenv func(string, string) error) {
	if kernelPath == "" {
		return
	}
	if getenv("NEXUS_KERNEL_PATH") != "" {
		return
	}
	_ = setenv("NEXUS_KERNEL_PATH", kernelPath)
}

// shell (sidecar line 3) unless the operator already set it in the environment.
func herdrApplyGuestShellNext(nextShell string, getenv func(string) string, setenv func(string, string) error) {
	if nextShell == "" || getenv(herdrGuestShellNextEnv) != "" {
		return
	}
	_ = setenv(herdrGuestShellNextEnv, nextShell)
}

// herdrReadSidecar reads the companion sidecar written by
// Sidecar format (two newline-separated lines):
//
//	<kernel image path stamped at install time>   ← may be absent (old sidecar)
//
// The sidecar is necessary because os.Executable() inside the hard-linked
// binary returns the hard link's own path. Using that as nexusBin would cause
// resolveKernelPath (CRITICAL 1): herdr opens panes from the user's home dir,
func herdrReadSidecar() (nexusBin, kernelPath, nextShell string) {
	self, err := os.Executable()
	if err != nil {
		return "", "", ""
	}
	data, err := os.ReadFile(self + herdrSidecarSuffix)
	if err != nil {
		return "", "", ""
	}
	nexusBin, kernelPath, nextShell = herdrParseSidecar(data)
	if nexusBin == "" {
		return "", "", ""
	}
	if _, err := os.Stat(nexusBin); err != nil {
		return "", "", "" // sidecar path stale; fail-open
	}
	return nexusBin, kernelPath, nextShell
}

// herdrParseSidecar splits the sidecar body into its three optional lines.
func herdrParseSidecar(data []byte) (nexusBin, kernelPath, nextShell string) {
	lines := strings.SplitN(strings.TrimRight(string(data), "\n"), "\n", 3)
	if len(lines) >= 1 {
		nexusBin = strings.TrimSpace(lines[0])
	}
	if len(lines) >= 2 {
		kernelPath = strings.TrimSpace(lines[1])
	}
	if len(lines) >= 3 {
		nextShell = strings.TrimSpace(lines[2])
	}
	return nexusBin, kernelPath, nextShell
}

func runHerdrDefaultShell(ctx context.Context, _ []string, _ *Output) error {
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		storeRoot = "" // soft error: core falls back to host shell
	}

	var svc sandboxGetter
	if s, err := newSandboxService(); err == nil {
		svc = s
	}

	nexusBin, err := os.Executable()
	if err != nil {
		nexusBin = "" // guard in core falls to host shell
	}

	return herdrDefaultShellCore(ctx, os.Getenv, storeRoot, svc, nexusBin, syscall.Exec)
}

// ~/.local/bin/nexus-guest-shell and writes a companion sidecar file with the
// Hard link vs wrapper script:
//   - No PATH lookup at runtime — the installed path IS the binary (CRITICAL 1).
//   - Hard link survives rebuilds: "go build" creates a new inode; the hard
//     link holds the installed inode until re-install (CRITICAL 2).
//     which has a top-level panic recovery (CRITICAL 3).
//   - The sidecar stores the real nexus binary path so the exec leg does not
func runHerdrInstallDefaultShell(_ context.Context, args []string, out *Output) error {
	nextShell, writeConfig, err := herdrInstallDefaultShellParseArgs(args)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("install-default-shell: resolve home: %w", err)
	}
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("install-default-shell: create ~/.local/bin: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("install-default-shell: resolve own binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("install-default-shell: resolve own binary symlinks: %w", err)
	}

	installPath := filepath.Join(binDir, "nexus-guest-shell")
	_ = os.Remove(installPath) // remove old file/link before installing

	var configPath string
	var configBackupPath string
	var origConfigData []byte
	if writeConfig {
		configPath, err = herdrConfigTOMLPath()
		if err != nil {
			return fmt.Errorf("install-default-shell: resolve config path: %w", err)
		}
		origConfigData, _ = os.ReadFile(configPath)
		_, bkp, foreign, wcErr := herdrWriteConfigTOML(configPath, installPath)
		if wcErr != nil {
			return fmt.Errorf("install-default-shell: write config: %w", wcErr)
		}
		configBackupPath = bkp
		if foreign != "" && nextShell == "" {
			if filepath.Base(foreign) == "nexus-guest-shell" {
				fmt.Fprintf(out.w, "Note: existing default_shell is nexus-guest-shell (%s); overwriting it.\n\n", foreign)
			} else if st, stErr := os.Stat(foreign); stErr == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				nextShell = foreign
			}
		}
	}

	// Hard-link this binary to the install path. A hard link means the
	// installed entry point IS this binary's inode: no PATH lookup, no stale
	if err := os.Link(self, installPath); err != nil {
		if err2 := herdrCopyBinary(self, installPath); err2 != nil {
			return fmt.Errorf("install-default-shell: install binary (link: %v; copy: %w)", err, err2)
		}
	}

	// Sidecar: two-line file storing the real nexus binary path (line 1) and
	// breaking the cwd dependency in resolveKernelPath (CRITICAL 1). If kernel
	// and the pane falls back to host shell — fail-open is preserved.
	// Version skew: the hard link freezes the DECISION binary at install time;
	// the sidecar always names the latest-built nexus for the EXEC leg. If the
	// exec verb's CLI surface changes after a rebuild, the stale decision binary
	sidecarPath := installPath + herdrSidecarSuffix
	kernelLine := ""
	if kp, kErr := resolveKernelPath(); kErr == nil {
		kernelLine = kp
	}
	sidecarContent := self + "\n" + kernelLine + "\n"
	if nextShell != "" {
		sidecarContent += nextShell + "\n"
	}
	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o644); err != nil {
		_ = os.Remove(installPath)
		return fmt.Errorf("install-default-shell: write sidecar: %w", err)
	}

	// itself with /bin/true (exit 0). A stale binary without the argv[0]
	if !herdrSkipInstallProbeForTest {
		if err := herdrInstallProbeCmd(installPath).Run(); err != nil {
			_ = os.Remove(installPath)
			_ = os.Remove(sidecarPath)
			return fmt.Errorf("install-default-shell: probe failed — binary may be too old; rebuild nexus and retry: %w", err)
		}
	}

	fmt.Fprintf(out.w, "Installed: %s\n\n", installPath)
	if nextShell != "" {
		fmt.Fprintf(out.w, "Chained guest shell (non-nexus panes): %s\n\n", nextShell)
	}

	if writeConfig {
		if herdrBin, herdrErr := resolveHerdrBin(); herdrErr == nil {
			checkOut, checkErr := osexec.Command(herdrBin, "config", "check").CombinedOutput()
			if checkErr != nil {
				if origConfigData != nil {
					_ = os.WriteFile(configPath, origConfigData, 0o644)
				} else {
					_ = os.Remove(configPath)
				}
				_ = os.Remove(configBackupPath)
				fmt.Fprintf(out.w, "Add to %s:\n\n", configPath)
				fmt.Fprintf(out.w, "[terminal]\ndefault_shell = %q\n", installPath)
				return fmt.Errorf("install-default-shell: herdr config check failed (config restored): %s",
					strings.TrimSpace(string(checkOut)))
			}
			_ = osexec.Command(herdrBin, "server", "reload-config").Run()
		}
		fmt.Fprintf(out.w, "Wrote %s\n", configPath)
	} else {
		fmt.Fprintf(out.w, "Add to ~/.config/herdr/config.toml:\n\n")
		fmt.Fprintf(out.w, "[terminal]\ndefault_shell = %q\n", installPath)
	}
	return nil
}

// herdrConfigTOMLPath returns the path to herdr's config.toml.
// $XDG_CONFIG_HOME/herdr/config.toml, then ~/.config/herdr/config.toml.
func herdrConfigTOMLPath() (string, error) {
	if p := os.Getenv("HERDR_CONFIG_PATH"); p != "" {
		return p, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("herdrConfigTOMLPath: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "herdr", "config.toml"), nil
}

// under the [terminal] section of herdr's config.toml at configPath.
// Four states (per D-4):
func herdrWriteConfigTOML(configPath, installPath string) (changed bool, backupPath string, foreignShell string, err error) {
	wantLine := fmt.Sprintf("default_shell = %q", installPath)

	data, readErr := os.ReadFile(configPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, "", "", fmt.Errorf("herdrWriteConfigTOML: read %s: %w", configPath, readErr)
	}

	if os.IsNotExist(readErr) {
		if mkErr := os.MkdirAll(filepath.Dir(configPath), 0o755); mkErr != nil {
			return false, "", "", fmt.Errorf("herdrWriteConfigTOML: mkdir: %w", mkErr)
		}
		content := "[terminal]\n" + wantLine + "\n"
		if wErr := os.WriteFile(configPath, []byte(content), 0o644); wErr != nil {
			return false, "", "", fmt.Errorf("herdrWriteConfigTOML: write: %w", wErr)
		}
		return true, "", "", nil
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	inTerminal := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inTerminal = trimmed == "[terminal]"
			continue
		}
		if !inTerminal {
			continue
		}
		key, val, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "default_shell" {
			continue
		}
		currentVal := strings.TrimSpace(val)
		if len(currentVal) >= 2 && currentVal[0] == '"' && currentVal[len(currentVal)-1] == '"' {
			currentVal = currentVal[1 : len(currentVal)-1]
		}
		if currentVal == installPath {
			return false, "", "", nil
		}
		foreignShell = currentVal
		ts := time.Now().UTC().Format("20060102T150405Z")
		backupPath = configPath + ".bak." + ts
		if wErr := os.WriteFile(backupPath, data, 0o644); wErr != nil {
			return false, "", "", fmt.Errorf("herdrWriteConfigTOML: write backup: %w", wErr)
		}
		lines[i] = wantLine
		newContent := strings.Join(lines, "\n") + "\n"
		if wErr := os.WriteFile(configPath, []byte(newContent), 0o644); wErr != nil {
			return false, "", "", fmt.Errorf("herdrWriteConfigTOML: rewrite: %w", wErr)
		}
		return true, backupPath, foreignShell, nil
	}

	termIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "[terminal]" {
			termIdx = i
			break
		}
	}
	var newLines []string
	if termIdx >= 0 {
		newLines = append(newLines, lines[:termIdx+1]...)
		newLines = append(newLines, wantLine)
		newLines = append(newLines, lines[termIdx+1:]...)
	} else {
		newLines = append(newLines, lines...)
		if len(lines) > 0 {
			newLines = append(newLines, "")
		}
		newLines = append(newLines, "[terminal]", wantLine)
	}
	newContent := strings.Join(newLines, "\n") + "\n"
	if wErr := os.WriteFile(configPath, []byte(newContent), 0o644); wErr != nil {
		return false, "", "", fmt.Errorf("herdrWriteConfigTOML: insert: %w", wErr)
	}
	return true, "", "", nil
}

// shell for non-nexus panes) and --write-config (idempotently write config.toml).
func herdrInstallDefaultShellParseArgs(args []string) (nextShell string, writeConfig bool, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--write-config":
			writeConfig = true
		case args[i] == "--next" && i+1 < len(args):
			nextShell = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--next="):
			nextShell = strings.TrimPrefix(args[i], "--next=")
		default:
			return "", false, &UsageError{Msg: "herdr install-default-shell: usage: install-default-shell [--write-config] [--next <guest-shell-path>]"}
		}
	}
	if nextShell == "" {
		return "", writeConfig, nil
	}
	st, statErr := os.Stat(nextShell)
	if statErr != nil {
		return "", false, fmt.Errorf("install-default-shell: --next %s: %w", nextShell, statErr)
	}
	if st.IsDir() || st.Mode()&0o111 == 0 {
		return "", false, fmt.Errorf("install-default-shell: --next %s: not an executable file", nextShell)
	}
	if filepath.Base(nextShell) == "nexus-guest-shell" {
		return "", false, fmt.Errorf("install-default-shell: --next %s: would chain nexus-guest-shell to itself", nextShell)
	}
	return nextShell, writeConfig, nil
}

func herdrInstallProbeCmd(installPath string) *osexec.Cmd {
	return &osexec.Cmd{
		Path: installPath,
		Args: []string{"nexus-guest-shell"},
		Env:  append(os.Environ(), "NEXUS_HOST_SHELL=1", "SHELL=/bin/true"),
	}
}

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

// t+0ms    SIGHUP  → process group  (the child dies here)
// t+14ms   SIGHUP  → process group  (repeat)
// t+279ms  SIGTERM → process group  (killed the parent: only SIGHUP was armed)
// t+~515ms SIGKILL → process group  (uncatchable — see the detached reaper)
var herdrPaneCloseSignals = []os.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT}

// the caller forks would also ignore the signal and never exit when herdr
// Absorbing is necessary but NOT sufficient: herdr escalates to SIGKILL about
func installParentSighupAbsorber() (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, herdrPaneCloseSignals...)
	go func() {
		for range ch { /* absorb: keep parent alive to hand off teardown */
		}
	}()
	return func() { signal.Stop(ch); close(ch) }
}

// process survives pane-close (herdr sends SIGHUP to the process group) and
// FAIL-OPEN contract: every error after the child exits is logged and
func herdrWtSupervisedShell(ctx context.Context, nexusBin string, binding HerdrSpaceBinding, argv []string) error {
	// Absorb SIGHUP via a Notify handler (NOT signal.Ignore) so the parent
	stopSighup := installParentSighupAbsorber()
	defer stopSighup()

	if err := herdrWtChildRunnerFn(ctx, nexusBin, argv); err != nil {
		// aborted, SIGHUP from pane-close, etc.).  Continue to the last-pane
		slog.Warn("nexus-guest-shell: wt/ child exited with error", "handle", binding.SandboxHandle, "err", err)
	}

	// because the store record can be left mid-mutation.  A setsid'd child is
	// SIGKILL never reaches it.
	// FAIL-OPEN: a spawn failure is logged and swallowed; the prune backstop
	if err := herdrWtSpawnDetachedReapFn(binding); err != nil {
		slog.Warn("nexus-guest-shell: wt/ detached reap spawn failed; prune will catch it",
			"handle", binding.SandboxHandle, "err", err)
	}
	return nil
}

// ── Detached wt/ reaper ──────────────────────────────────────────────────────

const (
	herdrWtReapHandleEnv    = "NEXUS_WT_REAP_HANDLE"
	herdrWtReapWorkspaceEnv = "NEXUS_WT_REAP_WORKSPACE"
	herdrWtReapPaneEnv      = "NEXUS_WT_REAP_PANE"
	herdrWtReapSandboxIDEnv = "NEXUS_WT_REAP_SANDBOX_ID"
)

var herdrWtReapSettle = 1500 * time.Millisecond

var herdrWtSpawnDetachedReapFn = herdrWtSpawnDetachedReap

// NEW SESSION (Setsid), so herdr's process-group SIGKILL cannot reach it, and
func herdrWtSpawnDetachedReap(binding HerdrSpaceBinding) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("wt/ detached reap: resolve self: %w", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("wt/ detached reap: open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	cmd := herdrWtDetachedReapCmd(self, binding, devNull)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("wt/ detached reap: start: %w", err)
	}
	return nil
}

/**
 * herdrWtReapOwnPane is the pane the reaper must NOT count as "remaining":
 * the one whose shell just exited. That is this process's own pane, which
 * herdr exports as HERDR_PANE_ID — not binding.GuestPaneID, which is the
 * FIRST guest pane opened at create time and may well still be alive. Using
 * the binding's pane made closing any second guest tab tear the whole space
 * down: the reaper excluded the live first pane as if it were itself, saw
 * zero remaining, and removed the VM under a working agent (2026-09-19,
 * nexus/main; and the earlier "ctrl+d on one of two tabs removes the space"
 * reports). The binding pane is only a fallback for a shell started outside
 * a herdr pane, where HERDR_PANE_ID is unset.
 */
func herdrWtReapOwnPane(getenv func(string) string, binding HerdrSpaceBinding) string {
	if own := getenv("HERDR_PANE_ID"); own != "" {
		return own
	}
	return binding.GuestPaneID
}

// real reaper: Setsid (escapes herdr's process-group SIGKILL) and the binding
func herdrWtDetachedReapCmd(self string, binding HerdrSpaceBinding, devNull *os.File) *osexec.Cmd {
	return &osexec.Cmd{
		Path: self,
		Args: []string{"nexus-guest-shell"},
		Env: append(os.Environ(),
			herdrWtReapHandleEnv+"="+binding.SandboxHandle,
			herdrWtReapWorkspaceEnv+"="+binding.HerdrWorkspaceID,
			herdrWtReapPaneEnv+"="+herdrWtReapOwnPane(os.Getenv, binding),
			herdrWtReapSandboxIDEnv+"="+binding.SandboxID,
		),
		Stdin:       devNull,
		Stdout:      devNull,
		Stderr:      devNull,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
}

func herdrWtReapBindingFromEnv(getenv func(string) string) (HerdrSpaceBinding, bool) {
	handle := getenv(herdrWtReapHandleEnv)
	if handle == "" {
		return HerdrSpaceBinding{}, false
	}
	return HerdrSpaceBinding{
		SandboxHandle:    handle,
		HerdrWorkspaceID: getenv(herdrWtReapWorkspaceEnv),
		GuestPaneID:      getenv(herdrWtReapPaneEnv),
		SandboxID:        getenv(herdrWtReapSandboxIDEnv),
	}, true
}

// FAIL-OPEN throughout: every failure leaves the sandbox for the prune backstop.
func runHerdrWtDetachedReap(ctx context.Context, binding HerdrSpaceBinding) {
	time.Sleep(herdrWtReapSettle)

	remaining, err := herdrWtPaneListerFn(ctx, binding.HerdrWorkspaceID, binding.GuestPaneID)
	if err != nil {
		// FAIL-OPEN: herdr unreachable, parse error, etc.
		slog.Warn("nexus-guest-shell: wt/ pane-list failed; skipping teardown (prune will catch it)",
			"handle", binding.SandboxHandle, "err", err)
		return
	}
	if remaining > 0 {
		return
	}

	storeRoot, storeErr := store.DefaultRoot()
	herdrBin, _ := resolveHerdrBin()
	if storeErr != nil {
		slog.Warn("nexus-guest-shell: wt/ store root unavailable; stopping VM without record checks",
			"handle", binding.SandboxHandle, "err", storeErr)
		if err := herdrWtSandboxStopperFn(ctx, binding.SandboxHandle, binding.SandboxID); err != nil {
			slog.Warn("nexus-guest-shell: wt/ sandbox stop failed; sandbox left running",
				"handle", binding.SandboxHandle, "err", err)
		}
		return
	}
	herdrWtTeardownFn(ctx, storeRoot, binding.SandboxHandle, herdrBin, binding.SandboxID)
}

/**
 * herdrWtTeardownFn is the last-pane action for a worktree sandbox. It STOPS
 * the VM and leaves the sandbox record, its binding and its named volumes in
 * place. It used to run the full teardown transaction (remove VM, close
 * workspace, delete binding), which turned every "close the last tab" —
 * including the ones a reaper bug (fixed 2026-09-19) mistook for the last
 * tab — into losing the running agent and its root disk. Removal is now the
 * job of the worktree.removed hook alone (prune --workspace); re-opening the
 * worktree rebinds the stopped sandbox and the guest shell starts it again.
 * sandboxID guards against acting on a sandbox that replaced the one this
 * pane belonged to.
 */
var herdrWtTeardownFn = func(ctx context.Context, storeRoot, handle, herdrBin, sandboxID string) {
	if err := herdrWtSandboxStopperFn(ctx, handle, sandboxID); err != nil {
		slog.Warn("nexus-guest-shell: wt/ stop on last pane failed (sandbox left running)", "handle", handle, "err", err)
	}
}

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

func herdrWtPaneLister(ctx context.Context, workspaceID, ownPaneID string) (int, error) {
	herdrBin, err := resolveHerdrBin()
	if err != nil {
		return 0, fmt.Errorf("wt/ pane-list: resolve herdr: %w", err)
	}
	if herdrBin == "" {
		return 0, fmt.Errorf("wt/ pane-list: herdr binary not available (HERDR_BIN_PATH unset and herdr not on PATH)")
	}
	out, cmdErr := osexec.CommandContext(ctx, herdrBin, "pane", "list", "--workspace", workspaceID).CombinedOutput()
	return parseWtPaneListRemaining(out, cmdErr, ownPaneID)
}

// (the sandbox SHOULD be reaped), NOT to a fail-open error. Any other command
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
	if cmdErr != nil {
		return 0, fmt.Errorf("wt/ pane-list: herdr pane list: %w: %s", cmdErr, strings.TrimSpace(string(out)))
	}
	return 0, fmt.Errorf("wt/ pane-list: parse output: %q", strings.TrimSpace(string(out)))
}

var herdrWtSandboxStopperFn = herdrWtSandboxStopper

/**
 * herdrWtSandboxStopper stops the worktree sandbox once its last pane is
 * gone. A sandbox that is not Running (already stopped, paused, mid-create)
 * is left alone, as is one whose ID no longer matches the pane's binding.
 */
func herdrWtSandboxStopper(ctx context.Context, handle, sandboxID string) error {
	svc, err := newSandboxService()
	if err != nil {
		return fmt.Errorf("wt/ sandbox stop: open service: %w", err)
	}
	sb, err := svc.Get(ctx, handle)
	if err != nil {
		return fmt.Errorf("wt/ sandbox stop: %w", err)
	}
	if sandboxID != "" && sb.ID.String() != sandboxID {
		return fmt.Errorf("wt/ sandbox stop: %s is now %s, pane belonged to %s; leaving it", handle, sb.ID, sandboxID)
	}
	if sb.State != domain.Running {
		return nil
	}
	_, err = svc.Stop(ctx, handle)
	return err
}

func herdrWtSandboxRemover(ctx context.Context, handle string) error {
	svc, err := newSandboxService()
	if err != nil {
		return fmt.Errorf("wt/ sandbox rm: open service: %w", err)
	}
	return svc.Remove(ctx, handle)
}
