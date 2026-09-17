package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/IniZio/nexus/internal/core/builder"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/govern"
	"github.com/IniZio/nexus/internal/core/perimeter"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/statedir"
	"github.com/IniZio/nexus/internal/core/store"
)

// governClock is the clock every governor the supervisor starts runs on. nil
// means the wall clock. It is a seam for tests only: the governor's boot
// delay and eval interval are real seconds, and a test that watches an
// in-process supervisor drive a resize has no other way to skip them.
var governClock govern.Clock

type serveAdoptedInput struct {
	cfg Config
	st  store.Store
	svc *service.Service
	drv *cloudhypervisor.CHDriver
	sb  domain.Sandbox
	// seedCA: MITM CA for StartPerimeterOnly (D-HSH-18), nil on CA loss.
	seedCA *service.CASeed
	// refreshers: credential refreshers to keep warm.
	refreshers []*cred.Refresher
	// waitForPID: previous supervisor to await before binding IPC socket; zero on crash path.
	waitForPID int
	// logPrefix: distinguishes "supervisor.adopt" vs "supervisor.reacquire" log events.
	logPrefix string
	// startPerimeterFn: injected in tests to bypass perimeter setup.
	startPerimeterFn func(ctx context.Context, sb domain.Sandbox, seed *service.CASeed) error
}

// serveAdoptedSupervisor runs the shared tail of both acquisition paths ([RunAdopt], [RunReacquire]).
// Does not return until supervisor shuts down.
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

	// ── Re-acquire the builder cache-disk slot(s) this VM occupies ───────
	// D-HSH-07: take the same slot to avoid collision with CH's write lock.
	cacheSlots := builder.DecodeCacheDiskSlots(sb.CacheDiskSlot)
	cacheLeases, err := acquireCacheDiskLeases(ctx, cacheSlots, nil, cacheDiskAdoptLeaseTimeout)
	if err != nil {
		return fmt.Errorf("supervisor: %s: %w", in.logPrefix, err)
	}
	defer builder.ReleaseCacheDiskLeases(cacheLeases)

	var perimErr error
	if in.startPerimeterFn != nil {
		perimErr = in.startPerimeterFn(ctx, sb, in.seedCA)
	} else {
		perimErr = svc.StartPerimeterOnly(ctx, sb, in.seedCA)
	}
	if perimErr != nil {
		return fmt.Errorf("supervisor: %s: start perimeter: %w", in.logPrefix, perimErr)
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
		// ticket 14: runtime-derived, not record-derived
		return handoffFromLiveSupervisor(hctx, peerSock, sup, cfg.SandboxRef, bootVCPUs, cfg.MemoryMiB)
	})

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
		Clock:     governClock,
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

	for _, lm := range cfg.LiveMounts {
		if lm.GuestPath == "/root/.claude" && !lm.ReadOnly {
			home, homeErr := os.UserHomeDir()
			if homeErr == nil {
				credsPath := filepath.Join(home, ".claude", ".credentials.json")
				g := cred.NewCredGuardian(credsPath)
				go g.Guard(ctx)
				slog.Info(in.logPrefix+".cred_guardian_armed", "path", credsPath)
			} else {
				slog.Warn(in.logPrefix+".cred_guardian_arm_failed", "err", homeErr)
			}
			break
		}
	}

	// ── git SSH relay ────────────────────────────────────────────────────────
	startGitSSHRelay(ctx, cfg.SocketDir, sb, nil)

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

	vmDeadCh := drv.RuntimeDeathCh(sb.ID)
	cause := awaitShutdown(ctx, stopCh, detachCh, vmDeadCh)
	if cause == shutdownByDetach {
		slog.Info(in.logPrefix+".detached", "sandboxRef", cfg.SandboxRef)
		return nil
	}

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
