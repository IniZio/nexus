package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/service"
)

type seqHibDrv struct {
	fakeHibDrv
	errs []error // per StopAfterHibernate call
}

func (d *seqHibDrv) StopAfterHibernate(context.Context, domain.SandboxID) error {
	n := int(d.stops.Add(1)) - 1
	if n < len(d.errs) {
		return d.errs[n]
	}
	return nil
}

func hibCtlWithSnap(t *testing.T, drv driver.Hibernator) (*hibernateCtl, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "snap"), 0o700); err != nil {
		t.Fatal(err)
	}
	svc := &fakeHibSvc{out: service.HibernateOutcome{Sandbox: domain.Sandbox{HibernateDir: dir}}}
	return newHibernateCtl(svc, drv, "ref"), dir
}

func attempted(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "snap", ".attempted"))
	return err == nil
}

func TestHibernateDo_FsyncsDisksAfterStop(t *testing.T) {
	drv := &fakeHibDrv{}
	ctl, dir := hibCtlWithSnap(t, drv)
	var synced []string
	ctl.setDisks([]string{"/d/root", "/d/extra"})
	ctl.syncFn = func(p string) error { synced = append(synced, p); return nil }
	if _, exit, err := ctl.Do(context.Background()); err != nil || !exit {
		t.Fatalf("exit=%v err=%v", exit, err)
	}
	if len(synced) != 2 || synced[0] != "/d/root" || synced[1] != "/d/extra" {
		t.Fatalf("synced = %v", synced)
	}
	if attempted(dir) {
		t.Fatal("snapshot marked unusable on success")
	}
}

func TestHibernateDo_FsyncFailureMarksUnusable(t *testing.T) {
	ctl, dir := hibCtlWithSnap(t, &fakeHibDrv{})
	ctl.setDisks([]string{"/d/root"})
	ctl.syncFn = func(string) error { return errors.New("EIO") }
	_, exit, err := ctl.Do(context.Background())
	if err == nil || !exit {
		t.Fatalf("exit=%v err=%v, want error + exit", exit, err)
	}
	if !attempted(dir) {
		t.Fatal(".attempted not written")
	}
}

func TestHibernateDo_StopRetryThenSucceeds(t *testing.T) {
	drv := &seqHibDrv{errs: []error{errors.New("boom")}}
	ctl, dir := hibCtlWithSnap(t, drv)
	ctl.killFn = func(domain.Sandbox) error { t.Fatal("kill must not run after a successful retry"); return nil }
	if _, _, err := ctl.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if drv.stops.Load() != 2 || attempted(dir) {
		t.Fatalf("stops=%d attempted=%v", drv.stops.Load(), attempted(dir))
	}
}

func TestHibernateDo_StopFailsTwiceKills(t *testing.T) {
	drv := &seqHibDrv{errs: []error{errors.New("a"), errors.New("b")}}
	ctl, dir := hibCtlWithSnap(t, drv)
	killed := 0
	ctl.killFn = func(domain.Sandbox) error { killed++; return nil }
	if _, exit, err := ctl.Do(context.Background()); err != nil || !exit {
		t.Fatalf("exit=%v err=%v", exit, err)
	}
	if drv.stops.Load() != 2 || killed != 1 || attempted(dir) {
		t.Fatalf("stops=%d killed=%d attempted=%v", drv.stops.Load(), killed, attempted(dir))
	}
}

func TestHibernateDo_KillFailsMarksUnusable(t *testing.T) {
	drv := &seqHibDrv{errs: []error{errors.New("a"), errors.New("b")}}
	ctl, dir := hibCtlWithSnap(t, drv)
	ctl.killFn = func(domain.Sandbox) error { return errors.New("no perms") }
	_, exit, err := ctl.Do(context.Background())
	if err == nil || !exit {
		t.Fatalf("exit=%v err=%v", exit, err)
	}
	if !attempted(dir) {
		t.Fatal(".attempted not written")
	}
}

func fakeProc(t *testing.T, pid int, pgid int, start string, argv0 string) string {
	t.Helper()
	root := t.TempDir()
	d := filepath.Join(root, "42")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := "42 (nexus) S 1 " + strconv.Itoa(pgid) + " 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 " + start + " 0 0"
	_ = os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644)
	_ = os.WriteFile(filepath.Join(d, "cmdline"), []byte(argv0+"\x00arg\x00"), 0o644)
	return root
}

func TestKillVMMGroup_VerifiesIdentity(t *testing.T) {
	sb := domain.Sandbox{NetnsChildPID: 42, NetnsChildPGID: 42, NetnsChildStartTime: 777}
	var sig []int
	kill := func(pid int, _ syscall.Signal) error { sig = append(sig, pid); return nil }

	if err := killVMMGroup(sb, fakeProc(t, 42, 42, "777", "/usr/bin/nexus"), kill); err != nil || len(sig) != 1 || sig[0] != -42 {
		t.Fatalf("good: err=%v sig=%v", err, sig)
	}
	sig = nil
	for name, root := range map[string]string{
		"starttime": fakeProc(t, 42, 42, "778", "/usr/bin/nexus"),
		"pgid":      fakeProc(t, 42, 9, "777", "/usr/bin/nexus"),
		"cmdline":   fakeProc(t, 42, 42, "777", "/bin/sleep"),
	} {
		if err := killVMMGroup(sb, root, kill); err == nil {
			t.Fatalf("%s mismatch: want refusal", name)
		}
	}
	if len(sig) != 0 {
		t.Fatalf("signalled despite mismatch: %v", sig)
	}
	if err := killVMMGroup(sb, t.TempDir(), kill); err != nil || len(sig) != 0 {
		t.Fatalf("vanished pid: err=%v sig=%v", err, sig)
	}
	if err := killVMMGroup(domain.Sandbox{}, t.TempDir(), kill); err == nil {
		t.Fatal("no pid recorded: want error")
	}
}
