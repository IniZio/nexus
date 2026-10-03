package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/IniZio/nexus/internal/core/builder"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/volumestore"
	"github.com/IniZio/nexus/internal/hubclient"
)

// createRun carries the state shared by the CreateAndBoot phases.
type createRun struct {
	svc     *Service
	project string
	name    string
	opts    CreateAndBootOptions

	id             domain.SandboxID
	ext4Path       string
	resolvedDigest string
	diskDir        string

	needsDisk         bool
	needsWorkspace    bool
	needsNamedVols    bool
	diskCopyPath      string
	workspaceDiskPath string
	scratchDiskPath   string

	intentLease       *createIntentLease
	volumeLeases      []*store.Lock
	namedDiskAttached []string
	success           bool

	bootDrv      driver.Driver
	agentProfile cred.AgentProfile
	sb           domain.Sandbox
	booted       domain.Sandbox
}

func (r *createRun) errf(format string, args ...any) error {
	return fmt.Errorf("service: create-and-boot %s/%s: "+format, append([]any{r.project, r.name}, args...)...)
}

// resolveImage resolves the root ext4, rejects duplicate handles and mints the ID.
func (r *createRun) resolveImage(ctx context.Context, cache *image.Cache) error {
	ext4Path, resolvedDigest, err := resolveExt4WithTools(ctx, r.opts.Image, cache, r.opts.CacheRoot, r.opts.AgentBytes, r.opts.SandboxTools)
	if err != nil {
		return r.errf("%w", err)
	}
	r.ext4Path, r.resolvedDigest = ext4Path, resolvedDigest

	handle := r.project + "/" + r.name
	if _, err := r.svc.store.ResolveByHandle(ctx, handle); err == nil {
		return fmt.Errorf("sandbox %q already exists: %w", handle, store.ErrAlreadyExists)
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("service: create-and-boot: check handle %q: %w", handle, err)
	}

	// Minted before driver construction so the per-sandbox disk copy is named by
	// ID. A caller may pre-mint (A-MOUNT) to stage ID-keyed resources first.
	r.id = r.opts.PreMintedID
	if r.id == (domain.SandboxID{}) {
		r.id = domain.NewSandboxID()
	}
	return nil
}

// planDisks resolves diskDir once, before any materialisation, so the create
// intent can record planned paths and cowExt4 and workspace capture share it.
func (r *createRun) planDisks() error {
	opts := r.opts
	r.needsDisk = opts.Image.RootfsPath == ""
	r.needsWorkspace = opts.Workspace != nil
	r.needsNamedVols = opts.Volumes != nil && len(opts.NamedVolumeMounts) > 0
	_, needsScratch := hostWorkspacePath(opts)
	needsScratch = needsScratch && !opts.NoScratchDisk
	if r.needsDisk || r.needsWorkspace || r.needsNamedVols || needsScratch {
		r.diskDir = opts.DiskDir
		if r.diskDir == "" {
			var err error
			r.diskDir, err = defaultDiskDir()
			if err != nil {
				return r.errf("%w", err)
			}
		}
	}
	if r.needsDisk {
		r.diskCopyPath = filepath.Join(r.diskDir, r.id.String()+".raw")
	}
	if r.needsWorkspace {
		r.workspaceDiskPath = filepath.Join(r.diskDir, r.id.String()+"-workspace.ext4")
	}
	return nil
}

// preflightDiskSpace refuses before any byte is written when the host lacks
// room (TBD-PD-26, M3-AC2). The projection is an upper bound; zero skips it.
func (r *createRun) preflightDiskSpace() error {
	if r.opts.ForceDiskSpace {
		return nil
	}
	src := ""
	if r.needsDisk {
		src = r.ext4Path
	}
	projected, detail := ProjectCreateBytes(r.diskDir, src, r.needsWorkspace)
	if projected <= 0 {
		return nil
	}
	check := r.opts.DiskPreflight
	if check == nil {
		check = CheckDiskSpaceBytes
	}
	if _, dErr := check(r.diskDir, projected, detail); dErr != nil {
		return r.errf("%w", dErr)
	}
	return nil
}

// writeIntent records the planned disks so the reaper can reclaim them if the
// process dies before store.Create. The flock lease held for the create window
// tells a concurrent reaper this create is still in flight.
func (r *createRun) writeIntent() error {
	// Named rw volumes need the lease even with no disk paths, or two concurrent
	// --rootfs creates cannot see each other in flight (M-a, D-PD-93).
	if r.diskCopyPath == "" && r.workspaceDiskPath == "" && !r.needsNamedVols {
		return nil
	}
	lease, err := writeCreateIntent(r.diskDir, r.id, r.diskCopyPath, r.workspaceDiskPath)
	if err != nil {
		return r.errf("write create intent: %w", err)
	}
	r.intentLease = lease
	return nil
}

// releaseDisks drops the intent lease and, on failure, removes materialised disks.
func (r *createRun) releaseDisks() {
	r.intentLease.release()
	if r.success {
		return
	}
	for _, p := range []string{r.diskCopyPath, r.workspaceDiskPath, r.scratchDiskPath} {
		if p != "" {
			_ = os.Remove(p)
		}
	}
}

func (r *createRun) unlockVolumeLeases() {
	for _, lk := range r.volumeLeases {
		_ = lk.Unlock()
		_ = lk.Close()
	}
	r.volumeLeases = nil
}

// releaseVolumes drops the D2 leases, then detaches attached volumes on failure.
func (r *createRun) releaseVolumes(ctx context.Context) {
	r.unlockVolumeLeases()
	if !r.success && r.opts.Volumes != nil {
		// ctx may already be cancelled on the error path (RISK-SD2-1).
		rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer rollbackCancel()
		for _, vname := range r.namedDiskAttached {
			_ = detachVolumeLocked(rollbackCtx, r.opts.Volumes, vname, r.id.String())
		}
	}
}

// precheckVolumes is fail-fast UX only: an unlocked read that surfaces
// "volume already in use" before the slow workspace capture. The locked check
// in attachVolumes is authoritative.
func (r *createRun) precheckVolumes(ctx context.Context) error {
	vs := r.opts.Volumes
	if vs == nil {
		return nil
	}
	for _, mount := range r.opts.NamedVolumeMounts {
		if mount.Kind == volumestore.KindDisk && !mount.ReadOnly {
			if preErr := preCheckRWAttachUnlocked(ctx, vs, r.svc.store, r.diskDir, mount.Name, r.id.String()); preErr != nil {
				return r.errf("%w", preErr)
			}
		}
	}
	return nil
}

// copyRootDisk gives each sandbox its own writable copy so the shared,
// digest-addressed cache artifact is never mutated by the guest.
func (r *createRun) copyRootDisk() error {
	if !r.needsDisk {
		return nil
	}
	cp, err := cowExt4(r.ext4Path, r.diskDir, r.id)
	if err != nil {
		return r.errf("%w", err)
	}
	r.ext4Path = cp
	return nil
}

// captureWorkspace snapshots the host source tree into an ext4 image appended
// to ExtraDisks after any caller-supplied disks.
func (r *createRun) captureWorkspace(ctx context.Context) error {
	ws := r.opts.Workspace
	if ws == nil {
		return nil
	}
	captureFn := r.opts.WorkspaceCapturer
	if captureFn == nil {
		captureFn = builder.WorktreeToDisk
	}
	if err := os.MkdirAll(r.diskDir, 0o700); err != nil {
		return r.errf("workspace disk dir mkdir: %w", err)
	}
	if err := captureFn(ctx, ws.SourcePath, r.workspaceDiskPath, ws.CaptureMaxBytes); err != nil {
		return r.errf("capture workspace: %w", err)
	}
	r.opts.ExtraDisks = append(r.opts.ExtraDisks, ExtraDisk{Path: r.workspaceDiskPath})
	return nil
}

// attachVolumes creates and attaches named volumes, holding each per-volume
// flock until store.Create commits (D2). It runs after workspace capture so the
// locks do not span the capture I/O.
func (r *createRun) attachVolumes(ctx context.Context) error {
	vs := r.opts.Volumes
	if vs == nil || len(r.opts.NamedVolumeMounts) == 0 {
		return nil
	}
	for _, mount := range r.opts.NamedVolumeMounts {
		if _, err := vs.Create(ctx, mount.Name, mount.Kind, mount.SizeBytes, ""); err != nil {
			return r.errf("create volume %s: %w", mount.Name, err)
		}
	}

	// Lock in sorted-name order so opposite declaration orders cannot ABBA-deadlock.
	sortedMounts := make([]NamedVolumeMount, len(r.opts.NamedVolumeMounts))
	copy(sortedMounts, r.opts.NamedVolumeMounts)
	sort.Slice(sortedMounts, func(i, j int) bool {
		return sortedMounts[i].Name < sortedMounts[j].Name
	})
	for _, mount := range sortedMounts {
		// A deadline turns a wedged lock-holder into an error, not a hung CLI (RISK-SD2-1).
		guardCtx, guardCancel := context.WithTimeout(ctx, 10*time.Second)
		var lk *store.Lock
		var err error
		if mount.Kind == volumestore.KindDisk && !mount.ReadOnly {
			lk, err = checkRWAttach(guardCtx, vs, r.svc.store, r.diskDir, mount.Name, r.id.String())
			guardCancel()
			if err != nil {
				return r.errf("volume %s: %w", mount.Name, err)
			}
		} else {
			lk, err = vs.AttachLocked(guardCtx, mount.Name, r.id.String())
			guardCancel()
			if err != nil {
				return r.errf("attach volume %s: %w", mount.Name, err)
			}
		}
		r.volumeLeases = append(r.volumeLeases, lk)
		r.namedDiskAttached = append(r.namedDiskAttached, mount.Name)
	}

	// Device letters follow DECLARATION order regardless of lock order (§1.5).
	var namedDiskExtras []ExtraDisk
	for _, mount := range r.opts.NamedVolumeMounts {
		if mount.Kind == volumestore.KindDisk {
			namedDiskExtras = append(namedDiskExtras, ExtraDisk{Path: vs.DiskPath(mount.Name)})
		}
	}
	if len(namedDiskExtras) > 0 {
		r.opts.ExtraDisks = append(namedDiskExtras, r.opts.ExtraDisks...)
	}
	return nil
}

// addScratchDisk appends the unformatted scratch disk (D-SD-01, D-SD-02). It
// MUST run after attachVolumes: scratch is always the last ExtraDisks entry
// (TestScratchDisk_IsLast_SD_AC1).
func (r *createRun) addScratchDisk() error {
	if _, hasWorkspace := hostWorkspacePath(r.opts); !hasWorkspace || r.opts.NoScratchDisk {
		return nil
	}
	if err := os.MkdirAll(r.diskDir, 0o700); err != nil {
		return r.errf("scratch disk dir mkdir: %w", err)
	}
	r.scratchDiskPath = ScratchDiskHostPath(r.diskDir, r.id.String())
	if err := createSparseDisk(r.scratchDiskPath, ScratchDiskDefaultBytes); err != nil {
		return r.errf("create scratch disk: %w", err)
	}
	r.opts.ExtraDisks = append(r.opts.ExtraDisks, ExtraDisk{Path: r.scratchDiskPath})
	return nil
}

// buildRecord constructs the per-sandbox driver and the Created-state record.
func (r *createRun) buildRecord(newDriver DriverFactory) error {
	opts := r.opts
	bootDrv, err := newDriver(r.ext4Path, opts.ExtraDisks)
	if err != nil {
		return r.errf("init driver: %w", err)
	}
	r.bootDrv = bootDrv

	// Resolved once so the persisted AgentName and the seeded credentials agree.
	r.agentProfile = opts.AgentProfile
	// Name, not PlaceholderEnvVar, is the zero-value sentinel (see
	// cred.AgentProfile.Name): an API-key-only agent has no OAuth placeholder.
	if opts.UseAgentSeed && r.agentProfile.Name == "" {
		r.agentProfile = cred.MustProfileByName(cred.ClaudeCodeProfileName)
	}

	principal := os.Getenv(vault.PrincipalEnv)
	if principal == "" {
		if p, lpErr := vault.LocalPrincipal(); lpErr == nil {
			principal = p
		}
	}

	r.sb = domain.Sandbox{
		ID:        r.id,
		Name:      r.name,
		Project:   r.project,
		Labels:    opts.Labels,
		State:     domain.Created,
		Principal: principal,
		Backend:   r.svc.backendFor(""),
		Envelope: domain.Envelope{
			ImageDigest:        r.resolvedDigest,
			AllowedHosts:       opts.AllowedHosts, // frozen at creation (P1-S6)
			SSHPublicKey:       opts.SSHPublicKey, // frozen at creation (ORCA-S1)
			SecretHosts:        append(secretHostsFromBinds(opts.Secrets), opts.ExtraSecretHosts...),
			SecretHostSuffixes: opts.ExtraSecretHostSuffixes,
			SecretSpecs:        secretSpecsFromBinds(opts.Secrets),
			OpenEgress:         opts.OpenEgress,              // D-PD-33: explicit opt-in; never inferred from empty AllowedHosts
			AllowedRepo:        opts.AllowedRepo,             // D-PD-36: per-repo path allowlist; enforced below
			AllowedBranches:    resolveAllowedBranches(opts), // TBD-1: derived from the bound worktree's branch; see resolveAllowedBranches
			PathPolicies:       opts.PathPolicies,            // T4: per-secret path policies; converted to mitm.PathPolicies at start
			MCPPolicies:        opts.MCPPolicies,
		},
		RemoveOnExit:    opts.RemoveOnExit,
		BaseRef:         opts.BaseRef, // G1: shallow-clone boundary SHA (D-PD-19); empty if no git workspace
		MountedVolumes:  namedVolumeAttachments(opts.NamedVolumeMounts),
		LiveMounts:      opts.LiveMounts,
		AgentName:       r.agentProfile.Name,                                  // TBD-PD-32: empty when no agent is attached
		ExtraAgentNames: extraAgentNamesFromProfiles(opts.ExtraAgentProfiles), // D-TP-09: persisted so supervisor can re-seed on restart
	}
	return nil
}

// guardSecrets enforces the pre-persist secret invariants for ALL callers.
//
// Every bind touching a GitHub host MUST carry a path policy (AllowedRepo shim
// or a PathPolicies entry): the operator credential is an unrotated full-scope
// PAT, and only the positive path policy bounds it across repos (D-PD-36,
// D-PDE-16). Do NOT add an opt-in flag. Non-GitHub hosts may be host-bounded.
// Mixed GitHub/non-GitHub binds are refused because non-GitHub hosts carry no
// path filter. Agent sandboxes may bind GitHub when a policy is set (D-SHL-05).
// Start and startSupervisor repeat the guard for records already on disk.
func (r *createRun) guardSecrets() error {
	for _, b := range r.opts.Secrets {
		if SecretMixesGitHubHosts(b) {
			return r.errf("%w", ErrMixedGitHubSecret)
		}
	}
	for _, b := range r.opts.Secrets {
		for _, h := range b.Hosts {
			if isGitHubHost(h) && !githubHostBoundByPolicy(h, r.opts.AllowedRepo, r.opts.PathPolicies) {
				return r.errf("%w", ErrUnboundGitHubSecret)
			}
		}
	}
	return nil
}

// persist commits the record. The D2 test seam fires while volume leases are
// still held; once the record is durable the leases are released so Prune sees
// the volume as live.
func (r *createRun) persist(ctx context.Context) error {
	if hookPtr := r.svc.testHookBeforeStoreCreate.Load(); hookPtr != nil {
		if hookErr := (*hookPtr)(); hookErr != nil {
			return r.errf("testHookBeforeStoreCreate: %w", hookErr)
		}
	}
	if err := r.svc.store.Create(ctx, r.sb); err != nil {
		return r.errf("create record: %w", err)
	}
	r.unlockVolumeLeases()
	r.svc.emitSandboxEvent(ctx, hubclient.TypeSandboxCreated, r.sb.ID.String(), r.sb.Handle())
	return nil
}

// boot starts the VM inside store.Update, the same locking pattern as
// service.Start, so the substrate call and record write share one flock and
// cannot double-boot.
func (r *createRun) boot(ctx context.Context) error {
	bootErr := r.svc.store.Update(ctx, r.id, func(rec *domain.Sandbox) error {
		tr, err := r.svc.machine.Next(rec.State, lifecycle.TriggerStart)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		instanceID, err := r.bootDrv.Start(ctx, driver.StartRequest{
			SandboxID:   rec.ID,
			ImageDigest: rec.Envelope.ImageDigest,
		})
		if err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		rec.State = tr.NextState
		rec.InstanceID = instanceID
		rec.StopReason = "" // cleared: sandbox is running
		if nsp, ok := r.bootDrv.(driver.NetnsStateProvider); ok {
			ns, hasNetns := nsp.NetnsState(rec.ID)
			if hasNetns {
				rec.NetnsChildPID = ns.ChildPID
				rec.NetnsChildPGID = ns.ChildPGID
				rec.NetnsChildStartTime = ns.ChildStartTime
				rec.VhostSocket = ns.VhostSocket
				rec.CHAPISocket = ns.APISocket
				rec.NetnsControlSocket = ns.ControlSocket
				rec.NetnsControlToken = ns.ControlToken
			}
		}
		r.booted = *rec
		return nil
	})
	if bootErr != nil {
		_ = r.svc.store.Delete(ctx, r.id)
		return r.errf("boot: %w", bootErr)
	}
	return nil
}

// abortBoot stops the VM and deletes the record after a post-boot failure.
func (r *createRun) abortBoot(ctx context.Context) {
	_ = r.bootDrv.Stop(ctx, r.booted.ID)
	_ = r.svc.store.Delete(ctx, r.booted.ID)
}

func (r *createRun) probeReachable(ctx context.Context, probe ProbeFunc) error {
	if probe == nil {
		return nil
	}
	timeout := r.opts.ReachabilityTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	rCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := probe(rCtx, r.bootDrv, r.booted.ID); err != nil {
		r.abortBoot(ctx)
		return r.errf("%w: %v", ErrAgentUnreachable, err)
	}
	return nil
}

// seedCredentials seeds placeholder credentials into the guest (P1-S6). Both
// paths are no-ops when Broker or Seeder is nil.
func (r *createRun) seedCredentials(ctx context.Context) error {
	if r.opts.UseAgentSeed {
		return r.seedAgentCredentials(ctx)
	}
	opts := r.opts
	booted := r.booted
	var combined []byte
	capture := func(_ context.Context, _ domain.SandboxID, payload []byte) error {
		combined = append(combined, payload...)
		return nil
	}
	seedFn := opts.Seeder
	if seedFn != nil {
		seedFn = capture
	}
	if _, err := SeedGuest(ctx, opts.Broker, booted.ID, booted.Envelope.AllowedHosts, seedFn); err != nil {
		r.abortBoot(ctx)
		return r.errf("seed: %w", err)
	}
	extra, _, err := applySecrets(opts.Broker, booted.ID, opts.Secrets)
	if err != nil {
		r.abortBoot(ctx)
		return r.errf("secrets: %w", err)
	}
	combined = append(combined, extra...)
	if opts.Seeder != nil && len(combined) > 0 {
		if err := opts.Seeder(ctx, booted.ID, combined); err != nil {
			r.abortBoot(ctx)
			return r.errf("seed: %w", err)
		}
	}
	return nil
}

// seedAgentCredentials delivers the agent profile's placeholder payload, wires
// the real token host-side so the MITM proxy can swap it, and enrols the
// sandbox for token rotation when the source supports it. Extra agents are
// seeded in the same write (D-TP-09). A missing token only warns: egress then
// forwards the placeholder.
func (r *createRun) seedAgentCredentials(ctx context.Context) error {
	opts := r.opts
	booted := r.booted
	agentProfile := r.agentProfile
	recs, err := seedGuestAgentForProfiles(ctx, opts.Broker, booted.ID, opts.Seeder, agentProfile, opts.AgentCredKind, opts.ExtraAgentProfiles)
	if err != nil {
		r.abortBoot(ctx)
		return r.errf("seed agent: %w", err)
	}

	// AgentCredSource (OAuth/Refresher) takes priority over the direct API-key token.
	var realToken string
	if opts.AgentCredSource != nil {
		t, _, credErr := opts.AgentCredSource.Token(ctx)
		if credErr != nil {
			slog.Warn("create-and-boot: get real token from credential source",
				"sandbox", booted.ID, "host", agentProfile.CredentialedHost, "err", credErr)
		} else {
			realToken = t
		}
	} else if opts.AgentEgressToken != "" {
		realToken = opts.AgentEgressToken
	}

	if realToken != "" && opts.Broker != nil && len(recs) > 0 {
		if err := opts.Broker.SetRealToken(booted.ID, agentProfile.CredentialedHost, realToken); err != nil {
			// Non-fatal: the proxy forwards the useless placeholder.
			slog.Warn("create-and-boot: set real token for agent egress",
				"sandbox", booted.ID, "host", agentProfile.CredentialedHost, "err", err)
		}
		// Type-assert to avoid importing cred.Refresher directly.
		type sandboxRegistrar interface{ Register(domain.SandboxID) }
		if reg, ok := opts.AgentCredSource.(sandboxRegistrar); ok {
			reg.Register(booted.ID)
		}
		if dr, ok := opts.AgentCredSource.(sandboxDeregistrar); ok {
			r.svc.storeDeregistrar(booted.ID, dr)
		}
	} else if realToken == "" {
		hint := "configure NEXUS_DEDICATED_CRED_STORE (OAuth path)"
		if agentProfile.APIKeyEnvVar != "" {
			hint = fmt.Sprintf("set %s (API-key path) or %s", agentProfile.APIKeyEnvVar, hint)
		}
		slog.Warn("create-and-boot: no real token for agent egress; egress will send placeholder",
			"sandbox", booted.ID, "host", agentProfile.CredentialedHost, "hint", hint)
	}
	return nil
}

// seedSSHKeys injects authorized_keys (ORCA-S1). Failure is fatal when a key was requested.
func (r *createRun) seedSSHKeys(ctx context.Context) error {
	if r.booted.Envelope.SSHPublicKey == "" {
		return nil
	}
	if err := SeedSSHAuthorizedKeys(ctx, r.booted.Envelope.SSHPublicKey, r.booted.ID, r.svc.sshSeeder); err != nil {
		r.abortBoot(ctx)
		return r.errf("ssh seed: %w", err)
	}
	return nil
}

// seedGit pushes the operator's git identity (G1, D-PD-03). A missing host
// identity is an actionable error; there is deliberately no bot-identity
// fallback (operator decision 2026-08-15, reversing D-PD-02).
func (r *createRun) seedGit(ctx context.Context) error {
	if r.opts.GitSeeder == nil {
		return nil
	}
	var workspacePath string
	if r.opts.Workspace != nil {
		workspacePath = r.opts.Workspace.GuestPath
	}
	if _, err := SeedGitIdentity(ctx, r.booted.ID, r.opts.Labels, SourceGuestPaths(workspacePath, r.opts.LiveMounts), r.opts.GitSeeder); err != nil {
		r.abortBoot(ctx)
		return r.errf("git identity: %w", err)
	}
	return nil
}
