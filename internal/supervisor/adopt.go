package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/govern"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor/handoff"
)

// adoptHandoffAcceptTimeout bounds how long RunAdopt waits, after listening
// on handoffSockPath, for the outgoing supervisor to dial in and offer a
// payload. Generous relative to the outgoing side's own handoffDialTimeout
// (5s), which is the deadline racing this one from the other end.
const adoptHandoffAcceptTimeout = 20 * time.Second

// adoptWaitOldExitTimeout bounds how long RunAdopt waits for the outgoing
// supervisor's pid to disappear before binding the canonical IPC socket
// path. The outgoing side's own shutdown (srv.Shutdown + removeOwnSocket)
// runs immediately after it reads a positive Ack, so this is generous
// headroom rather than the expected wait.
const adoptWaitOldExitTimeout = 15 * time.Second

// RunAdopt runs a detached supervisor in adopt mode: unlike RunDetached, it
// never calls driver.Start and never boots a VM. It listens on
// handoffSockPath (a Unix STREAM socket the CLI told the outgoing supervisor
// to dial via POST /supervisor/handoff), accepts exactly one handoff offer,
// and — only for a payload it can adopt — installs the netns runtime
// described by the persisted sandbox record into a freshly constructed
// driver and confirms.
//
// # Safety (D-HSH-08)
//
// Every failure path here returns a non-nil error WITHOUT ever calling
// [handoff.Confirm]. The outgoing side (performHandoff, on the other end of
// the same conn) only detaches after it reads a positive Ack; every error
// return here therefore leaves the VM and perimeter under the outgoing
// side's ownership, unchanged.
//
// The one moment ownership stops being unambiguous is a successful return
// from [handoff.Confirm]. If Confirm itself fails (a write error — the
// outgoing side may or may not have received it), RunAdopt still returns an
// error and does NOT proceed to bind the IPC socket, write a pidfile, or do
// anything else externally visible: the *NetnsRuntime and *CHDriver built in
// this process are process-local Go values, and the perimeter fd this
// process holds is its own SCM_RIGHTS-duplicated copy of the same underlying
// socket the outgoing side independently still holds a reference to. Letting
// this process exit without calling rt.Stop() (which would SIGKILL the VM's
// process group) is therefore always safe: either the outgoing side saw the
// Ack and would be wrong to still think it owns the VM (in which case NOT
// serving from this side is the bug to fix in a follow-up slice, not a
// safety violation), or it did not see the Ack and correctly resumes
// ownership — in neither case does this process's exit mutate anything.
func RunAdopt(cfg Config, handoffSockPath string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return fmt.Errorf("supervisor: adopt: mkdir state dir %s: %w", cfg.StateDir, err)
	}

	st, err := store.NewFileStore(cfg.StoreRoot)
	if err != nil {
		return fmt.Errorf("supervisor: adopt: open store at %s: %w", cfg.StoreRoot, err)
	}
	sb, err := st.ResolveByPrefix(ctx, cfg.SandboxRef)
	if err != nil {
		return fmt.Errorf("supervisor: adopt: resolve sandbox %s: %w", cfg.SandboxRef, err)
	}

	// Fail-closed identity guard. The CLI verb that spawned this process
	// already checked this against the same store record; RunAdopt re-checks
	// independently rather than trusting its caller, per this motive's rail
	// that a check must never be satisfied by "the caller already verified
	// it" — a zero/absent/unreadable value here REFUSES.
	if sb.NetnsChildPID <= 0 || sb.NetnsChildPGID <= 0 || sb.NetnsChildStartTime == 0 ||
		sb.GuestTapName == "" || sb.CHAPISocket == "" {
		return fmt.Errorf("supervisor: adopt: sandbox %s has an incomplete netns identity; refusing to adopt", sb.ID)
	}

	extraDisks := make([]cloudhypervisor.ExtraDisk, 0, len(cfg.ExtraDisks))
	for _, p := range cfg.ExtraDisks {
		extraDisks = append(extraDisks, cloudhypervisor.ExtraDisk{Path: p})
	}
	var memMaxMiB uint32
	if cfg.GovBounds.MemMaxBytes > 0 {
		memMaxMiB = uint32(cfg.GovBounds.MemMaxBytes / (1024 * 1024)) //nolint:gosec // bytes→MiB; fits uint32 for any sane ceiling
	}
	vcpuMax := uint32(cfg.GovBounds.VCPUMax) //nolint:gosec // int32→uint32; VCPUMax is always non-negative by construction
	drv, err := cloudhypervisor.New(buildSupervisorDriverConfig(cfg, memMaxMiB, vcpuMax, extraDisks))
	if err != nil {
		return fmt.Errorf("supervisor: adopt: init driver: %w", err)
	}

	svc := service.New(st, drv, lifecycle.New())
	broker := cred.NewBroker()
	svc = svc.WithBroker(broker)

	var refreshers []*cred.Refresher
	if cfg.CredsFile != "" {
		for _, host := range service.AgentEgressHosts(cred.ClaudeCodeProfile) {
			r, rErr := cred.NewRefresher(cfg.CredsFile, host, broker)
			if errors.Is(rErr, cred.ErrStoreAbsent) {
				break // same file for all hosts; no point trying others
			}
			if rErr != nil {
				slog.Warn("supervisor.adopt.refresher_init_failed", "host", host, "err", rErr)
				continue
			}
			refreshers = append(refreshers, r)
		}
	}

	// ── Listen for the handoff offer ──────────────────────────────────────
	_ = os.Remove(handoffSockPath)
	rawLn, err := net.Listen("unix", handoffSockPath)
	if err != nil {
		return fmt.Errorf("supervisor: adopt: listen handoff socket %s: %w", handoffSockPath, err)
	}
	hln, ok := rawLn.(*net.UnixListener)
	if !ok {
		rawLn.Close()
		return fmt.Errorf("supervisor: adopt: unexpected handoff listener type %T", rawLn)
	}
	defer func() {
		hln.Close()
		_ = os.Remove(handoffSockPath)
	}()

	if err := hln.SetDeadline(time.Now().Add(adoptHandoffAcceptTimeout)); err != nil {
		return fmt.Errorf("supervisor: adopt: set accept deadline: %w", err)
	}
	rawConn, err := hln.Accept()
	if err != nil {
		return fmt.Errorf("supervisor: adopt: accept handoff connection: %w", err)
	}
	conn, isUnix := rawConn.(*net.UnixConn)
	if !isUnix {
		rawConn.Close()
		return fmt.Errorf("supervisor: adopt: unexpected handoff conn type %T", rawConn)
	}
	defer conn.Close()

	payload, fdFile, err := handoff.Accept(conn)
	if err != nil {
		return fmt.Errorf("supervisor: adopt: accept payload: %w", err)
	}
	if payload.Version != handoff.CurrentVersion {
		reason := fmt.Sprintf("unsupported handoff version %d (this binary understands %d)", payload.Version, handoff.CurrentVersion)
		if fdFile != nil {
			fdFile.Close()
		}
		_ = handoff.Refuse(conn, reason)
		return fmt.Errorf("supervisor: adopt: %s", reason)
	}
	if !payload.Perimeter.Present || fdFile == nil {
		if fdFile != nil {
			fdFile.Close()
		}
		const reason = "payload carries no perimeter fd"
		_ = handoff.Refuse(conn, reason)
		return fmt.Errorf("supervisor: adopt: %s", reason)
	}

	rt, err := cloudhypervisor.AdoptNetnsRuntime(
		sb.NetnsChildPID, sb.NetnsChildPGID, sb.NetnsChildStartTime,
		sb.GuestTapName, sb.CHAPISocket, fdFile,
	)
	if err != nil {
		_ = handoff.Refuse(conn, err.Error())
		return fmt.Errorf("supervisor: adopt: adopt netns runtime: %w", err)
	}
	if err := drv.AdoptRuntime(sb.ID, rt); err != nil {
		// Do NOT call rt.Stop() on this path: that would SIGKILL the process
		// group of the only live copy of the VM. Dropping rt un-stopped and
		// refusing is what leaves the outgoing side as sole owner.
		_ = handoff.Refuse(conn, err.Error())
		return fmt.Errorf("supervisor: adopt: install runtime: %w", err)
	}

	if err := handoff.Confirm(conn); err != nil {
		return fmt.Errorf("supervisor: adopt: confirm: %w", err)
	}
	slog.Info("supervisor.adopted", "sandboxRef", cfg.SandboxRef, "sandbox", sb.ID)

	// ── Wait for the outgoing supervisor to actually exit before binding the
	// canonical IPC socket path — it still owns that inode until its own
	// shutdown path unlinks it (removeOwnSocket). ────────────────────────
	sockPath := SockPath(cfg.StateDir)
	if sb.SupervisorPID > 0 {
		deadline := time.Now().Add(adoptWaitOldExitTimeout)
		for PidAlive(sb.SupervisorPID) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	_ = os.Remove(sockPath) // best-effort: only relevant if the old process left a stale file

	// Seed the new perimeter's MITM proxy with the SAME CA the outgoing
	// supervisor's payload carried, rather than letting StartPerimeterOnly
	// mint a fresh one: the guest already imported and pinned the outgoing
	// CA this boot, and a fresh CA would invalidate that trust without a
	// guest reboot to re-seed it.
	var seedCA *service.CASeed
	if len(payload.CA.CertPEM) > 0 && len(payload.CA.KeyPEM) > 0 {
		seedCA = &service.CASeed{CertPEM: payload.CA.CertPEM, KeyPEM: payload.CA.KeyPEM}
	}
	if err := svc.StartPerimeterOnly(ctx, sb, seedCA); err != nil {
		return fmt.Errorf("supervisor: adopt: start perimeter: %w", err)
	}

	var perimSupPtr atomic.Pointer[perimeter.PerimeterSupervisor]
	if sup := svc.GetPerimeterSupervisor(sb.ID); sup != nil {
		perimSupPtr.Store(sup)
	}

	binaryHash, hashErr := computeBinaryHash()
	if hashErr != nil {
		slog.Warn("supervisor.adopt.binary_hash_failed", "err", hashErr)
	}

	allowEgressFn := allowEgressFunc(func(host string) error {
		sup := perimSupPtr.Load()
		if sup == nil {
			return fmt.Errorf("perimeter not yet ready")
		}
		return sup.AllowEgress(host)
	})
	handoffFn := handoffFunc(func(hctx context.Context, peerSock string) (bool, string, error) {
		sup := perimSupPtr.Load()
		if sup == nil {
			return false, "perimeter not yet ready", nil
		}
		bootVCPUs := cfg.BootVCPUs
		if bootVCPUs == 0 {
			bootVCPUs = 1
		}
		build := payloadBuilder(func() (handoff.Payload, *os.File, error) {
			return buildHandoffPayload(sup, cfg.SandboxRef, bootVCPUs, cfg.MemoryMiB)
		})
		return performHandoff(hctx, peerSock, build)
	})

	ipcH, err := serveIPC(ctx, sockPath, svc, cfg.SandboxRef, allowEgressFn, handoffFn, binaryHash)
	if err != nil {
		return fmt.Errorf("supervisor: adopt: bind IPC socket %s: %w", sockPath, err)
	}
	stopCh := ipcH.StopCh
	detachCh := ipcH.DetachCh
	defer removeOwnSocket(sockPath, ipcH.BindStat)

	bootVCPUs := int32(cfg.BootVCPUs) //nolint:gosec // uint32→int32; vCPU counts always fit int32
	if bootVCPUs == 0 {
		bootVCPUs = 1
	}
	resizer := cloudhypervisor.NewSandboxResizer(drv, sb.ID, cfg.GovBounds, int64(cfg.MemoryMiB)*1024*1024, bootVCPUs)
	gov := govern.New(govern.Config{
		Resizer:   resizer,
		Telemetry: govern.NewVsockTelemetry(drv, sb.ID),
		Bounds:    cfg.GovBounds,
	})
	diskIndices := cfg.ResizableDiskIndices
	if len(diskIndices) == 0 && cfg.HasWorkspaceDisk {
		diskIndices = []int{cfg.WorkspaceDiskIndex}
	}
	wireGovernorAxes(gov, resizer, resizer, cfg.GovBounds, diskIndices)
	go gov.Run(ctx)

	for _, r := range refreshers {
		r.Register(sb.ID)
	}
	for _, r := range refreshers {
		go func(r *cred.Refresher) {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, _, tokErr := r.Token(ctx); tokErr != nil {
						slog.Warn("supervisor.adopt.token_refresh_failed", "host", r.Host(), "err", tokErr)
					}
				}
			}
		}(r)
	}

	// NOTE: unlike RunDetached, RunAdopt does not re-seed the guest agent
	// placeholder credentials — the guest was never rebooted, so it already
	// holds its placeholder. The MITM CA itself IS transferred: seedCA above
	// carries the outgoing supervisor's CA key material into the new
	// mitm.Proxy StartPerimeterOnly constructs, so the guest's already-
	// pinned CA continues to sign leaf certificates correctly (found and
	// fixed live during ticket 08's proof — see handoff.Payload.Validate,
	// which refuses any payload with an empty CA and was previously
	// unreachable because no payloadBuilder populated it).

	pid := os.Getpid()
	pidfile := PidfilePath(cfg.StateDir)
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return fmt.Errorf("supervisor: adopt: write pidfile %s: %w", pidfile, err)
	}
	defer func() {
		data, readErr := os.ReadFile(pidfile)
		if readErr != nil {
			return
		}
		if !bytes.Equal(bytes.TrimRight(data, "\n"), []byte(strconv.Itoa(pid))) {
			return
		}
		_ = os.Remove(pidfile)
	}()

	slog.Info("supervisor.adopt.ready", "sandboxRef", cfg.SandboxRef, "pid", pid, "sock", sockPath)

	cause := awaitShutdown(ctx, stopCh, detachCh)
	if cause == shutdownByDetach {
		slog.Info("supervisor.adopt.detached", "sandboxRef", cfg.SandboxRef)
		return nil
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if _, stopErr := svc.Stop(stopCtx, cfg.SandboxRef); stopErr != nil {
		slog.Warn("supervisor.adopt.stop_failed", "sandboxRef", cfg.SandboxRef, "err", stopErr)
	}
	slog.Info("supervisor.adopt.exited", "sandboxRef", cfg.SandboxRef)
	return nil
}
