package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/govern"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/statedir"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor/handoff"
)

// serveAdoptedInput carries what serveAdoptedSupervisor needs from whichever
// acquisition path produced a live, installed netns runtime.
type serveAdoptedInput struct {
	cfg Config
	st  store.Store
	svc *service.Service
	drv *cloudhypervisor.CHDriver
	sb  domain.Sandbox

	// seedCA is the MITM CA to hand StartPerimeterOnly. Non-nil on the
	// HANDOFF path, where the outgoing supervisor's payload carried the CA
	// that the guest already imported and pinned this boot. NIL on the CRASH
	// path, where no payload exists and StartPerimeterOnly must mint a fresh
	// CA — see RunReacquire, which logs the resulting TLS breakage rather
	// than letting a caller believe TLS survived.
	seedCA *service.CASeed

	// refreshers are the credential refreshers to register and keep warm.
	refreshers []*cred.Refresher

	// waitForPID, when > 0, is a previous supervisor whose exit must be
	// observed before this process binds the canonical IPC socket path: the
	// old process still owns that inode until its own shutdown unlinks it
	// (removeOwnSocket). Zero on the crash path — the previous supervisor is
	// already dead, which is why this path exists at all.
	waitForPID int

	// logPrefix distinguishes this path's structured log events
	// ("supervisor.adopt" vs "supervisor.reacquire") so an operator reading
	// journals can tell a planned upgrade from a crash recovery.
	logPrefix string
}

// serveAdoptedSupervisor runs the long-lived supervisor loop for a sandbox
// whose netns runtime this process has already acquired and installed in drv,
// WITHOUT booting a VM.
//
// It is the shared tail of both acquisition paths:
//
//   - [RunAdopt] — planned upgrade. The outgoing supervisor is alive and
//     passes the perimeter fd over SCM_RIGHTS; seedCA carries its CA.
//   - [RunReacquire] — crash recovery. The outgoing supervisor is dead; the
//     perimeter is rebuilt through the surviving netns child's control
//     socket and seedCA is nil.
//
// Both must then do the SAME thing for the VM's whole lifetime: start the
// perimeter, bind the IPC socket, run the governor, keep credentials warm,
// and serve until stop/detach/VM-death. Extracting it keeps the two paths
// from drifting — in particular the IPC-socket ordering hazard documented on
// waitForPID, which is easy to get subtly wrong when reimplemented.
//
// This function does NOT return until the supervisor is shutting down.
func serveAdoptedSupervisor(ctx context.Context, in serveAdoptedInput) error {
	cfg, st, svc, drv, sb := in.cfg, in.st, in.svc, in.drv, in.sb

	// ── Wait for any previous supervisor to actually exit before binding the
	// canonical IPC socket path — it still owns that inode until its own
	// shutdown path unlinks it (removeOwnSocket). ────────────────────────
	sockPath := SockPath(cfg.StateDir)
	if in.waitForPID > 0 {
		deadline := time.Now().Add(adoptWaitOldExitTimeout)
		for PidAlive(in.waitForPID) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	_ = os.Remove(sockPath) // best-effort: only relevant if the old process left a stale file

	if err := svc.StartPerimeterOnly(ctx, sb, in.seedCA); err != nil {
		return fmt.Errorf("supervisor: %s: start perimeter: %w", in.logPrefix, err)
	}

	var perimSupPtr atomic.Pointer[perimeter.PerimeterSupervisor]
	if sup := svc.GetPerimeterSupervisor(sb.ID); sup != nil {
		perimSupPtr.Store(sup)
	}

	binaryHash, hashErr := computeBinaryHash()
	if hashErr != nil {
		slog.Warn(in.logPrefix+".binary_hash_failed", "err", hashErr)
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

	// agentHealthFn probes the guest agent's control/data planes live, using
	// the same drv+sb.ID this process dials the guest through for every other
	// RPC. sb was resolved successfully by the caller before it acquired the
	// runtime, so there is no resolve-failure branch to degrade here.
	agentHealthFn := agentHealthFunc(func(hctx context.Context) AgentHealth {
		return checkAgentHealth(hctx, drv, sb.ID)
	})
	ipcH, err := serveIPC(ctx, sockPath, svc, cfg.SandboxRef, allowEgressFn, handoffFn, agentHealthFn, binaryHash)
	if err != nil {
		return fmt.Errorf("supervisor: %s: bind IPC socket %s: %w", in.logPrefix, sockPath, err)
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

	for _, r := range in.refreshers {
		r.Register(sb.ID)
	}
	for _, r := range in.refreshers {
		go func(r *cred.Refresher) {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, _, tokErr := r.Token(ctx); tokErr != nil {
						slog.Warn(in.logPrefix+".token_refresh_failed", "host", r.Host(), "err", tokErr)
					}
				}
			}
		}(r)
	}

	// NOTE: unlike RunDetached, neither adoption path re-seeds the guest agent
	// placeholder credentials — the guest was never rebooted, so it already
	// holds its placeholder.

	pid := os.Getpid()
	pidfile := PidfilePath(cfg.StateDir)
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(pid)+"\n"), statedir.FileMode); err != nil {
		return fmt.Errorf("supervisor: %s: write pidfile %s: %w", in.logPrefix, pidfile, err)
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

	slog.Info(in.logPrefix+".ready", "sandboxRef", cfg.SandboxRef, "pid", pid, "sock", sockPath)

	// Wire the VM-death channel for the acquired runtime (AC-12a/b): if the
	// VM dies unexpectedly, awaitShutdown returns shutdownByVMDeath and the
	// teardown block reconciles the record. nil is safe — a nil channel is
	// never selected.
	vmDeadCh := drv.RuntimeDeathCh(sb.ID)
	cause := awaitShutdown(ctx, stopCh, detachCh, vmDeadCh)
	if cause == shutdownByDetach {
		slog.Info(in.logPrefix+".detached", "sandboxRef", cfg.SandboxRef)
		return nil
	}

	// shutdownByVMDeath: the VM died unexpectedly. Reconcile the store record
	// to Stopped/MemoryLost without calling svc.Stop() — the VM is already
	// gone and driver.Stop on a dead pgid would overwrite the reason.
	if cause == shutdownByVMDeath {
		slog.Warn(in.logPrefix+".vm_died", "sandboxRef", cfg.SandboxRef)
		reconCtx, reconCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer reconCancel()
		if err := reconcileVMDeath(reconCtx, st, sb.ID); err != nil {
			slog.Warn(in.logPrefix+".vm_died_record_update_failed",
				"sandboxRef", cfg.SandboxRef, "err", err)
		}
		slog.Info(in.logPrefix+".exited", "sandboxRef", cfg.SandboxRef, "cause", "vm_died")
		return nil
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if _, stopErr := svc.Stop(stopCtx, cfg.SandboxRef); stopErr != nil {
		slog.Warn(in.logPrefix+".stop_failed", "sandboxRef", cfg.SandboxRef, "err", stopErr)
	}
	slog.Info(in.logPrefix+".exited", "sandboxRef", cfg.SandboxRef)
	return nil
}
