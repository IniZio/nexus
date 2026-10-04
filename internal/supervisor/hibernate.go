package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/service"
)

// ipcHibernatePath is the HTTP path for POST /supervisor/hibernate: snapshot
// the VM, record Hibernated, stop the VMM, respond, then exit this supervisor.
const ipcHibernatePath = "/supervisor/hibernate"

// HibernateRequestTimeout bounds RequestHibernate; large snapshots are slow.
var HibernateRequestTimeout = 120 * time.Second

// hibernateStopTimeout bounds how long the VM-death branch waits for an
// in-flight hibernate to settle.
const hibernateStopTimeout = 2 * time.Minute

// hibernateService is the subset of service.Service the controller uses.
type hibernateService interface {
	Hibernate(ctx context.Context, ref string, opts ...service.HibernateOption) (service.HibernateOutcome, error)
}

// hibernateResponse is the JSON body of /supervisor/hibernate.
type hibernateResponse struct {
	OK                  bool   `json:"ok"`
	Already             bool   `json:"already,omitempty"`
	PauseMs             int64  `json:"pause_ms,omitempty"`
	SnapshotMs          int64  `json:"snapshot_ms,omitempty"`
	TotalMs             int64  `json:"total_ms,omitempty"`
	SnapshotBytes       int64  `json:"snapshot_bytes,omitempty"`
	SnapshotBytesOnDisk int64  `json:"snapshot_bytes_on_disk,omitempty"`
	Error               string `json:"error,omitempty"`
	// Code names the service sentinel behind Error so the client can restore
	// it: unsupported | refused | snapshot_failed.
	Code string `json:"code,omitempty"`
}

func hibernateErrCode(err error) string {
	switch {
	case errors.Is(err, service.ErrHibernateUnsupported):
		return "unsupported"
	case errors.Is(err, service.ErrHibernateRefused):
		return "refused"
	case errors.Is(err, service.ErrSnapshotFailed):
		return "snapshot_failed"
	}
	return ""
}

func hibernateErrFromCode(code, msg string) error {
	var sentinel error
	switch code {
	case "unsupported":
		sentinel = service.ErrHibernateUnsupported
	case "refused":
		sentinel = service.ErrHibernateRefused
	case "snapshot_failed":
		sentinel = service.ErrSnapshotFailed
	default:
		return errors.New(msg)
	}
	return fmt.Errorf("%s: %w", msg, sentinel)
}

// hibernateFunc runs one hibernate. exit is true when the VMM is gone and the
// supervisor must exit once the response is written.
type hibernateFunc func(ctx context.Context) (out service.HibernateOutcome, exit bool, err error)

// hibernateCtl drives a hibernate and owns the "expecting VMM exit" flag that
// suppresses reconcileVMDeath.
type hibernateCtl struct {
	svc hibernateService
	hib driver.Hibernator // nil: driver cannot hibernate
	ref string

	expecting  atomic.Bool
	hibernated atomic.Bool // response flushed; supervisor must exit
	exitCh     chan struct{}
	exitOnce   sync.Once

	mu      sync.Mutex
	quiesce func()
	resume  func()

	// diskPaths are the sandbox's root + extra disk images, fsynced after the
	// VMM stops. syncFn and killFn are seams for tests.
	diskPaths []string
	syncFn    func(path string) error
	killFn    func(sb domain.Sandbox) error
}

// hibernateAttemptedMarker mirrors cloudhypervisor's .attempted marker: a
// snapshot dir holding it is never restored; resume cold-starts instead.
const hibernateAttemptedMarker = ".attempted"

// setDisks records the disk images fsynced after a successful VMM stop.
func (c *hibernateCtl) setDisks(paths []string) { c.diskPaths = paths }

// markSnapshotUnusable writes snap/.attempted so the next resume cold-starts.
func markSnapshotUnusable(hibernateDir string) error {
	if hibernateDir == "" {
		return errors.New("no hibernate dir on record")
	}
	return os.WriteFile(filepath.Join(hibernateDir, "snap", hibernateAttemptedMarker), nil, 0o600)
}

func fsyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// syncDisks fsyncs every disk image so a host crash cannot leave disks older
// than the memory snapshot.
func (c *hibernateCtl) syncDisks() error {
	fn := c.syncFn
	if fn == nil {
		fn = fsyncFile
	}
	for _, p := range c.diskPaths {
		if err := fn(p); err != nil {
			return fmt.Errorf("fsync %s: %w", p, err)
		}
	}
	return nil
}

// killVMMGroup SIGKILLs the netns child's process group (child + CH) after
// verifying identity: the pid's starttime and pgid must match the record and
// its cmdline must name a nexus or cloud-hypervisor binary. A vanished pid is
// success. procRoot and kill are seams for tests.
func killVMMGroup(sb domain.Sandbox, procRoot string, kill func(int, syscall.Signal) error) error {
	pid, pgid := sb.NetnsChildPID, sb.NetnsChildPGID
	if pid <= 0 || pgid <= 0 {
		return errors.New("no VMM pid recorded")
	}
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read stat: %w", err)
	}
	line := string(stat)
	idx := strings.LastIndex(line, ")")
	if idx < 0 {
		return errors.New("parse stat: no ')'")
	}
	f := strings.Fields(line[idx+1:])
	if len(f) < 20 {
		return errors.New("parse stat: too few fields")
	}
	if got, _ := strconv.Atoi(f[2]); got != pgid {
		return fmt.Errorf("pid %d pgid %d != recorded %d: refusing to signal", pid, got, pgid)
	}
	if sb.NetnsChildStartTime != 0 {
		if st, _ := strconv.ParseUint(f[19], 10, 64); st != sb.NetnsChildStartTime {
			return fmt.Errorf("pid %d starttime %d != recorded %d: refusing to signal", pid, st, sb.NetnsChildStartTime)
		}
	}
	cmd, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return fmt.Errorf("read cmdline: %w", err)
	}
	argv0 := filepath.Base(strings.SplitN(string(cmd), "\x00", 2)[0])
	if !strings.Contains(argv0, "nexus") && !strings.Contains(argv0, "cloud-hypervisor") {
		return fmt.Errorf("pid %d cmdline %q is not a VMM: refusing to signal", pid, argv0)
	}
	if err := kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill pgid %d: %w", pgid, err)
	}
	return nil
}

// stopOrKill stops the VMM: StopAfterHibernate, one retry, then a hard kill by
// verified pid. A nil return means no VMM remains.
func (c *hibernateCtl) stopOrKill(ctx context.Context, sb domain.Sandbox) error {
	serr := c.hib.StopAfterHibernate(ctx, sb.ID)
	if serr == nil {
		return nil
	}
	slog.Warn("supervisor.hibernate.stop_failed_retry", "ref", c.ref, "err", serr)
	if serr = c.hib.StopAfterHibernate(ctx, sb.ID); serr == nil {
		return nil
	}
	kill := c.killFn
	if kill == nil {
		kill = func(sb domain.Sandbox) error { return killVMMGroup(sb, "/proc", syscall.Kill) }
	}
	if kerr := kill(sb); kerr != nil {
		return fmt.Errorf("%w; hard kill: %v", serr, kerr)
	}
	slog.Warn("supervisor.hibernate.stop_failed_killed", "ref", c.ref, "err", serr)
	return nil
}

func newHibernateCtl(svc hibernateService, hib driver.Hibernator, ref string) *hibernateCtl {
	return &hibernateCtl{svc: svc, hib: hib, ref: ref, exitCh: make(chan struct{})}
}

// bindGovernor registers the governor quiesce/resume hooks (late-bound: the
// governor starts after the IPC socket).
func (c *hibernateCtl) bindGovernor(quiesce, resume func()) {
	c.mu.Lock()
	c.quiesce, c.resume = quiesce, resume
	c.mu.Unlock()
}

func (c *hibernateCtl) govHooks() (func(), func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nop := func() {}
	q, r := c.quiesce, c.resume
	if q == nil {
		q = nop
	}
	if r == nil {
		r = nop
	}
	return q, r
}

// Expecting reports whether a VMM exit is expected (hibernate in flight or done).
func (c *hibernateCtl) Expecting() bool { return c != nil && c.expecting.Load() }

// ExitCh is closed once a hibernate succeeded and its response was written.
func (c *hibernateCtl) ExitCh() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.exitCh
}

// markResponded is called by the IPC handler after the success response is
// flushed: it releases the supervisor to exit.
func (c *hibernateCtl) markResponded() {
	c.hibernated.Store(true)
	c.exitOnce.Do(func() { close(c.exitCh) })
}

// waitSettled is used by the VM-death branch: when a hibernate is in flight it
// waits for the outcome and reports whether the sandbox ended up hibernated.
func (c *hibernateCtl) waitSettled(ctx context.Context) bool {
	if c == nil {
		return false
	}
	deadline := time.Now().Add(hibernateStopTimeout)
	for {
		if c.hibernated.Load() {
			return true
		}
		if !c.expecting.Load() || time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Do runs one hibernate attempt.
func (c *hibernateCtl) Do(ctx context.Context) (service.HibernateOutcome, bool, error) {
	if c.hib == nil {
		return service.HibernateOutcome{}, false, service.ErrHibernateUnsupported
	}
	if !c.expecting.CompareAndSwap(false, true) {
		return service.HibernateOutcome{}, false, errors.New("supervisor: hibernate already in progress")
	}
	quiesce, resume := c.govHooks()
	quiesce()
	out, err := c.svc.Hibernate(ctx, c.ref, service.WithSkipVMMStop())
	if err != nil || out.Already {
		c.expecting.Store(false)
		resume()
		return out, false, err
	}
	// Record is Hibernated. Stop the VMM; the flag stays set either way: the
	// record is committed, so this supervisor must not reconcile or resume.
	// A failure that leaves a VMM alive or disks unsynced makes the snapshot
	// unusable (.attempted), so resume cold-starts; the record stays Hibernated.
	if serr := c.stopOrKill(ctx, out.Sandbox); serr != nil {
		c.failUnusable(out.Sandbox, serr)
		return out, true, fmt.Errorf("supervisor: hibernated but stop vmm failed (snapshot marked unusable): %w", serr)
	}
	if err := c.syncDisks(); err != nil {
		c.failUnusable(out.Sandbox, err)
		return out, true, fmt.Errorf("supervisor: hibernated but disk sync failed (snapshot marked unusable): %w", err)
	}
	return out, true, nil
}

func (c *hibernateCtl) failUnusable(sb domain.Sandbox, cause error) {
	if merr := markSnapshotUnusable(sb.HibernateDir); merr != nil {
		slog.Error("supervisor.hibernate.mark_unusable_failed", "ref", c.ref, "cause", cause, "err", merr)
		return
	}
	slog.Error("supervisor.hibernate.snapshot_unusable", "ref", c.ref, "cause", cause)
}

func toHibernateResponse(o service.HibernateOutcome) hibernateResponse {
	return hibernateResponse{
		OK: true, Already: o.Already,
		PauseMs: o.Result.PauseMs, SnapshotMs: o.Result.SnapshotMs, TotalMs: o.Result.TotalMs,
		SnapshotBytes: o.Result.SnapshotBytes, SnapshotBytesOnDisk: o.Result.SnapshotBytesOnDisk,
	}
}

func hibernateHandler(fn hibernateFunc, markResponded func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if fn == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(hibernateResponse{Error: "hibernate not available"})
			return
		}
		// The snapshot must finish even if the client gives up.
		out, exit, err := fn(context.WithoutCancel(r.Context()))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(hibernateResponse{Error: err.Error(), Code: hibernateErrCode(err)})
		} else {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(toHibernateResponse(out))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if exit && markResponded != nil {
			markResponded()
		}
	}
}

// RequestHibernate asks the supervisor in stateDir to hibernate its sandbox.
// It is bounded by HibernateRequestTimeout.
func RequestHibernate(ctx context.Context, stateDir string) (service.HibernateOutcome, error) {
	sockPath := SockPath(stateDir)
	ctx, cancel := context.WithTimeout(ctx, HibernateRequestTimeout)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+ipcHibernatePath, nil)
	if err != nil {
		return service.HibernateOutcome{}, fmt.Errorf("hibernate supervisor: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return service.HibernateOutcome{}, fmt.Errorf("hibernate supervisor: request: %w", err)
	}
	defer resp.Body.Close()
	var body hibernateResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return service.HibernateOutcome{}, fmt.Errorf("hibernate supervisor: status %d: decode response: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || !body.OK {
		return service.HibernateOutcome{}, fmt.Errorf("hibernate supervisor: status %d: %w", resp.StatusCode, hibernateErrFromCode(body.Code, body.Error))
	}
	return service.HibernateOutcome{
		Already: body.Already,
		Result: driver.HibernateResult{
			PauseMs: body.PauseMs, SnapshotMs: body.SnapshotMs, TotalMs: body.TotalMs,
			SnapshotBytes: body.SnapshotBytes, SnapshotBytesOnDisk: body.SnapshotBytesOnDisk,
		},
	}, nil
}

// refuseHibernated rejects supervisor adopt/reacquire of a Hibernated sandbox:
// no VMM exists to adopt; resume is a separate path.
func refuseHibernated(sb domain.Sandbox) error {
	if sb.State == domain.Hibernated {
		return fmt.Errorf("supervisor: sandbox %s is hibernated: nothing to adopt", sb.ID)
	}
	return nil
}

// govRunner runs the governor so it can be paused (no resize during
// pause+snapshot) and restarted after a failed hibernate.
type govRunner struct {
	parent context.Context
	run    func(context.Context)
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newGovRunner(parent context.Context, run func(context.Context)) *govRunner {
	g := &govRunner{parent: parent, run: run}
	g.Resume()
	return g
}

// Resume starts the governor if it is not running.
func (g *govRunner) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(g.parent)
	done := make(chan struct{})
	g.cancel, g.done = cancel, done
	go func() { defer close(done); g.run(ctx) }()
}

// Quiesce stops the governor and waits for any in-flight resize to return.
func (g *govRunner) Quiesce() {
	g.mu.Lock()
	cancel, done := g.cancel, g.done
	g.cancel, g.done = nil, nil
	g.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// hibernateDiskPaths lists root then extra disk images, skipping an empty root.
func hibernateDiskPaths(root string, extra []string) []string {
	if root == "" {
		return nil
	}
	return append([]string{root}, extra...)
}
