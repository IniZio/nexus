package cloudhypervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

type chFake struct {
	mu       sync.Mutex
	calls    []string
	state    string
	snapFail bool
}

func (f *chFake) called(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func hibernateFixture(t *testing.T, f *chFake) (*CHDriver, domain.SandboxID, string) {
	t.Helper()
	f.state = "Running"
	oldSync := hibernateSyncDisks
	hibernateSyncDisks = func([]string) error { return nil }
	t.Cleanup(func() { hibernateSyncDisks = oldSync })
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vmm.ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"52.0"}`))
	})
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Write([]byte(`{"state":"` + f.state + `","config":{"memory":{"size":1024},"balloon":{"size":7},"disks":[{"path":"/d/root.ext4"}]}}`))
	})
	mux.HandleFunc("/api/v1/vm.pause", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, "pause")
		f.state = "Paused"
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/vm.resume", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, "resume")
		f.state = "Running"
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/vm.snapshot", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, "snapshot")
		fail := f.snapFail
		f.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var req vmSnapshotRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		d := strings.TrimPrefix(req.DestinationURL, "file://")
		_ = os.WriteFile(filepath.Join(d, "memory-ranges"), make([]byte, 4096), 0o600)
		_ = os.WriteFile(filepath.Join(d, "config.json"), []byte("{}"), 0o600)
		w.WriteHeader(http.StatusNoContent)
	})
	sock := unixTestServer(t, mux)
	id := domain.NewSandboxID()
	d := &CHDriver{cfg: Config{SocketDir: filepath.Dir(sock)}}
	if err := os.Rename(sock, d.socketPath(id)); err != nil {
		t.Fatal(err)
	}
	return d, id, t.TempDir()
}

func TestHibernateTo_Success(t *testing.T) {
	f := &chFake{}
	d, id, dir := hibernateFixture(t, f)
	res, err := d.HibernateTo(context.Background(), id, dir)
	if err != nil {
		t.Fatal(err)
	}
	if f.called("resume") != 0 || f.state != "Paused" {
		t.Fatalf("VM must stay paused: calls=%v state=%s", f.calls, f.state)
	}
	if !ValidHibernateDir(dir) {
		t.Fatal("dir not valid")
	}
	if res.SnapshotBytes < 4096 || res.SnapshotBytesOnDisk <= 0 || res.TotalMs < 0 {
		t.Fatalf("bad result %+v", res)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "snap" {
		t.Fatalf("unexpected entries %v", ents)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "snap", "manifest.json"))
	for _, want := range []string{"52.0", "/d/root.ext4", "memory-ranges"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("manifest missing %q: %s", want, b)
		}
	}
}

func TestHibernateTo_AlreadyPausedSkipsPause(t *testing.T) {
	f := &chFake{}
	d, id, dir := hibernateFixture(t, f)
	f.state = "Paused"
	if _, err := d.HibernateTo(context.Background(), id, dir); err != nil {
		t.Fatal(err)
	}
	if f.called("pause") != 0 {
		t.Fatal("pause called on paused VM")
	}
}

func TestHibernateTo_SnapshotErrorCleansAndResumes(t *testing.T) {
	f := &chFake{snapFail: true}
	d, id, dir := hibernateFixture(t, f)
	if _, err := d.HibernateTo(context.Background(), id, dir); err == nil {
		t.Fatal("want error")
	}
	if f.called("resume") != 1 {
		t.Fatalf("resume not called: %v", f.calls)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("leftovers %v", ents)
	}
}

func TestHibernateTo_RenameErrorCleansAndResumes(t *testing.T) {
	f := &chFake{}
	d, id, dir := hibernateFixture(t, f)
	old := hibernateRename
	hibernateRename = func(a, b string) error { return os.ErrPermission }
	t.Cleanup(func() { hibernateRename = old })
	if _, err := d.HibernateTo(context.Background(), id, dir); err == nil {
		t.Fatal("want error")
	}
	if f.called("resume") != 1 {
		t.Fatalf("resume not called: %v", f.calls)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("leftovers %v", ents)
	}
}

func TestHibernateTo_FsyncErrorCleansAndResumes(t *testing.T) {
	f := &chFake{}
	d, id, dir := hibernateFixture(t, f)
	old := hibernateFsync
	hibernateFsync = func(string) error { return os.ErrInvalid }
	t.Cleanup(func() { hibernateFsync = old })
	if _, err := d.HibernateTo(context.Background(), id, dir); err == nil {
		t.Fatal("want error")
	}
	if f.called("resume") != 1 {
		t.Fatalf("resume not called: %v", f.calls)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("leftovers %v", ents)
	}
}

func TestHibernateTo_DiskFsyncErrorNoCommit(t *testing.T) {
	f := &chFake{}
	d, id, dir := hibernateFixture(t, f)
	var got []string
	hibernateSyncDisks = func(p []string) error { got = p; return os.ErrInvalid }
	if _, err := d.HibernateTo(context.Background(), id, dir); err == nil {
		t.Fatal("want error")
	}
	if len(got) != 1 || got[0] != "/d/root.ext4" {
		t.Fatalf("disks synced = %v", got)
	}
	if f.called("resume") != 1 {
		t.Fatalf("resume not called: %v", f.calls)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("leftovers (COMMITTED/tmp) %v", ents)
	}
}

func TestSyncDiskImages(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.img")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syncDiskImages([]string{p}); err != nil {
		t.Fatal(err)
	}
	if err := syncDiskImages([]string{p + ".missing"}); err == nil {
		t.Fatal("want error for missing disk")
	}
}

func TestHibernateTo_ReplacesPreviousSnap(t *testing.T) {
	f := &chFake{}
	d, id, dir := hibernateFixture(t, f)
	if _, err := d.HibernateTo(context.Background(), id, dir); err != nil {
		t.Fatal(err)
	}
	f.state = "Paused"
	if _, err := d.HibernateTo(context.Background(), id, dir); err != nil {
		t.Fatal(err)
	}
	if !ValidHibernateDir(dir) {
		t.Fatal("invalid after replace")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("leftovers %v", ents)
	}
}

func TestValidHibernateDir_CrashBeforeMarker(t *testing.T) {
	f := &chFake{}
	d, id, dir := hibernateFixture(t, f)
	if _, err := d.HibernateTo(context.Background(), id, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "snap", hibernateCommitted)); err != nil {
		t.Fatal(err)
	}
	if ValidHibernateDir(dir) {
		t.Fatal("dir without COMMITTED must be invalid")
	}
}

func TestValidHibernateDir_SizeMismatch(t *testing.T) {
	f := &chFake{}
	d, id, dir := hibernateFixture(t, f)
	if _, err := d.HibernateTo(context.Background(), id, dir); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "snap", "config.json"), []byte("{} extra"), 0o600)
	if ValidHibernateDir(dir) {
		t.Fatal("truncated/changed file must be invalid")
	}
	if ValidHibernateDir(t.TempDir()) {
		t.Fatal("empty dir valid")
	}
}

func TestRunningCHVersion_MultiLineOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cloud-hypervisor")
	script := "#!/bin/sh\necho 'cloud-hypervisor v53.0'\necho 'Migration Protocol Versions: 0'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := runningCHVersion(bin)
	if err != nil {
		t.Fatal(err)
	}
	if got != "v53.0" {
		t.Fatalf("runningCHVersion = %q, want v53.0", got)
	}
}

func TestSameCHVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v53.0", "53.0.0", true}, // --version vs vmm.ping spellings
		{"v52.0", "52.0", true},
		{"v53.0", "53.1.0", false},
		{"v53.0", "v52.0", false},
		{"", "", false},
	} {
		if got := sameCHVersion(c.a, c.b); got != c.want {
			t.Errorf("sameCHVersion(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}
