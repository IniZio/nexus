package service_test

// ca_persistence_test.go covers the CREATE side and the DELETE side of the
// persisted MITM CA's lifetime (D-HSH-18, ticket 13 / slice
// s15-ca-persistence). The load side lives in internal/supervisor.
//
// Both cases drive the real production call sites — Service.StartPerimeterOnly
// (which is startSupervisor, the same assembly Service.Start uses) and
// Service.Remove — with XDG_STATE_HOME redirecting store.DefaultRoot, which is
// the single resolver both ends use. No stand-in writes the file and no
// stand-in deletes it.

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/statedir"
	"github.com/IniZio/nexus3/internal/core/store"
)

// startPerimeterForCATest brings a sandbox to Running and wires its perimeter
// through the real StartPerimeterOnly, returning the sandbox and the store root
// that XDG_STATE_HOME now points at.
func startPerimeterForCATest(t *testing.T, name string) (*service.Service, domain.Sandbox, string) {
	t.Helper()
	ctx := context.Background()

	root := shortTempDir(t)
	t.Setenv("XDG_STATE_HOME", root)
	storeRoot := filepath.Join(root, "nexus3")

	guestConn, hostConn := net.Pipe()
	t.Cleanup(func() { hostConn.Close() })
	drv := &fakeNetHookDriver{FakeDriver: fake.New(), guestConn: guestConn}

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker())

	sb, err := svc.Create(ctx, "proj", name, service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A default sandbox has OpenEgress=false, so SandboxHasMITMProxy is true
	// and the perimeter really does mint a CA. Assert that rather than assume
	// it: without a MITM proxy this test would pass vacuously.
	if !service.SandboxHasMITMProxy(sb) {
		t.Fatal("fixture sandbox has no MITM proxy; there would be no CA to persist")
	}
	if err := st.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		rec.State = domain.Running
		return nil
	}); err != nil {
		t.Fatalf("st.Update: %v", err)
	}
	sb.State = domain.Running

	if err := svc.StartPerimeterOnly(ctx, sb, nil); err != nil {
		t.Fatalf("StartPerimeterOnly: %v", err)
	}
	return svc, sb, storeRoot
}

// TestStartPerimeter_PersistsTheMintedCA proves the perimeter writes its CA
// where the crash path will look for it, at the right modes, and that the
// persisted material is the SAME anchor the live proxy is signing with — the
// property that makes recovery transparent.
func TestStartPerimeter_PersistsTheMintedCA(t *testing.T) {
	svc, sb, storeRoot := startPerimeterForCATest(t, "ca-persist")

	dir := statedir.SupervisorDir(storeRoot, sb.ID)
	path := statedir.CAPath(dir)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the perimeter minted a CA but did not persist it to %s: %v — a crash-path recovery would break in-guest TLS", path, err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("persisted CA mode = %04o, want 0600 (unencrypted private key)", got)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("state dir mode = %04o, want 0700", got)
	}

	// Same anchor as the LIVE proxy: loading it back and seeding a proxy must
	// reproduce the CA the guest imported from this very supervisor.
	live := svc.GetPerimeterCACert(sb.ID)
	if live == nil {
		t.Fatal("no live perimeter CA to compare against")
	}
	certPEM, keyPEM, err := statedir.LoadCA(dir)
	if err != nil {
		t.Fatalf("LoadCA of the file the perimeter just wrote: %v", err)
	}
	seeded, err := mitm.New(mitm.Config{SandboxID: sb.ID, SeedCACertPEM: certPEM, SeedCAKeyPEM: keyPEM})
	if err != nil {
		t.Fatalf("mitm.New with the persisted CA: %v", err)
	}
	if seeded.CACert().SerialNumber.Cmp(live.SerialNumber) != 0 {
		t.Fatalf("persisted CA serial = %v, live perimeter CA serial = %v — the wrong CA was written",
			seeded.CACert().SerialNumber, live.SerialNumber)
	}
}

// TestRemove_LeavesNoCAMaterial is the lifetime bound D-HSH-18's risk section
// names as the failure to avoid: a CA private key outliving its sandbox.
//
// s14 made Service.Remove drop the whole state dir, so this ought to come free
// — which is exactly why it is asserted rather than assumed. It walks the tree
// looking for surviving PEM material rather than stat'ing one known path, so a
// future change that moves the CA elsewhere under the store root still fails
// here instead of silently leaking.
func TestRemove_LeavesNoCAMaterial(t *testing.T) {
	svc, sb, storeRoot := startPerimeterForCATest(t, "ca-teardown")

	dir := statedir.SupervisorDir(storeRoot, sb.ID)
	if _, err := os.Stat(statedir.CAPath(dir)); err != nil {
		t.Fatalf("precondition: a CA must exist before teardown: %v", err)
	}

	if err := svc.Remove(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("state dir %s survived Remove (stat err = %v)", dir, err)
	}
	var leaked []string
	_ = filepath.WalkDir(storeRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a walk error is not a leak; keep scanning
		}
		raw, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}
		if containsPrivateKeyPEM(raw) {
			leaked = append(leaked, p)
		}
		return nil
	})
	if len(leaked) != 0 {
		t.Fatalf("private-key PEM material survived Remove at %v — a CA key must never outlive its sandbox", leaked)
	}
}

// containsPrivateKeyPEM reports whether raw holds a PEM private-key header.
func containsPrivateKeyPEM(raw []byte) bool {
	for _, marker := range []string{
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
	} {
		if bytes.Contains(raw, []byte(marker)) {
			return true
		}
	}
	return false
}
