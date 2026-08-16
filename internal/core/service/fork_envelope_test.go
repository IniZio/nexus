package service_test

// fork_envelope_test.go pins the security properties that Service.Fork must
// preserve when it mints child sandboxes from a parent snapshot.
//
// Service.Fork copies parent.Envelope (AllowedHosts, SSHPublicKey, ImageDigest)
// and maps.Clone(parent.Labels) onto each child, then calls startSupervisor
// per child when a NetworkHook and broker are attached. An empty child
// AllowedHosts is the AllowAll sentinel at startSupervisor; copying the
// parent's curated list is what keeps forked children from escalating to
// wide-open unaudited egress (D-PD-22: agent children stay as dark as the
// parent).
//
// These tests express the properties as they MUST hold after the FORK-ENV
// fix. A regression that drops Envelope or Labels, or that skips the
// per-child supervisor, fails with a message that names the property.

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── forkNetHookDriver ─────────────────────────────────────────────────────────

// forkNetHookDriver wraps FakeDriver with driver.NetworkHook so that
// service.startSupervisor (service.go:655) fires when a broker is attached.
//
// Unlike fakeNetHookDriver in supervisor_test.go (single-use fd), this driver
// mints a FRESH net.Pipe per sandbox ID — mirroring the real CH driver, which
// registers a per-child netState with its own perimConn in
// cloudhypervisor/fork.go:346. That makes the driver capable of serving a
// perimeter fd for a forked child, so a failure to obtain one is attributable
// to the service layer, not to test scaffolding.
type forkNetHookDriver struct {
	*fake.FakeDriver

	mu     sync.Mutex
	served map[domain.SandboxID]int // per-sandbox GuestNetworkFD call count
	hosts  []net.Conn               // host ends, closed at test teardown
}

func newForkNetHookDriver() *forkNetHookDriver {
	return &forkNetHookDriver{
		FakeDriver: fake.New(),
		served:     make(map[domain.SandboxID]int),
	}
}

func (f *forkNetHookDriver) GuestNetworkFD(_ context.Context, id domain.SandboxID) (io.ReadWriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.served[id]++
	guestConn, hostConn := net.Pipe()
	f.hosts = append(f.hosts, hostConn)
	return guestConn, nil
}

// servedCount returns how many times a perimeter fd was claimed for id.
func (f *forkNetHookDriver) servedCount(id domain.SandboxID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.served[id]
}

func (f *forkNetHookDriver) closeHosts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.hosts {
		_ = c.Close()
	}
	f.hosts = nil
}

var _ driver.NetworkHook = (*forkNetHookDriver)(nil)

// ── harness ───────────────────────────────────────────────────────────────────

// curatedParentHosts is a deliberately non-resolvable allowlist: the test must
// not depend on DNS. A non-empty list is all that matters — it is what takes
// startSupervisor off the AllowAll branch (service.go:676) and instantiates the
// MITM proxy (service.go:685-698).
var curatedParentHosts = []string{"allowed.example.invalid"}

const curatedParentSSHKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFORKTESTKEY parent@nexus3"

// newForkEnvelopeSvc builds a service wired for perimeter assembly and seeds a
// parent sandbox record carrying a curated Envelope and a label set.
//
// The parent record is written directly through the store because
// Service.Create hardcodes an empty Envelope (service.go:197); the curated
// Envelope is populated only by CreateAndBoot (create.go:460), which needs a
// real image and a real VMM. Seeding the record reproduces exactly the state
// CreateAndBoot leaves behind, with no substrate dependency.
func newForkEnvelopeSvc(t *testing.T, labels map[string]string) (*service.Service, *forkNetHookDriver, domain.Sandbox) {
	t.Helper()

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := newForkNetHookDriver()
	t.Cleanup(drv.closeHosts)

	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker())

	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "fork-envelope-parent",
		Project: "proj",
		Labels:  labels,
		State:   domain.Created,
		Envelope: domain.Envelope{
			AllowedHosts: curatedParentHosts,
			SSHPublicKey: curatedParentSSHKey,
		},
	}
	if err := st.Create(context.Background(), parent); err != nil {
		t.Fatalf("seed parent record: %v", err)
	}

	started, err := svc.Start(context.Background(), parent.ID.String())
	if err != nil {
		t.Fatalf("Start(parent): %v", err)
	}
	t.Cleanup(func() { _, _ = svc.Stop(context.Background(), started.ID.String()) })

	return svc, drv, started
}

// ── (a)/(b) crux: does the child get a policed perimeter at all? ─────────────

// TestFork_ChildPerimeterInheritsParentEgressPolicy pins the property:
//
//	A child minted by Fork from a parent with a curated egress allowlist must
//	be governed by an egress perimeter at least as restrictive as its parent's.
//
// Two ways the property can break, and the assertions distinguish them:
//
//	(a) The child's perimeter starts but takes the AllowAll branch
//	    (startSupervisor) because its Envelope.AllowedHosts is empty —
//	    wide-open, un-intercepted, un-audited egress inherited by escalation.
//	(b) No perimeter is assembled for the child at all — Fork used to skip
//	    startSupervisor (sole caller was Start). Fork now calls it per child
//	    when a NetworkHook and broker are attached.
//
// The GuestNetworkFD claim count discriminates (a) from (b): the AllowAll
// branch still claims the fd, so servedCount(child) == 0 proves (b).
func TestFork_ChildPerimeterInheritsParentEgressPolicy(t *testing.T) {
	svc, drv, parent := newForkEnvelopeSvc(t, nil)
	c := context.Background()

	// Baseline: the parent IS policed. A non-empty allowlist instantiates the
	// MITM proxy, so a CA certificate exists for the parent.
	if drv.servedCount(parent.ID) != 1 {
		t.Fatalf("harness: parent perimeter fd claims = %d, want 1", drv.servedCount(parent.ID))
	}
	if svc.GetPerimeterCACert(parent.ID) == nil {
		t.Fatal("harness: parent has no MITM CA; the curated-allowlist branch did not run")
	}

	children, err := svc.Fork(c, parent.ID.String(), 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("Fork returned %d children, want 1", len(children))
	}
	child := children[0]

	// Property 1 — the egress envelope must be inherited.
	if len(child.Envelope.AllowedHosts) == 0 {
		t.Errorf("PROPERTY VIOLATED (egress envelope inheritance): child %s has an EMPTY "+
			"Envelope.AllowedHosts while parent %s is restricted to %v. An empty allowlist "+
			"is the AllowAll sentinel at service.go:676 — should this child's perimeter ever "+
			"start, egress is wide open, un-intercepted (no MITM) and un-audited (onAudit nil).",
			child.ID, parent.ID, parent.Envelope.AllowedHosts)
	} else if len(child.Envelope.AllowedHosts) != len(parent.Envelope.AllowedHosts) {
		t.Errorf("PROPERTY VIOLATED (egress envelope inheritance): child AllowedHosts %v != parent %v",
			child.Envelope.AllowedHosts, parent.Envelope.AllowedHosts)
	}

	// Property 2 — a running child must actually be behind a perimeter.
	// Fork persists the child in Running state, so no subsequent Start is
	// even legal (TriggerStart is defined only from Created and Stopped).
	// Fork must call startSupervisor itself.
	claims := drv.servedCount(child.ID)
	if claims == 0 {
		t.Errorf("PROPERTY VIOLATED (perimeter coverage, case (b)): no perimeter fd was ever "+
			"claimed for forked child %s. Service.Fork must call startSupervisor for each "+
			"Running child. The child runs with a live TAP "+
			"(driver/cloudhypervisor/fork.go registers its netState) and no policy attached to it.", child.ID)
	}
	if svc.GetPerimeterCACert(child.ID) == nil {
		t.Errorf("PROPERTY VIOLATED (MITM coverage): forked child %s has no MITM CA while its "+
			"parent %s does. Traffic from the child is not intercepted.", child.ID, parent.ID)
	}
}

// TestFork_ChildInheritsSSHPublicKey pins the record-level property:
//
//	A forked child must carry the parent's Envelope.SSHPublicKey so that a
//	later Stop→Start re-injects authorized_keys (service.go:436-437).
func TestFork_ChildInheritsSSHPublicKey(t *testing.T) {
	svc, _, parent := newForkEnvelopeSvc(t, nil)

	children, err := svc.Fork(context.Background(), parent.ID.String(), 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	child := children[0]

	if child.Envelope.SSHPublicKey != parent.Envelope.SSHPublicKey {
		t.Errorf("PROPERTY VIOLATED (SSH key inheritance): child %s Envelope.SSHPublicKey = %q, "+
			"want parent's %q. The re-injection branch at service.go:436 is dead for this child.",
			child.ID, child.Envelope.SSHPublicKey, parent.Envelope.SSHPublicKey)
	}
}

// ── labels consequence ────────────────────────────────────────────────────────

// TestFork_ChildrenRemainInParentLabelGroup pins the property:
//
//	Children forked from a labelled sandbox stay inside the parent's label
//	group, so `harvest <motive-id>` (service/harvest.go:54 → GetByMotive) and
//	`sandbox list --label` (cli/cmd_sandbox.go → GetByLabels) select them.
func TestFork_ChildrenRemainInParentLabelGroup(t *testing.T) {
	const motiveID = "m-fork-group"
	labels := map[string]string{"motive": motiveID, "tier": "dev"}

	svc, _, parent := newForkEnvelopeSvc(t, labels)
	c := context.Background()

	children, err := svc.Fork(c, parent.ID.String(), 2)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	// harvest selection path.
	byMotive, err := svc.GetByMotive(c, motiveID)
	if err != nil {
		t.Fatalf("GetByMotive: %v", err)
	}
	if len(byMotive) != 1+len(children) {
		t.Errorf("PROPERTY VIOLATED (label group membership): GetByMotive(%q) returned %d "+
			"sandbox(es), want %d (parent + %d forked children). Fork does not copy "+
			"parent.Labels onto the child record (service.go:939-949), so `harvest %s` "+
			"silently skips the children.",
			motiveID, len(byMotive), 1+len(children), len(children), motiveID)
	}

	// label selection path (AND-matched multi-key; backs `sandbox list --label`).
	byLabels, err := svc.GetByLabels(c, labels)
	if err != nil {
		t.Fatalf("GetByLabels: %v", err)
	}
	selected := make(map[domain.SandboxID]bool, len(byLabels))
	for _, sb := range byLabels {
		selected[sb.ID] = true
	}
	for _, ch := range children {
		if !selected[ch.ID] {
			t.Errorf("PROPERTY VIOLATED (label group membership): forked child %s is NOT "+
				"selected by GetByLabels(%v); child.Labels = %v",
				ch.ID, labels, ch.Labels)
		}
	}
}
