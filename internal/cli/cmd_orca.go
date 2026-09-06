package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/vmcfg"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func init() {
	Register(Command{
		Name:    "orca",
		Summary: "VM lifecycle hooks invoked by the Orca VM-recipe (create|suspend|resume|destroy)",
		Run:     runOrca,
	})
}

// ── Orca contract types ───────────────────────────────────────────────────────
//
// `orca create` must print EXACTLY ONE JSON object on stdout (schema v1).
// Orca's ssh2 transporter spawns proxyCommand and pipes its stdio as the SSH
// socket; host/port in target are used only for %h/%p substitution.

type orcaConnectionTarget struct {
	Label        string `json:"label"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Username     string `json:"username"`
	ProxyCommand string `json:"proxyCommand"`
	// IdentityFile is the absolute host path to the ed25519 private key for this
	// sandbox. Orca's ssh2 client uses it to authenticate over the proxyCommand
	// pipe. Generated once per instanceID and stored at
	// ~/.local/share/nexus3/orca/<instanceID>/id_ed25519.
	IdentityFile string `json:"identityFile,omitempty"`
}

type orcaConnection struct {
	Type        string               `json:"type"`
	Target      orcaConnectionTarget `json:"target"`
	ProjectRoot string               `json:"projectRoot"`
}

type orcaUserData struct {
	SandboxID string `json:"sandboxId"`
}

// orcaCreateResult is the single JSON object emitted on stdout by `orca create`.
type orcaCreateResult struct {
	SchemaVersion int            `json:"schemaVersion"`
	Connection    orcaConnection `json:"connection"`
	UserData      orcaUserData   `json:"userData"`
}

// ── env helpers ───────────────────────────────────────────────────────────────

type orcaEnv struct {
	Mode          string // ORCA_VM_MODE
	InstanceID    string // ORCA_VM_INSTANCE_ID — stable per sandbox instance
	ProjectID     string // ORCA_PROJECT_ID
	WorkspaceID   string // ORCA_WORKSPACE_ID
	WorkspaceName string // ORCA_WORKSPACE_NAME
	RepoPath      string // ORCA_REPO_PATH
	RepoURL       string // ORCA_REPO_URL
	RepoBranch    string // ORCA_REPO_BRANCH
	RepoRef       string // ORCA_REPO_REF
	Version       string // ORCA_VERSION
}

func readOrcaEnv() orcaEnv {
	return orcaEnv{
		Mode:          os.Getenv("ORCA_VM_MODE"),
		InstanceID:    os.Getenv("ORCA_VM_INSTANCE_ID"),
		ProjectID:     os.Getenv("ORCA_PROJECT_ID"),
		WorkspaceID:   os.Getenv("ORCA_WORKSPACE_ID"),
		WorkspaceName: os.Getenv("ORCA_WORKSPACE_NAME"),
		RepoPath:      os.Getenv("ORCA_REPO_PATH"),
		RepoURL:       os.Getenv("ORCA_REPO_URL"),
		RepoBranch:    os.Getenv("ORCA_REPO_BRANCH"),
		RepoRef:       os.Getenv("ORCA_REPO_REF"),
		Version:       os.Getenv("ORCA_VERSION"),
	}
}

// ── pure connection-JSON builder ─────────────────────────────────────────────

// orcaProjectRoot returns the absolute guest path for the project checkout.
// The repo is cloned here by orcaCreate (ORCA-P3); Orca opens it as the
// workspace root.
func orcaProjectRoot(repoPath, workspaceName, instanceID string) string {
	repoName := filepath.Base(repoPath)
	if repoName == "" || repoName == "." || repoName == "/" {
		repoName = workspaceName
	}
	if repoName == "" {
		repoName = instanceID
	}
	return "/root/workspace/" + repoName
}

// buildOrcaConnectionJSON constructs the orcaCreateResult value for a booted
// sandbox. This is a pure function (no I/O, no side effects) extracted for
// unit testing.
//
// privKeyPath is the absolute host-side path to the ed25519 private key (set to
// "" in tests that don't exercise SSH auth).
//
// proxyCommand uses %h which the SSH client expands to target.Host (= sandboxID),
// yielding:  nexus3 ssh --stdio <sandboxID>
// This matches the cmd_ssh.go usage: `nexus3 ssh [--stdio] <sandbox-ref>`.
func buildOrcaConnectionJSON(instanceID, sandboxID, workspaceName, repoPath, privKeyPath string) orcaCreateResult {
	label := workspaceName
	if label == "" {
		label = instanceID
	}
	projectRoot := orcaProjectRoot(repoPath, workspaceName, instanceID)

	return orcaCreateResult{
		SchemaVersion: 1,
		Connection: orcaConnection{
			Type: "ssh",
			Target: orcaConnectionTarget{
				Label:        label,
				Host:         sandboxID,
				Port:         22,
				Username:     "root",
				ProxyCommand: "nexus3 ssh --stdio %h",
				IdentityFile: privKeyPath,
			},
			ProjectRoot: projectRoot,
		},
		UserData: orcaUserData{SandboxID: sandboxID},
	}
}

// orcaWorkspaceSpec returns a WorkspaceSpec that captures the Orca-managed
// host git worktree (ORCA_REPO_PATH) for delivery into the sandbox, or nil
// when ORCA_REPO_PATH is absent or the path does not exist on disk (e.g. no
// repo is configured in the Orca workspace).
//
// The GuestPath mirrors orcaProjectRoot so the connection.projectRoot emitted
// to Orca is the directory that actually receives the workspace files.
func orcaWorkspaceSpec(env orcaEnv) *service.WorkspaceSpec {
	if env.RepoPath == "" {
		return nil
	}
	if _, err := os.Stat(env.RepoPath); err != nil {
		return nil
	}
	return buildWorkspaceSpec(
		env.RepoPath,
		orcaProjectRoot(env.RepoPath, env.WorkspaceName, env.InstanceID),
		0, // AUTO: free-space-derived cap (zero means auto in WorktreeToDisk).
	)
}

// gitHostsFromURL extracts extra egress hostnames for a git repo URL so an
// orca sandbox can fetch from non-GitHub forges. GitHub hosts are NEVER
// returned (D-PD-23 / N-AC1): the orca path is an agent sandbox and stays
// dark. A future --secret bind on a human/git sandbox is the only way
// github.com enters AllowedHosts. Returns nil for empty, unparseable, or
// GitHub URLs.
func gitHostsFromURL(repoURL string) []string {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		return nil
	}
	host := u.Hostname() // strips port if present
	if domain.IsGitHubHost(host) {
		return nil
	}
	return []string{host}
}

// ── SSH keypair helpers ───────────────────────────────────────────────────────

// orcaKeyDir returns ~/.local/share/nexus3/orca/<instanceID>, the directory
// where the per-instance SSH keypair is stored for cross-reconnect reuse.
func orcaKeyDir(instanceID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("orca keypair: home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share", "nexus3", "orca", instanceID), nil
}

// generateOrLoadOrcaKeypair returns the ed25519 keypair for instanceID,
// generating and persisting it on first call. Returns (publicKeyLine, privateKeyPEM, err).
// The public key is in OpenSSH authorized_keys format; the private key is in
// OpenSSH PEM format for use by the SSH client.
func generateOrLoadOrcaKeypair(instanceID string) (string, string, error) {
	dir, err := orcaKeyDir(instanceID)
	if err != nil {
		return "", "", err
	}
	privPath := filepath.Join(dir, "id_ed25519")
	pubPath := filepath.Join(dir, "id_ed25519.pub")

	// Load existing keypair if both files are present and readable.
	if pubBytes, err := os.ReadFile(pubPath); err == nil {
		if privBytes, err := os.ReadFile(privPath); err == nil {
			return strings.TrimRight(string(pubBytes), "\n"), string(privBytes), nil
		}
	}

	// Generate a fresh ed25519 keypair via the service helper.
	pubKey, privKey, err := service.GenerateEphemeralSSHKeypair()
	if err != nil {
		return "", "", fmt.Errorf("orca keypair: generate: %w", err)
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", fmt.Errorf("orca keypair: mkdir: %w", err)
	}
	if err := os.WriteFile(privPath, []byte(privKey), 0600); err != nil {
		return "", "", fmt.Errorf("orca keypair: write private key: %w", err)
	}
	if err := os.WriteFile(pubPath, []byte(pubKey+"\n"), 0644); err != nil {
		return "", "", fmt.Errorf("orca keypair: write public key: %w", err)
	}
	return pubKey, privKey, nil
}

// ── sandbox name helpers ──────────────────────────────────────────────────────

// orcaSandboxName derives a stable, valid sandbox name from an Orca instance ID.
// UUID chars (hex digits and hyphens) pass through unchanged; others become hyphens.
func orcaSandboxName(instanceID string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(instanceID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	s := strings.Trim(sb.String(), "-")
	if len(s) > 48 {
		s = s[:48]
	}
	if s == "" {
		s = "instance"
	}
	return s
}

// ── workspace sync helpers ────────────────────────────────────────────────────

// orcaWorkspaceSyncScript returns the shell script that mounts the workspace
// disk read-only and copies its contents to guestPath in the rootfs.
//
// The device path is derived from WorkspaceGuestMount rather than hardcoded:
// the workspace disk occupies ExtraDisks[numShadowDisks], mapping to
// /dev/vd{b+numShadowDisks}. Callers pass the actual shadow-disk count so
// the device letter always matches the real disk layout even when shadow disks
// are added in the future.
func orcaWorkspaceSyncScript(guestPath string, numShadowDisks int) string {
	device := WorkspaceGuestMount(guestPath, numShadowDisks).Device
	return fmt.Sprintf(
		"set -e; mkdir -p /mnt/__ws_src %s; "+
			"mount -t ext4 -o ro %s /mnt/__ws_src; "+
			"cp -a /mnt/__ws_src/. %s/; "+
			"umount /mnt/__ws_src",
		guestPath, device, guestPath)
}

// orcaSyncWorkspace mounts the workspace disk inside the running sandbox
// identified by sandboxID, copies all files to ws.GuestPath in the rootfs,
// then unmounts. Returns an error if the mount/copy fails.
//
// Callers must treat any returned error as fatal: booting the agent against
// an empty workspace is strictly worse than failing the create step.
func orcaSyncWorkspace(ctx context.Context, svc *service.Service, sandboxID string, ws *service.WorkspaceSpec, numShadowDisks int) error {
	syncCtx, syncCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer syncCancel()
	script := orcaWorkspaceSyncScript(ws.GuestPath, numShadowDisks)
	var buf bytes.Buffer
	code, err := svc.Exec(syncCtx, sandboxID, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", script},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stderr: &buf,
	})
	if err != nil || code != 0 {
		device := WorkspaceGuestMount(ws.GuestPath, numShadowDisks).Device
		return fmt.Errorf("mount/copy workspace disk (%s) to %s failed (exit %d, err %w): %s",
			device, ws.GuestPath, code, err, buf.String())
	}
	return nil
}

// ── supervisor spawn helper ───────────────────────────────────────────────────

// buildOrcaSpawnConfig constructs the supervisor.SpawnConfig for an orca
// create handoff. Extracted for unit-testability: the forward-trace test
// can verify that realistic orca create inputs produce a SpawnConfig with
// GovBounds, ExtraDisks, HasWorkspaceDisk, WorkspaceDiskIndex, and Cmdline
// all set — without actually forking a subprocess or booting a VM.
//
// extraDiskPaths are the host paths of the extra disks captured by the
// DriverFactory closure (workspace disk, etc.). workspaceDiskIndex must be
// derived from orcaNumShadowDisks (= 0 on the orca path), not hardcoded.
//
// guestPath is the in-guest directory where the workspace disk is mounted
// (e.g. "/root/workspace/myrepo"). When non-empty and hasWorkspaceDisk is
// true, buildOrcaSpawnConfig generates a --workspace-mount= cmdline so the
// supervisor-owned VM boots with disk telemetry active (DiskSupported=true).
// Passing empty string skips workspace mount generation (disk axis blind).
func buildOrcaSpawnConfig(
	sandboxID, sandboxHandle, storeRoot, stateDir, chBin, socketDir, kernelPath, capturedDiskPath string,
	extraDiskPaths []string,
	govBounds resize.Bounds,
	bootVCPUs uint32,
	hasWorkspaceDisk bool,
	workspaceDiskIndex int,
	credsFile string,
	guestPath string,
	hasScratchDisk bool,
) supervisor.SpawnConfig {
	// Build the kernel cmdline for the supervisor-owned VM. The supervisor
	// reboots the VM independently (CLI stops the initial boot before handoff),
	// so it must reproduce the same cmdline the CLI built at CreateAndBoot time.
	//
	// Two concerns are composed here, matching the logic in cmd_sandbox.go.
	//
	// The driver independently inserts memhp kernel params (memhp_default_state=
	// online etc.) before "--" when MemoryMaxMiB > 0 (supervisor.go derives it
	// from GovBounds.MemMaxBytes). This function only builds the PID-1 section.
	// Auto-resize is unconditional (D-DC-30 revised 2026-08-14).
	memMaxMiB := uint32(govBounds.MemMaxBytes / (1024 * 1024)) //nolint:gosec // bytes→MiB, fits uint32
	// PID-1 auto-resize args come from vmcfg.Resolve so there is a single
	// source of truth for the cmdline fragment (leading space included).
	arArgs := vmcfg.Resolve(vmcfg.Config{MemMaxMiB: memMaxMiB}).PID1Args

	// One workspace mount, no shadow disks on the orca path (orcaNumShadowDisks=0).
	// No mounts when there is no workspace disk or the guest path is unknown.
	var mounts []agent.GuestMount
	if hasWorkspaceDisk && guestPath != "" {
		mounts = []agent.GuestMount{WorkspaceGuestMount(guestPath, workspaceDiskIndex)}
	}
	// scratchIdx is -1 unless scratch was explicitly attached (hasScratchDisk).
	// Do NOT infer from hasWorkspaceDisk: NoScratchDisk=true sandboxes have
	// a workspace disk but no scratch disk; a wrong index causes mkfs on the
	// workspace volume (D-SD-01). Invariant when true: scratch is len(ExtraDisks)-1 (D-DC-32).
	scratchIdx := -1
	if hasScratchDisk {
		scratchIdx = len(extraDiskPaths) - 1
	}
	cmdline := guestBootCmdline(mounts, arArgs, sandboxHandle, scratchIdx)

	return supervisor.SpawnConfig{
		Config: supervisor.Config{
			SandboxRef: sandboxID,
			StoreRoot:  storeRoot,
			StateDir:   stateDir,
			CHBin:      chBin,
			SocketDir:  socketDir,
			KernelPath: kernelPath,
			DiskPath:   capturedDiskPath,
			// S3 owns CredsFile / broker wiring; pass the path so the supervisor
			// can load it when S3 wires the broker. No-op if file is absent.
			CredsFile:          credsFile,
			ExtraDisks:         extraDiskPaths,
			GovBounds:          govBounds,
			BootVCPUs:          bootVCPUs,
			HasWorkspaceDisk:   hasWorkspaceDisk,
			WorkspaceDiskIndex: workspaceDiskIndex,
			Cmdline:            cmdline,
		},
		ReadyTimeout: 5 * time.Minute,
	}
}

// ── sandbox lookup helper ─────────────────────────────────────────────────────

// orcaByInstanceID resolves a sandbox by ORCA_VM_INSTANCE_ID (its MotiveID).
// Returns an error if no sandbox is associated with the given instance.
func orcaByInstanceID(ctx context.Context, svc *service.Service, instanceID string) (domain.Sandbox, error) {
	sbs, err := svc.GetByMotive(ctx, instanceID)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("orca: lookup instance %q: %w", instanceID, err)
	}
	if len(sbs) == 0 {
		return domain.Sandbox{}, fmt.Errorf("orca: no sandbox found for ORCA_VM_INSTANCE_ID=%q", instanceID)
	}
	return sbs[0], nil
}

// ── dispatcher ───────────────────────────────────────────────────────────────

func runOrca(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "orca: missing subcommand; usage: orca <create|suspend|resume|destroy>"}
	}
	remote, rest, rerr := resolveOrcaRemote(args)
	if rerr != nil {
		return &UsageError{Msg: "orca: " + rerr.Error()}
	}
	if len(rest) == 0 {
		return &UsageError{Msg: "orca: missing subcommand; usage: orca <create|suspend|resume|destroy>"}
	}
	if remote != nil {
		switch rest[0] {
		case "create":
			return orcaCreateRemote(ctx, out.w, remote)
		case "suspend", "resume", "destroy":
			return orcaLifecycleRemote(ctx, rest[0], remote)
		default:
			return &UsageError{Msg: fmt.Sprintf("orca: unknown subcommand %q", rest[0])}
		}
	}
	args = rest
	switch args[0] {
	case "create":
		return orcaCreate(ctx, out.w)
	case "suspend":
		return orcaSuspend(ctx)
	case "resume":
		return orcaResume(ctx)
	case "destroy":
		return orcaDestroy(ctx)
	default:
		return &UsageError{Msg: fmt.Sprintf("orca: unknown subcommand %q; valid: create suspend resume destroy", args[0])}
	}
}

// ── create ────────────────────────────────────────────────────────────────────

// orcaCreate provisions a new sandbox, hands VM ownership to a detached
// supervisor (D-PP-01 S2), and prints the Orca connection JSON on w.
//
// ORCA_VM_INSTANCE_ID is used as the sandbox's MotiveID so that suspend/
// resume/destroy can locate it via service.GetByMotive across separate
// process invocations.
//
// Idempotent: if GetByMotive returns an existing sandbox for this instance,
// the existing connection JSON is returned without booting a new VM.
func orcaCreate(ctx context.Context, w io.Writer) error {
	env := readOrcaEnv()
	if env.InstanceID == "" {
		return fmt.Errorf("orca create: ORCA_VM_INSTANCE_ID is not set")
	}

	// SSH keypair: generate-or-load BEFORE the idempotency check so that
	// privKeyPath is available regardless of which branch we take.
	pubKey, _, err := generateOrLoadOrcaKeypair(env.InstanceID)
	if err != nil {
		return fmt.Errorf("orca create: %w", err)
	}
	keyDir, err := orcaKeyDir(env.InstanceID)
	if err != nil {
		return fmt.Errorf("orca create: %w", err)
	}
	privKeyPath := filepath.Join(keyDir, "id_ed25519")

	svc, err := newSandboxService()
	if err != nil {
		return fmt.Errorf("orca create: %w", err)
	}

	// Idempotency: if a sandbox already exists for this instance, return its JSON.
	// Supervisor liveness check:
	//   - Live supervisor → re-adopt (return connection JSON without spawning a second).
	//   - Stale supervisor (PID dead) → clean up pid/sock files, clear store fields,
	//     then return the connection JSON (sandbox record preserved; user may restart).
	//   - No supervisor recorded → return connection JSON as-is.
	if existing, err := svc.GetByMotive(ctx, env.InstanceID); err == nil && len(existing) > 0 {
		sb := existing[0]
		if sb.SupervisorPID > 0 {
			alive, _ := supervisor.CheckAndReconcile(sb.SupervisorPID, sb.SupervisorSock)
			if alive {
				slog.Info("orca create: live supervisor re-adopted",
					"sandbox", sb.ID, "pid", sb.SupervisorPID, "sock", sb.SupervisorSock)
			} else {
				slog.Warn("orca create: stale supervisor detected; cleaning up",
					"sandbox", sb.ID, "pid", sb.SupervisorPID)
				// Best-effort: clear the stale supervisor fields so destroy
				// doesn't try to stop a process that no longer exists.
				if clearErr := svc.ClearSupervisor(ctx, sb.ID); clearErr != nil {
					slog.Warn("orca create: clear stale supervisor fields", "err", clearErr)
				}
			}
		}
		result := buildOrcaConnectionJSON(env.InstanceID, sb.ID.String(), env.WorkspaceName, env.RepoPath, privKeyPath)
		return json.NewEncoder(w).Encode(result)
	}

	// Image to boot. Override via NEXUS3_IMAGE; default to the standard base.
	//
	// Image requirement (ORCA-P3): the image must include git, sshd, claude-code.
	// The default "nexus3-base:latest" may lack these tools.
	imageRef := os.Getenv("NEXUS3_IMAGE")
	if imageRef == "" {
		imageRef = "nexus3-base:latest"
	}

	// Preflight: validate the kernel path before store/cache/VM setup so that a
	// missing/misconfigured NEXUS3_KERNEL_PATH surfaces immediately with an
	// actionable error rather than after expensive work inside CreateAndBoot.
	kernelPath, err := resolveKernelPath()
	if err != nil {
		return fmt.Errorf("orca create: %w", err)
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return fmt.Errorf("orca create: store root: %w", err)
	}
	cacheRoot := filepath.Join(storeRoot, "images")

	imgCache, err := image.NewCache(cacheRoot)
	if err != nil {
		return fmt.Errorf("orca create: image cache: %w", err)
	}

	// ── Resolve socketDir ─────────────────────────────────────────────────────
	// Must match the formula in cloudhypervisor.defaultSocketDir so that:
	//   (a) the initial CreateAndBoot factory and
	//   (b) the detached supervisor (passed as --socket-dir) and
	//   (c) newSandboxService()'s driver (default)
	// all resolve to the same directory. This is what makes svc.Exec work after
	// the supervisor owns the VM.
	socketDir, err := orcaSocketDir()
	if err != nil {
		return fmt.Errorf("orca create: resolve socket dir: %w", err)
	}

	// ── stateDir for supervisor.pid / supervisor.sock ─────────────────────────
	// Use /tmp so the path is guaranteed short (supervisor.sock fits in 107-byte
	// AF_UNIX sun_path even on systems with long $HOME).
	stateDir, err := os.MkdirTemp("/tmp", "nexus3-sv-")
	if err != nil {
		return fmt.Errorf("orca create: create supervisor state dir: %w", err)
	}

	// ── cloud-hypervisor binary ────────────────────────────────────────────────
	chBin, _ := exec.LookPath("cloud-hypervisor")

	// ── DriverFactory ─────────────────────────────────────────────────────────
	// capturedDiskPath: the CoW ext4 copy path forwarded to the supervisor.
	// capturedExtraDisks: the extra disk paths (workspace disk, etc.) to
	// re-attach when the supervisor re-boots the VM. Captured here so the
	// supervisor's cloudhypervisor.New call can pass the same disks.
	var capturedDiskPath string
	var capturedExtraDisks []string
	newDriver := service.DriverFactory(func(ext4Path string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		capturedDiskPath = ext4Path
		capturedExtraDisks = nil // reset on each call (CreateAndBoot calls once)
		cfg := buildCHConfig(kernelPath, ext4Path, 0, 0)
		cfg.SocketDir = socketDir // explicit so it matches supervisor + svc
		if chBin != "" {
			cfg.BinaryPath = chBin
		}
		for _, ed := range extraDisks {
			cfg.ExtraDisks = append(cfg.ExtraDisks, cloudhypervisor.ExtraDisk{Path: ed.Path})
			capturedExtraDisks = append(capturedExtraDisks, ed.Path)
		}
		return cloudhypervisor.New(cfg)
	})

	// Probe: wait for the guest agent control port to answer via vsock.
	probe := func(pCtx context.Context, drv driver.Driver, id domain.SandboxID) error {
		gd, ok := drv.(driver.GuestDialer)
		if !ok {
			return nil
		}
		for {
			if pCtx.Err() != nil {
				return pCtx.Err()
			}
			dialCtx, cancel := context.WithTimeout(pCtx, 2*time.Second)
			conn, dialErr := gd.DialGuest(dialCtx, id, driver.AgentControlPort)
			cancel()
			if dialErr == nil {
				_ = conn.Close()
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	// Frozen egress allowlist: AllowedHosts is stored in the sandbox Envelope at
	// creation time and is read back by the detached supervisor when it calls
	// svc.Start. Without this, Envelope.AllowedHosts is empty and the perimeter
	// netfilter is default-deny for all outbound traffic (including api.anthropic.com).
	// Base set: AgentEgressHosts(cred.ClaudeCodeProfile) (api.anthropic.com + platform.claude.com).
	// Non-GitHub forges from the recipe URL may be appended. GitHub hosts are
	// never added here (D-PD-23): orca is an agent path.
	allowedHosts := append(service.AgentEgressHosts(cred.ClaudeCodeProfile), gitHostsFromURL(env.RepoURL)...)

	// Initial boot opts: AllowedHosts is frozen here so the detached supervisor
	// inherits the correct perimeter allowlist when it re-boots the VM.
	// SSHPublicKey is frozen at creation so the supervisor-owned VM also has it
	// in the guest's authorized_keys (seeded below via shadow driver).
	// Workspace captures the Orca host worktree (ORCA_REPO_PATH) to an ext4
	// image and attaches it in the initial boot so the sync step below can copy
	// the files to the rootfs before the supervisor takes over. The device is
	// derived via WorkspaceGuestMount (currently /dev/vdb, since the orca path
	// attaches no shadow disks) — never hardcode it, or adding shadow disks
	// here would silently mount the wrong volume.
	opts := service.CreateAndBootOptions{
		Labels:              map[string]string{"motive": env.InstanceID},
		Image:               service.ImageSpec{Ref: imageRef},
		CacheRoot:           cacheRoot,
		ReachabilityTimeout: 120 * time.Second,
		SSHPublicKey:        pubKey,
		AllowedHosts:        allowedHosts,
		Workspace:           orcaWorkspaceSpec(env),
		// Record which agent this sandbox serves. The credential seed itself is
		// the detached supervisor's job (UseAgentSeed stays false), but without
		// the profile here the record carries no agent and the perimeter cannot
		// tell an agent sandbox apart from a plain one.
		AgentProfile: cred.ClaudeCodeProfile,
	}

	name := orcaSandboxName(env.InstanceID)
	sb, err := service.CreateAndBoot(ctx, svc, imgCache, newDriver, probe, "orca", name, opts)
	if err != nil {
		return fmt.Errorf("orca create: boot sandbox: %w", err)
	}
	slog.Info("orca create: initial boot done; handing off to supervisor", "sandbox", sb.ID)

	// orcaNumShadowDisks is the count of shadow disks preceding the workspace
	// disk on the orca path. The orca path attaches no shadow disks, so the
	// workspace disk is always ExtraDisks[0] → /dev/vdb. This is the canonical
	// derivation source for WorkspaceDiskIndex passed to the supervisor.
	const orcaNumShadowDisks = 0

	// ── Sync workspace to rootfs before supervisor handoff ────────────────────
	// When ORCA_REPO_PATH is set, opts.Workspace captured the host worktree to
	// an ext4 image attached as ExtraDisks[orcaNumShadowDisks].
	// orcaSyncWorkspace derives the device from WorkspaceGuestMount so the
	// letter tracks the actual disk layout rather than being hardcoded.
	//
	// After sync the workspace disk is kept attached (capturedExtraDisks carries
	// it forward to the supervisor). This gives the disk auto-resize axis a valid
	// virtio-blk target (/dev/vd{b+orcaNumShadowDisks}) throughout the VM's life.
	//
	// Failure is a hard error: booting the agent against an empty workspace is
	// strictly worse than refusing to boot.
	if ws := opts.Workspace; ws != nil {
		if syncErr := orcaSyncWorkspace(ctx, svc, sb.ID.String(), ws, orcaNumShadowDisks); syncErr != nil {
			// Best-effort stop: halt the running VM before returning so it
			// does not remain as a live orphan. Consistent with how the
			// surrounding code handles other post-boot failures.
			if _, stopErr := svc.Stop(ctx, sb.ID.String()); stopErr != nil {
				slog.Warn("orca create: stop after sync failure", "stopErr", stopErr)
			}
			return fmt.Errorf("orca create: workspace sync to rootfs: %w", syncErr)
		}
		slog.Info("orca create: workspace synced to rootfs",
			"src", env.RepoPath,
			"guestPath", ws.GuestPath)
	} else {
		slog.Info("orca create: ORCA_REPO_PATH not set or nonexistent; skipping workspace capture",
			"repoPath", env.RepoPath)
	}

	// ── Stop the initial boot — supervisor re-boots with perimeter ────────────
	if _, stopErr := svc.Stop(ctx, sb.ID.String()); stopErr != nil {
		return fmt.Errorf("orca create: stop before supervisor handoff: %w", stopErr)
	}

	// Auto-resize bounds for the orca supervisor governor. The detached supervisor
	// is the exclusive governor host (D-DC-12). vmcfg.Resolve with zero inputs
	// applies the documented defaults:
	// MemMin=512MiB, MemMax=4096MiB, VCPUMin=1, VCPUMax=4, DiskMax=100GiB.
	govBounds := vmcfg.Resolve(vmcfg.Config{}).Bounds

	// ── SpawnDetached: supervisor takes ownership of VM + perimeter ───────────
	// Resolve the guest path so buildOrcaSpawnConfig can embed a
	// --workspace-mount= arg in the cmdline; without it the supervisor-owned
	// VM boots without workspace mount info and disk telemetry is permanently
	// blind (DiskSupported=false, selectWorkspaceMount returns ok=false).
	var guestWorkspacePath string
	if opts.Workspace != nil {
		guestWorkspacePath = opts.Workspace.GuestPath
	}
	spawnCfg := buildOrcaSpawnConfig(
		sb.ID.String(), sb.Handle(), storeRoot, stateDir, chBin, socketDir, kernelPath, capturedDiskPath,
		capturedExtraDisks,
		govBounds,
		1, // bootVCPUs: orca create passes 0 to buildCHConfig → driver default = 1
		opts.Workspace != nil,
		orcaNumShadowDisks,
		service.DedicatedCredStorePathForProfile(cred.ClaudeCodeProfile),
		guestWorkspacePath,
		// hasScratchDisk mirrors service/create.go step 4.9: scratch is attached
		// iff workspace was requested and NoScratchDisk was not set (orca never
		// sets NoScratchDisk, so workspace presence is sufficient here).
		opts.Workspace != nil,
	)
	// Non-ephemeral supervisor: watchdog pipe is nil (orca sandbox persists after CLI exit).
	pid, _, err := supervisor.SpawnDetached(spawnCfg)
	if err != nil {
		return fmt.Errorf("orca create: spawn supervisor: %w", err)
	}
	sockPath := supervisor.SockPath(stateDir)
	slog.Info("orca create: supervisor ready", "pid", pid, "sock", sockPath)

	// ── Persist SupervisorPID + SupervisorSock onto sandbox record ────────────
	// Must happen before SSH seeding so that a partial failure still leaves
	// destroy able to find and tear down the supervisor.
	if err := svc.SetSupervisor(ctx, sb.ID, pid, sockPath); err != nil {
		return fmt.Errorf("orca create: persist supervisor info: %w", err)
	}

	// ── SSH authorized_keys seeding via shadow driver ─────────────────────────
	// The supervisor now owns the VM. Create a shadow CHDriver that talks to
	// the same API/vsock sockets (same socketDir) without owning the process.
	if pubKey != "" {
		shadowCfg := buildCHConfig(kernelPath, capturedDiskPath, 0, 0)
		shadowCfg.SocketDir = socketDir
		if chBin != "" {
			shadowCfg.BinaryPath = chBin
		}
		shadowDrv, shadowErr := cloudhypervisor.New(shadowCfg)
		if shadowErr == nil {
			if gd, ok := driver.Driver(shadowDrv).(driver.GuestDialer); ok {
				// Wait for agent under the supervisor-owned VM.
				waitCtx, waitCancel := context.WithTimeout(ctx, 60*time.Second)
				defer waitCancel()
				waitForOrcaAgent(waitCtx, gd, sb.ID)

				agentClient := agent.NewClient(gd, sb.ID)
				sshSeeder := service.NewAgentSSHKeyCopySeeder(agentClient)
				seedCtx, seedCancel := context.WithTimeout(ctx, 15*time.Second)
				defer seedCancel()
				if seedErr := service.SeedSSHAuthorizedKeys(seedCtx, pubKey, sb.ID, sshSeeder); seedErr != nil {
					slog.Warn("orca create: seed SSH authorized_keys", "err", seedErr)
				}
			}
		} else {
			slog.Warn("orca create: shadow driver init failed; SSH seeding skipped", "err", shadowErr)
		}
	}

	result := buildOrcaConnectionJSON(env.InstanceID, sb.ID.String(), env.WorkspaceName, env.RepoPath, privKeyPath)
	return json.NewEncoder(w).Encode(result)
}

// orcaSocketDir returns the directory used for per-sandbox Cloud Hypervisor
// API and vsock sockets. It mirrors cloudhypervisor.defaultSocketDir so that
// the initial CreateAndBoot factory, the detached supervisor (--socket-dir),
// and newSandboxService()'s CHDriver all resolve to the same path.
func orcaSocketDir() (string, error) {
	var dir string
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		dir = filepath.Join(d, "nexus3")
	} else {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("nexus3-%d", os.Getuid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("orca socket dir %s: %w", dir, err)
	}
	return dir, nil
}

// waitForOrcaAgent polls the guest agent control port via vsock until it
// answers or ctx expires. Non-fatal: caller logs a warning if ctx times out.
func waitForOrcaAgent(ctx context.Context, gd driver.GuestDialer, id domain.SandboxID) {
	for {
		if ctx.Err() != nil {
			slog.Warn("orca create: timed out waiting for guest agent under supervisor", "sandbox", id)
			return
		}
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := gd.DialGuest(dialCtx, id, driver.AgentControlPort)
		cancel()
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// ── suspend ───────────────────────────────────────────────────────────────────

func orcaSuspend(ctx context.Context) error {
	instanceID := os.Getenv("ORCA_VM_INSTANCE_ID")
	if instanceID == "" {
		return fmt.Errorf("orca suspend: ORCA_VM_INSTANCE_ID is not set")
	}
	svc, err := newSandboxService()
	if err != nil {
		return fmt.Errorf("orca suspend: %w", err)
	}
	sb, err := orcaByInstanceID(ctx, svc, instanceID)
	if err != nil {
		return fmt.Errorf("orca suspend: %w", err)
	}
	if _, err := svc.Pause(ctx, sb.ID.String()); err != nil {
		return fmt.Errorf("orca suspend: pause %s: %w", sb.ID, err)
	}
	return nil
}

// ── resume ────────────────────────────────────────────────────────────────────

func orcaResume(ctx context.Context) error {
	instanceID := os.Getenv("ORCA_VM_INSTANCE_ID")
	if instanceID == "" {
		return fmt.Errorf("orca resume: ORCA_VM_INSTANCE_ID is not set")
	}
	svc, err := newSandboxService()
	if err != nil {
		return fmt.Errorf("orca resume: %w", err)
	}
	sb, err := orcaByInstanceID(ctx, svc, instanceID)
	if err != nil {
		return fmt.Errorf("orca resume: %w", err)
	}
	if _, err := svc.Resume(ctx, sb.ID.String()); err != nil {
		return fmt.Errorf("orca resume: resume %s: %w", sb.ID, err)
	}
	return nil
}

// ── destroy ───────────────────────────────────────────────────────────────────

func orcaDestroy(ctx context.Context) error {
	instanceID := os.Getenv("ORCA_VM_INSTANCE_ID")
	if instanceID == "" {
		return fmt.Errorf("orca destroy: ORCA_VM_INSTANCE_ID is not set")
	}
	svc, err := newSandboxService()
	if err != nil {
		return fmt.Errorf("orca destroy: %w", err)
	}
	sb, err := orcaByInstanceID(ctx, svc, instanceID)
	if err != nil {
		return fmt.Errorf("orca destroy: %w", err)
	}

	// Signal the supervisor (and its VM + perimeter) to shut down gracefully
	// before removing the sandbox record and disk. The supervisor calls
	// svc.Stop internally so the sandbox transitions to Stopped before Remove.
	if sb.SupervisorSock != "" {
		if stopErr := supervisor.StopSupervisor(ctx, sb.SupervisorSock); stopErr != nil {
			// Supervisor may already be gone (crash, manual kill). Log and proceed
			// with Remove; the disk + record cleanup must still happen.
			slog.Warn("orca destroy: StopSupervisor",
				"sock", sb.SupervisorSock, "pid", sb.SupervisorPID, "err", stopErr)
		} else {
			slog.Info("orca destroy: supervisor stopped",
				"sock", sb.SupervisorSock, "pid", sb.SupervisorPID)
		}
	}

	if err := svc.Remove(ctx, sb.ID.String()); err != nil {
		return fmt.Errorf("orca destroy: remove %s: %w", sb.ID, err)
	}
	return nil
}
