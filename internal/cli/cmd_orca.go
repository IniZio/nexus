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
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
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

// buildGitCloneArgv returns the argv for a /bin/sh -c git-clone that clones
// repoURL at repoRef (falling back to repoBranch) into destDir. Returns nil
// when repoURL is empty. PATH is not set here — callers must pass it via
// agent.ExecOptions.Env so that /bin/sh can resolve git.
func buildGitCloneArgv(repoURL, repoRef, repoBranch, destDir string) []string {
	if repoURL == "" {
		return nil
	}
	ref := repoRef
	if ref == "" {
		ref = repoBranch
	}
	var sb strings.Builder
	sb.WriteString("git clone --depth=1")
	if ref != "" {
		sb.WriteString(" --branch ")
		sb.WriteString(ref)
	}
	sb.WriteString(" -- ")
	sb.WriteString(repoURL)
	sb.WriteString(" ")
	sb.WriteString(destDir)
	return []string{"/bin/sh", "-c", sb.String()}
}

// gitHostsFromURL extracts the egress allowlist hostnames for a git repo URL.
// For github.com, codeload.github.com (the CDN used for git-pack downloads) is
// included. Returns nil for unrecognised or unparseable URLs.
func gitHostsFromURL(repoURL string) []string {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		return nil
	}
	host := u.Hostname() // strips port if present
	if host == "github.com" {
		return []string{"github.com", "codeload.github.com"}
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

// orcaCreate boots a new sandbox and prints the Orca connection JSON on w.
// ORCA_VM_INSTANCE_ID is used as the sandbox's MotiveID so that suspend/resume/
// destroy can locate it via service.GetByMotive across separate process invocations.
//
// Idempotent: if GetByMotive returns an existing sandbox for this instance, the
// existing connection JSON is returned without booting a new VM.
func orcaCreate(ctx context.Context, w io.Writer) error {
	env := readOrcaEnv()
	if env.InstanceID == "" {
		return fmt.Errorf("orca create: ORCA_VM_INSTANCE_ID is not set")
	}

	// SSH keypair: generate-or-load BEFORE the idempotency check so that
	// privKeyPath is available regardless of which branch we take.
	// The public key is injected into the guest via SSHPublicKey; the private key
	// path is emitted in the connection JSON as identityFile so Orca's ssh2 client
	// authenticates over the proxyCommand pipe.
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
	if existing, err := svc.GetByMotive(ctx, env.InstanceID); err == nil && len(existing) > 0 {
		sb := existing[0]
		result := buildOrcaConnectionJSON(env.InstanceID, sb.ID.String(), env.WorkspaceName, env.RepoPath, privKeyPath)
		return json.NewEncoder(w).Encode(result)
	}

	// Image to boot. Override via NEXUS3_IMAGE; default to the standard base.
	//
	// Image requirement (ORCA-P3): the image must include:
	//   - git          (for repo checkout below)
	//   - sshd         (for Orca's SSH connection)
	//   - claude-code  (for Orca's in-guest agent execution)
	// The default "nexus3-base:latest" may lack these tools. Set NEXUS3_IMAGE
	// to an image built with internal/core/builder (BuildAgentBaseImage) plus
	// git and openssh-server installed. The post-boot git check below will warn
	// at runtime if git is absent.
	imageRef := os.Getenv("NEXUS3_IMAGE")
	if imageRef == "" {
		imageRef = "nexus3-base:latest"
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

	kernelPath := kernelPathFor()
	newDriver := service.DriverFactory(func(ext4Path string) (driver.Driver, error) {
		cfg := buildCHConfig(kernelPath, ext4Path, 0, 0)
		if p, err := exec.LookPath("cloud-hypervisor"); err == nil {
			cfg.BinaryPath = p
		}
		return cloudhypervisor.New(cfg)
	})

	// Probe: wait for the guest agent control port to answer via vsock.
	// Mirrors the probe in cmd_herdr_plugin.go and cmd_sandbox.go exactly.
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

	// Egress perimeter for git clone: when ORCA_REPO_URL is set, attach a broker
	// and set AllowedHosts so the service's perimeter supervisor allows outbound
	// HTTPS to the repo host. For public repos no credential injection is needed —
	// the MITM proxy forwards traffic without rewriting Authorization headers.
	//
	// Egress gap: the perimeter supervisor only activates when svc.WithBroker is
	// set AND the cloud-hypervisor driver implements driver.NetworkHook. Operator
	// must ensure the cloud-hypervisor build supports the vsock-tap network stack.
	// If the guest VM has unrestricted outbound access by default (no perimeter
	// configured), the git clone will also work without this wiring.
	opts := service.CreateAndBootOptions{
		MotiveID:            env.InstanceID,
		Image:               service.ImageSpec{Ref: imageRef},
		CacheRoot:           cacheRoot,
		ReachabilityTimeout: 120 * time.Second,
		SSHPublicKey:        pubKey,
	}
	if env.RepoURL != "" {
		broker := cred.NewBroker()
		svc = svc.WithBroker(broker)
		opts.Broker = broker
		opts.AllowedHosts = gitHostsFromURL(env.RepoURL)
	}

	name := orcaSandboxName(env.InstanceID)
	sb, err := service.CreateAndBoot(ctx, svc, imgCache, newDriver, probe, "orca", name, opts)
	if err != nil {
		return fmt.Errorf("orca create: boot sandbox: %w", err)
	}

	projectRoot := orcaProjectRoot(env.RepoPath, env.WorkspaceName, env.InstanceID)

	// Post-boot verification: confirm git is present in the image.
	// Non-fatal: warn and skip clone if absent so the caller still receives
	// valid JSON and can open the sandbox (empty workspace).
	gitAvailable := true
	{
		checkCtx, checkCancel := context.WithTimeout(ctx, 10*time.Second)
		defer checkCancel()
		var checkBuf bytes.Buffer
		code, checkErr := svc.Exec(checkCtx, sb.ID.String(), agent.ExecOptions{
			Argv:   []string{"/bin/sh", "-c", "command -v git"},
			Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			Stdout: &checkBuf,
			Stderr: &checkBuf,
		})
		if checkErr != nil || code != 0 {
			slog.Warn("orca create: git not found in sandbox image; repo clone skipped",
				"image", imageRef,
				"check_err", checkErr,
				"exit_code", code,
				"hint", "set NEXUS3_IMAGE to an image with git, sshd, and claude-code installed")
			gitAvailable = false
		}
	}

	// Repo checkout: clone ORCA_REPO_URL at ORCA_REPO_REF (or ORCA_REPO_BRANCH)
	// into the guest at projectRoot. Non-fatal on clone failure — return valid
	// JSON so Orca can still open the sandbox (operator can clone manually).
	if env.RepoURL != "" && gitAvailable {
		cloneCtx, cloneCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cloneCancel()

		// Ensure the workspace parent directory exists.
		if _, mkErr := svc.Exec(cloneCtx, sb.ID.String(), agent.ExecOptions{
			Argv: []string{"/bin/sh", "-c", "mkdir -p /root/workspace"},
			Env:  map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		}); mkErr != nil {
			slog.Warn("orca create: mkdir /root/workspace failed", "err", mkErr)
		}

		cloneArgv := buildGitCloneArgv(env.RepoURL, env.RepoRef, env.RepoBranch, projectRoot)
		var cloneBuf bytes.Buffer
		code, cloneErr := svc.Exec(cloneCtx, sb.ID.String(), agent.ExecOptions{
			Argv: cloneArgv,
			Env: map[string]string{
				"PATH":                "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				"HOME":                "/root",
				"GIT_TERMINAL_PROMPT": "0", // prevent git from blocking on auth prompts
			},
			Stdout: &cloneBuf,
			Stderr: &cloneBuf,
		})
		if cloneErr != nil || code != 0 {
			slog.Warn("orca create: git clone failed; workspace will be empty",
				"url", env.RepoURL,
				"dest", projectRoot,
				"exit_code", code,
				"err", cloneErr,
				"output", cloneBuf.String())
		} else {
			slog.Info("orca create: git clone succeeded", "url", env.RepoURL, "dest", projectRoot)
		}
	} else if env.RepoURL == "" {
		slog.Info("orca create: ORCA_REPO_URL not set; skipping repo clone",
			"projectRoot", projectRoot)
	}

	result := buildOrcaConnectionJSON(env.InstanceID, sb.ID.String(), env.WorkspaceName, env.RepoPath, privKeyPath)
	return json.NewEncoder(w).Encode(result)
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
	if err := svc.Remove(ctx, sb.ID.String()); err != nil {
		return fmt.Errorf("orca destroy: remove %s: %w", sb.ID, err)
	}
	return nil
}
