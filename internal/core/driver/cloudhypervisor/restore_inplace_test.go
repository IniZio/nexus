package cloudhypervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

type restoreFake struct {
	mu       sync.Mutex
	calls    []string
	reqs     []vmRestoreRequest
	restoreE bool
	started  int
	agentErr error
}

func (r *restoreFake) has(n string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c == n {
			return true
		}
	}
	return false
}

// restoreEnv hibernates a fake VM, kills its socket, and stubs the netns
// start with a fake CH that records vm.restore/vm.resume like the real child.
func restoreEnv(t *testing.T) (*CHDriver, domain.SandboxID, string, *restoreFake) {
	t.Helper()
	d, id, dir := hibernateFixture(t, &chFake{})
	d.cfg.DiskImagePath = "/d/root.ext4"
	d.cfg.BinaryPath = "ch"
	d.nets = map[domain.SandboxID]*netState{}
	if _, err := d.HibernateTo(context.Background(), id, dir); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(d.socketPath(id))

	rf := &restoreFake{}
	oldStart, oldVer, oldProbe, oldReady := startRestoreProc, chBinaryVersion, probeOnDemand, waitAgentReady
	t.Cleanup(func() {
		startRestoreProc, chBinaryVersion, probeOnDemand, waitAgentReady = oldStart, oldVer, oldProbe, oldReady
	})
	chBinaryVersion = func(string) (string, error) { return "52.0", nil }
	probeOnDemand = func() error { return nil }
	waitAgentReady = func(*CHDriver, context.Context, domain.SandboxID) error {
		rf.mu.Lock()
		defer rf.mu.Unlock()
		rf.calls = append(rf.calls, "agent")
		return rf.agentErr
	}
	startRestoreProc = func(ctx context.Context, cfg Config, sid domain.SandboxID, sock, url string) (*NetnsRuntime, error) {
		rf.mu.Lock()
		rf.started++
		rf.mu.Unlock()
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/vmm.ping", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{}`)) })
		mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"state":"Running"}`)) })
		mux.HandleFunc("/api/v1/vm.restore", func(w http.ResponseWriter, r *http.Request) {
			var q vmRestoreRequest
			_ = json.NewDecoder(r.Body).Decode(&q)
			rf.mu.Lock()
			rf.calls = append(rf.calls, "restore")
			rf.reqs = append(rf.reqs, q)
			fail := rf.restoreE
			rf.mu.Unlock()
			if fail {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/api/v1/vm.resume", func(w http.ResponseWriter, _ *http.Request) {
			rf.mu.Lock()
			rf.calls = append(rf.calls, "resume")
			rf.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		})
		s := unixTestServer(t, mux)
		if err := os.Rename(s, sock); err != nil {
			return nil, err
		}
		if err := restoreAndResume(ctx, newClient(sock), url, cfg.restoreMode); err != nil {
			return nil, err
		}
		return &NetnsRuntime{}, nil
	}
	return d, id, dir, rf
}

func marker(dir, n string) bool {
	_, err := os.Stat(filepath.Join(dir, "snap", n))
	return err == nil
}

func TestRestoreInPlace_Success(t *testing.T) {
	d, id, dir, rf := restoreEnv(t)
	res, err := d.RestoreInPlace(context.Background(), id, dir, driver.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != driver.RestoreModeCopy || !rf.has("restore") || !rf.has("resume") || !rf.has("agent") {
		t.Fatalf("res=%+v calls=%v", res, rf.calls)
	}
	if rf.reqs[0].MemoryRestoreMode != "" || rf.reqs[0].SourceURL != "file://"+filepath.Join(dir, "snap") {
		t.Fatalf("req=%+v", rf.reqs[0])
	}
	if marker(dir, ".attempted") || !marker(dir, ".restored") {
		t.Fatal("want .restored and no .attempted")
	}
	if HibernateReusable(dir) {
		t.Fatal("consumed snapshot must not be reusable")
	}
	if !ValidHibernateDir(dir) {
		t.Fatal("snapshot data stays valid")
	}
	if _, err := d.RestoreInPlace(context.Background(), id, dir, driver.RestoreOptions{}); !errors.Is(err, driver.ErrHibernateInvalid) {
		t.Fatalf("second restore: %v", err)
	}
}

func TestRestoreInPlace_MarkerFsyncErrorAbortsBeforeStart(t *testing.T) {
	d, id, dir, rf := restoreEnv(t)
	old := restoreMarkerSync
	restoreMarkerSync = func(string, string) error { return os.ErrInvalid }
	t.Cleanup(func() { restoreMarkerSync = old })
	_, err := d.RestoreInPlace(context.Background(), id, dir, driver.RestoreOptions{})
	if !errors.Is(err, driver.ErrHibernateInvalid) {
		t.Fatalf("want ErrHibernateInvalid, got %v", err)
	}
	if rf.started != 0 {
		t.Fatalf("VM started despite marker fsync failure")
	}
}

func TestRestoreInPlace_OnDemandMode(t *testing.T) {
	d, id, dir, rf := restoreEnv(t)
	res, err := d.RestoreInPlace(context.Background(), id, dir, driver.RestoreOptions{Mode: driver.RestoreModeOnDemand})
	if err != nil {
		t.Fatal(err)
	}
	q := rf.reqs[0]
	if res.Mode != driver.RestoreModeOnDemand || q.MemoryRestoreMode != "ondemand" || q.Prefault {
		t.Fatalf("res=%+v req=%+v", res, q)
	}
}

func TestRestoreInPlace_OnDemandUnavailable(t *testing.T) {
	d, id, dir, rf := restoreEnv(t)
	probeOnDemand = func() error { return errors.New("no uffd") }
	_, err := d.RestoreInPlace(context.Background(), id, dir, driver.RestoreOptions{Mode: driver.RestoreModeOnDemand})
	if !errors.Is(err, driver.ErrHibernateIncompatible) || rf.started != 0 || marker(dir, ".attempted") {
		t.Fatalf("err=%v started=%d", err, rf.started)
	}
}

func TestRestoreInPlace_RejectsWithoutStarting(t *testing.T) {
	cases := map[string]struct {
		mut  func(*CHDriver, string)
		want error
	}{
		"no COMMITTED": {func(_ *CHDriver, dir string) { _ = os.Remove(filepath.Join(dir, "snap", "COMMITTED")) }, driver.ErrHibernateInvalid},
		"size mismatch": {func(_ *CHDriver, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "snap", "memory-ranges"), []byte("x"), 0o600)
		}, driver.ErrHibernateInvalid},
		"already attempted": {func(_ *CHDriver, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "snap", ".attempted"), nil, 0o600)
		}, driver.ErrHibernateInvalid},
		"version": {func(*CHDriver, string) { chBinaryVersion = func(string) (string, error) { return "53.0", nil } }, driver.ErrHibernateIncompatible},
		"disks":   {func(d *CHDriver, _ string) { d.cfg.DiskImagePath = "/other/root.ext4" }, driver.ErrHibernateIncompatible},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d, id, dir, rf := restoreEnv(t)
			tc.mut(d, dir)
			_, err := d.RestoreInPlace(context.Background(), id, dir, driver.RestoreOptions{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
			if rf.started != 0 {
				t.Fatal("must not start anything")
			}
			if name != "already attempted" && marker(dir, ".attempted") {
				t.Fatal("must not write .attempted on validation failure")
			}
		})
	}
}

func TestRestoreInPlace_RestoreErrorTearsDown(t *testing.T) {
	d, id, dir, rf := restoreEnv(t)
	rf.restoreE = true
	_, err := d.RestoreInPlace(context.Background(), id, dir, driver.RestoreOptions{})
	if err == nil {
		t.Fatal("want error")
	}
	d.mu.Lock()
	_, left := d.nets[id]
	d.mu.Unlock()
	if left {
		t.Fatal("net state not torn down")
	}
	if !marker(dir, ".attempted") || marker(dir, ".restored") || HibernateReusable(dir) {
		t.Fatal("failed restore must leave .attempted and block reuse")
	}
}

func TestRestoreInPlace_AgentNotReadyTearsDown(t *testing.T) {
	d, id, dir, rf := restoreEnv(t)
	rf.agentErr = errors.New("agent down")
	if _, err := d.RestoreInPlace(context.Background(), id, dir, driver.RestoreOptions{}); err == nil {
		t.Fatal("want error")
	}
	d.mu.Lock()
	_, left := d.nets[id]
	d.mu.Unlock()
	if left || !marker(dir, ".attempted") {
		t.Fatal("teardown/marker wrong")
	}
}
