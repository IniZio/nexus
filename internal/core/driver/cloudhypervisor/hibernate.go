package cloudhypervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

var _ driver.Hibernator = (*CHDriver)(nil)

const (
	hibernateSnapDir   = "snap"
	hibernateManifest  = "manifest.json"
	hibernateCommitted = "COMMITTED"
	hibernateTmpPrefix = ".tmp-"
	hibernateOldPrefix = ".old-"
	// hibernateAttempted is written into snap/ before a restore starts. A
	// restore that crashed or failed leaves it, so the snapshot is never
	// automatically reused. hibernateRestored replaces it after a successful
	// restore: the VM has diverged from the snapshot, which stays on disk
	// (until the next hibernate or rm) but must not be restored a second time.
	hibernateAttempted = ".attempted"
	hibernateRestored  = ".restored"
)

// Test seams for failure injection.
var (
	probeOnDemand    = ProbeOnDemandRestore
	chBinaryVersion  = runningCHVersion
	startRestoreProc = StartNetnsRuntime
	waitAgentReady   = (*CHDriver).dialAgentReady
	hibernateRename  = os.Rename
	hibernateFsync   = fsyncSnapDir
	// hibernateSyncDisks flushes every guest disk image to stable storage
	// while the VM is paused, before COMMITTED is written.
	hibernateSyncDisks = syncDiskImages
	// restoreMarkerSync makes the .attempted marker durable before the VM starts.
	restoreMarkerSync = func(marker, snap string) error {
		if err := syncFile(marker); err != nil {
			return err
		}
		return syncDir(snap)
	}
)

// syncDiskImages fsyncs each disk image path.
func syncDiskImages(paths []string) error {
	for _, p := range paths {
		if err := syncFile(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// hibernateManifestDoc is manifest.json inside <dir>/snap. Files excludes
// manifest.json and COMMITTED themselves.
type hibernateManifestDoc struct {
	snapshotManifest
	MemoryBytes  uint64   `json:"memory_bytes,omitempty"`
	BalloonBytes uint64   `json:"balloon_bytes,omitempty"`
	CHVersion    string   `json:"ch_version,omitempty"`
	Disks        []string `json:"disks,omitempty"`
}

// ValidHibernateDir reports whether dir holds a fully committed hibernate
// snapshot: COMMITTED marker present, manifest parses, every listed file
// exists at its recorded size. A dir torn before the marker is invalid.
func ValidHibernateDir(dir string) bool {
	snap := filepath.Join(dir, hibernateSnapDir)
	if _, err := os.Stat(filepath.Join(snap, hibernateCommitted)); err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(snap, hibernateManifest))
	if err != nil {
		return false
	}
	var m hibernateManifestDoc
	if json.Unmarshal(b, &m) != nil || len(m.Files) == 0 {
		return false
	}
	m.Dir = snap
	return verifyManifest(m.snapshotManifest) == nil
}

// chVersion asks the VMM for its version via vmm.ping (best effort).
func (c *client) chVersion(ctx context.Context) string {
	resp, err := c.do(ctx, http.MethodGet, "/vmm.ping", nil)
	if err != nil {
		return ""
	}
	defer drainClose(resp)
	var v struct {
		Version string `json:"version"`
	}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &v)
	return v.Version
}

func syncDir(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncFile(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// HibernateTo pauses the VM, snapshots it into dir/snap and leaves it
// PAUSED. The caller kills the VMM (see StopVMM) after recording state.
// On failure before COMMITTED the tmp dir is removed and, if this call paused
// the VM, it is resumed.
func (d *CHDriver) HibernateTo(ctx context.Context, id domain.SandboxID, dir string) (res driver.HibernateResult, err error) {
	start := time.Now()
	fail := func(format string, a ...any) (driver.HibernateResult, error) {
		return driver.HibernateResult{}, fmt.Errorf("cloudhypervisor: hibernate %s: "+format, append([]any{id}, a...)...)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail("mkdir %q: %w", dir, err)
	}
	reapHibernateTmp(dir)
	tmp, err := os.MkdirTemp(dir, hibernateTmpPrefix)
	if err != nil {
		return fail("mkdir tmp: %w", err)
	}

	c := newClient(d.socketPath(id))
	infoCtx, infoCancel := d.callCtx(ctx)
	info, state, ierr := c.VMInfoFull(infoCtx)
	infoCancel()
	if ierr != nil {
		_ = os.RemoveAll(tmp)
		return fail("vm.info: %w", ierr)
	}

	paused := false
	var pauseMs int64
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = os.RemoveAll(tmp)
		if paused {
			rc, cancel := d.callCtx(context.WithoutCancel(ctx))
			_ = c.VMResume(rc)
			cancel()
		}
	}()

	if state != driver.Paused {
		t0 := time.Now()
		pc, cancel := d.callCtx(ctx)
		perr := c.VMPause(pc)
		cancel()
		if perr != nil {
			return fail("pause: %w", perr)
		}
		paused = true
		pauseMs = time.Since(t0).Milliseconds()
	}

	t1 := time.Now()
	if serr := c.VMSnapshot(ctx, "file://"+tmp); serr != nil {
		return fail("vm.snapshot: %w", serr)
	}
	snapshotMs := time.Since(t1).Milliseconds()

	if ferr := hibernateFsync(tmp); ferr != nil {
		return fail("fsync: %w", ferr)
	}
	m, merr := buildManifest(tmp)
	if merr != nil {
		return fail("build manifest: %w", merr)
	}
	doc := hibernateManifestDoc{snapshotManifest: m, CHVersion: c.chVersion(ctx)}
	if info != nil && info.Config != nil {
		if info.Config.Memory != nil {
			doc.MemoryBytes = info.Config.Memory.SizeBytes
		}
		if info.Config.Balloon != nil {
			doc.BalloonBytes = info.Config.Balloon.SizeBytes
		}
		for _, dk := range info.Config.Disks {
			doc.Disks = append(doc.Disks, dk.Path)
		}
	}
	doc.Dir = ""
	// Device I/O is quiesced while paused: flush every disk image before
	// COMMITTED so a reusable snapshot never sits next to disks missing
	// writes the guest saw complete.
	if derr := hibernateSyncDisks(doc.Disks); derr != nil {
		return fail("fsync disks: %w", derr)
	}
	mb, jerr := json.MarshalIndent(doc, "", "  ")
	if jerr != nil {
		return fail("marshal manifest: %w", jerr)
	}
	mpath := filepath.Join(tmp, hibernateManifest)
	if werr := os.WriteFile(mpath, mb, 0o600); werr != nil {
		return fail("write manifest: %w", werr)
	}
	if ferr := syncFile(mpath); ferr != nil {
		return fail("fsync manifest: %w", ferr)
	}
	if ferr := syncDir(tmp); ferr != nil {
		return fail("fsync tmp dir: %w", ferr)
	}

	final := filepath.Join(dir, hibernateSnapDir)
	var aside string
	if _, serr := os.Stat(final); serr == nil {
		aside = filepath.Join(dir, hibernateOldPrefix+filepath.Base(tmp)[len(hibernateTmpPrefix):])
		if rerr := hibernateRename(final, aside); rerr != nil {
			return fail("move old snap aside: %w", rerr)
		}
	}
	if rerr := hibernateRename(tmp, final); rerr != nil {
		if aside != "" {
			_ = os.Rename(aside, final)
		}
		return fail("rename tmp to snap: %w", rerr)
	}
	marker := filepath.Join(final, hibernateCommitted)
	cerr := os.WriteFile(marker, []byte("committed\n"), 0o600)
	if cerr == nil {
		cerr = syncFile(marker)
	}
	if cerr == nil {
		cerr = syncDir(final)
	}
	if cerr == nil {
		cerr = syncDir(dir)
	}
	if cerr != nil {
		// Undo: drop the uncommitted snap, restore the old one.
		_ = os.RemoveAll(final)
		if aside != "" {
			_ = os.Rename(aside, final)
		}
		return fail("commit marker: %w", cerr)
	}
	committed = true
	if aside != "" {
		_ = os.RemoveAll(aside)
	}

	var logical, onDisk int64
	_ = filepath.WalkDir(final, func(p string, de fs.DirEntry, werr error) error {
		if werr != nil || !de.Type().IsRegular() {
			return nil
		}
		if fi, e := de.Info(); e == nil {
			logical += fi.Size()
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				onDisk += st.Blocks * 512
			}
		}
		return nil
	})
	return driver.HibernateResult{
		PauseMs:             pauseMs,
		SnapshotMs:          snapshotMs,
		TotalMs:             time.Since(start).Milliseconds(),
		SnapshotBytes:       logical,
		SnapshotBytesOnDisk: onDisk,
	}, nil
}

// reapHibernateTmp removes torn .tmp-*/.old-* leftovers from earlier crashes.
func reapHibernateTmp(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, hibernateTmpPrefix) || strings.HasPrefix(n, hibernateOldPrefix) {
			_ = os.RemoveAll(filepath.Join(dir, n))
		}
	}
}

// StopVMM kills the VMM after a committed HibernateTo. Same teardown as Stop.
func (d *CHDriver) StopVMM(ctx context.Context, id domain.SandboxID) error {
	return d.Stop(ctx, id)
}

// HibernateReusable reports whether dir holds a committed snapshot that has
// not yet been tried: ValidHibernateDir and neither .attempted nor .restored.
func HibernateReusable(dir string) bool {
	if !ValidHibernateDir(dir) {
		return false
	}
	snap := filepath.Join(dir, hibernateSnapDir)
	for _, m := range []string{hibernateAttempted, hibernateRestored} {
		if _, err := os.Stat(filepath.Join(snap, m)); err == nil {
			return false
		}
	}
	return true
}

// runningCHVersion returns the version of the CH binary ("v52.0"), the last
// field of the first line of `cloud-hypervisor --version` (later lines carry
// e.g. "Migration Protocol Versions: 0"). vmm.ping spells it "52.0.0";
// compare with sameCHVersion.
func runningCHVersion(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	f := strings.Fields(first)
	if len(f) == 0 {
		return "", fmt.Errorf("empty --version output")
	}
	return f[len(f)-1], nil
}

// sameCHVersion compares a `--version` string ("v53.0") with a vmm.ping one
// ("53.0.0"): strip a leading "v" and trailing ".0" components.
func sameCHVersion(a, b string) bool {
	norm := func(v string) string {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		for strings.HasSuffix(v, ".0") {
			v = strings.TrimSuffix(v, ".0")
		}
		return v
	}
	return a != "" && b != "" && norm(a) == norm(b)
}

// restoreAndResume issues vm.restore in mode and then vm.resume. Shared by the
// netns child (RunNetnsChild) and RestoreInPlace tests.
func restoreAndResume(ctx context.Context, c *client, sourceURL string, mode RestoreMode) error {
	if err := c.VMRestoreMode(ctx, sourceURL, mode); err != nil {
		return err
	}
	if err := c.VMResume(ctx); err != nil {
		return fmt.Errorf("vm.resume: %w", err)
	}
	return nil
}

// currentDiskPaths mirrors the disk set Start attaches: root, then ExtraDisks.
func (d *CHDriver) currentDiskPaths(id domain.SandboxID) []string {
	root := d.cfg.DiskImagePath
	if root == "" && d.cfg.DiskDir != "" {
		cand := filepath.Join(d.cfg.DiskDir, id.String()+".raw")
		if _, err := os.Stat(cand); err == nil {
			root = cand
		}
	}
	var out []string
	if root != "" {
		out = append(out, root)
		for _, ed := range d.cfg.ExtraDisks {
			out = append(out, ed.Path)
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dialAgentReady polls the guest agent control port until it accepts.
func (d *CHDriver) dialAgentReady(ctx context.Context, id domain.SandboxID) error {
	var last error
	for {
		dc, cancel := context.WithTimeout(ctx, vsockHandshakeTimeout)
		conn, err := d.DialGuest(dc, id, driver.AgentControlPort)
		cancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("guest agent not ready: %w (last dial: %v)", ctx.Err(), last)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// RestoreInPlace revives the hibernated sandbox id from dir/snap: same id, same
// disks, same vsock path. Validation failures return driver.ErrHibernateInvalid
// or driver.ErrHibernateIncompatible before anything is started or written.
// Any failure after the .attempted marker is written tears the half-started
// VMM and netns child down; the marker stays, so the snapshot is not reused.
// On success .attempted is renamed to .restored (see hibernateRestored).
func (d *CHDriver) RestoreInPlace(ctx context.Context, id domain.SandboxID, dir string, opts driver.RestoreOptions) (res driver.RestoreResult, err error) {
	start := time.Now()
	fail := func(kind error, format string, a ...any) (driver.RestoreResult, error) {
		msg := fmt.Sprintf(format, a...)
		if kind == nil {
			return driver.RestoreResult{}, fmt.Errorf("cloudhypervisor: restore %s: %s", id, msg)
		}
		return driver.RestoreResult{}, fmt.Errorf("cloudhypervisor: restore %s: %s: %w", id, msg, kind)
	}
	mode, perr := ParseRestoreMode(string(opts.Mode))
	if perr != nil {
		return fail(nil, "%v", perr)
	}
	if !HibernateReusable(dir) {
		return fail(driver.ErrHibernateInvalid, "%s is not a fresh committed snapshot", dir)
	}
	snap := filepath.Join(dir, hibernateSnapDir)
	var m hibernateManifestDoc
	if b, rerr := os.ReadFile(filepath.Join(snap, hibernateManifest)); rerr != nil || json.Unmarshal(b, &m) != nil {
		return fail(driver.ErrHibernateInvalid, "unreadable manifest")
	}
	have, verr := chBinaryVersion(d.cfg.BinaryPath)
	if verr != nil {
		return fail(driver.ErrHibernateIncompatible, "cloud-hypervisor version: %v", verr)
	}
	if !sameCHVersion(have, m.CHVersion) {
		return fail(driver.ErrHibernateIncompatible, "cloud-hypervisor version %q != snapshot %q", have, m.CHVersion)
	}
	if cur := d.currentDiskPaths(id); !sameStrings(cur, m.Disks) {
		return fail(driver.ErrHibernateIncompatible, "disks %v != snapshot %v", cur, m.Disks)
	}
	if mode == RestoreModeOnDemand {
		if perr := probeOnDemand(); perr != nil {
			return fail(driver.ErrHibernateIncompatible, "%v", perr)
		}
	}

	socketPath := d.socketPath(id)
	{
		pc, cancel := context.WithTimeout(ctx, probeTimeout)
		pingErr := newClient(socketPath).Ping(pc)
		cancel()
		switch {
		case pingErr == nil:
			return fail(ErrVMMAlreadyBound, "%s", socketPath)
		case isAbsent(pingErr):
			_ = os.Remove(socketPath)
			d.removeStaleVsock(id)
		default:
			return fail(nil, "pre-flight ping %s: %v", socketPath, pingErr)
		}
	}

	marker := filepath.Join(snap, hibernateAttempted)
	if werr := os.WriteFile(marker, []byte("attempted\n"), 0o600); werr != nil {
		return fail(nil, "write %s: %v", hibernateAttempted, werr)
	}
	if serr := restoreMarkerSync(marker, snap); serr != nil {
		return fail(driver.ErrHibernateInvalid, "fsync %s: %v", hibernateAttempted, serr)
	}

	cfg := d.cfg
	cfg.restoreMode = mode
	rt, serr := startRestoreProc(ctx, cfg, id, socketPath, "file://"+snap)
	if serr != nil {
		return fail(nil, "start netns runtime: %v", serr)
	}
	d.mu.Lock()
	d.nets[id] = &netState{rt: rt, perimConn: rt.PerimConn}
	d.mu.Unlock()
	ok := false
	defer func() {
		if !ok {
			d.clearState(id)
		}
	}()
	withStderr := func(e error) error {
		if tail := rt.ChildStderr(); tail != "" {
			return fmt.Errorf("%w\nVMM stderr:\n%s", e, tail)
		}
		return e
	}

	timeout := d.cfg.StartTimeout
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	pollCtx, pollCancel := context.WithTimeout(ctx, timeout)
	defer pollCancel()
	c := newClient(socketPath)
	for {
		if e := pollCtx.Err(); e != nil {
			return driver.RestoreResult{}, withStderr(fmt.Errorf("cloudhypervisor: restore %s: not running within %s: %w", id, timeout, e))
		}
		if st, ierr := c.VMInfo(pollCtx); ierr == nil && st == driver.Running {
			break
		}
		select {
		case <-rt.DeathCh():
			return driver.RestoreResult{}, withStderr(fmt.Errorf("cloudhypervisor: restore %s: netns child exited during restore", id))
		case <-pollCtx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
	restoreMs := time.Since(start).Milliseconds()

	t1 := time.Now()
	if aerr := waitAgentReady(d, pollCtx, id); aerr != nil {
		return driver.RestoreResult{}, withStderr(fmt.Errorf("cloudhypervisor: restore %s: %w", id, aerr))
	}
	agentMs := time.Since(t1).Milliseconds()

	if iid, e := newInstanceID(); e == nil {
		_ = d.writeInstanceID(id, iid)
	}
	_ = os.Rename(marker, filepath.Join(snap, hibernateRestored))
	_ = syncDir(snap)
	ok = true
	return driver.RestoreResult{
		RestoreMs:    restoreMs,
		AgentReadyMs: agentMs,
		TotalMs:      time.Since(start).Milliseconds(),
		Mode:         mode,
	}, nil
}
