package supervisor

// handoff_payload_builder_test.go proves that the REAL payload builder both
// RunDetached and RunAdopt call — buildHandoffPayload — produces a payload
// that actually passes handoff.Payload.Validate(), against a Service whose
// perimeter has real CA material.
//
// Gate finding (motive nexus3-host-supervisor-hotswap, ticket 08, round 3):
// handoff.Payload.Validate() has always unconditionally required non-empty
// CA.CertPEM/CA.KeyPEM, but no payloadBuilder populated Payload.CA until the
// ticket-08 CA-transfer fix — and every existing test (ipc_detach_test.go,
// handoff_validate_test.go, transport_test.go) hand-rolls its own payload
// instead of calling the real builder, so a motive-long, always-refusing
// handoff passed every unit suite. Reverting the CA fix at the real builder
// (not at the mitm layer, where seed_ca_test.go already covers a different
// slice of the mechanism) must turn this test RED.

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// netHookStub wraps fake.FakeDriver and satisfies driver.NetworkHook by
// returning one end of a net.Pipe as the guest fd — the same pattern
// internal/core/service's own t6NetHook/fakeNetHookDriver test helpers use,
// duplicated here (unexported, package-local) rather than imported, since
// those helpers live in _test.go files of another package.
type netHookStub struct {
	*fake.FakeDriver
	conn net.Conn
}

func (h *netHookStub) GuestNetworkFD(_ context.Context, _ domain.SandboxID) (io.ReadWriteCloser, error) {
	c := h.conn
	h.conn = nil
	return c, nil
}

// TestBuildHandoffPayload_RealBuilder_PopulatesCAAndPassesValidate is the
// regression-site proof: call the ACTUAL function RunDetached and RunAdopt
// invoke, not a hand-rolled stand-in, and assert its output would survive
// performHandoff's payload.Validate() call.
func TestBuildHandoffPayload_RealBuilder_PopulatesCAAndPassesValidate(t *testing.T) {
	guestConn, hostConn := net.Pipe()
	t.Cleanup(func() { hostConn.Close() })

	drv := &netHookStub{FakeDriver: fake.New(), conn: guestConn}
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker())

	sb, err := svc.Create(context.Background(), "proj", "handoff-builder", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.Update(context.Background(), sb.ID, func(rec *domain.Sandbox) error {
		rec.State = domain.Running
		return nil
	}); err != nil {
		t.Fatalf("st.Update: %v", err)
	}
	sb.State = domain.Running

	if err := svc.StartPerimeterOnly(context.Background(), sb, nil); err != nil {
		t.Fatalf("StartPerimeterOnly: %v", err)
	}
	sup := svc.GetPerimeterSupervisor(sb.ID)
	if sup == nil {
		t.Fatal("GetPerimeterSupervisor returned nil — no perimeter was constructed; test fixture is wrong, not the code under test")
	}

	payload, fdFile, err := buildHandoffPayload(sup, sb.ID.String(), 1, 512)
	if err != nil {
		t.Fatalf("buildHandoffPayload: %v", err)
	}
	if fdFile != nil {
		defer fdFile.Close()
	}

	if reason := payload.Validate(); reason != "" {
		t.Fatalf("the REAL payload builder produced a payload that fails Validate(): %q — "+
			"this is the exact defect that made every handoff refuse for the whole motive", reason)
	}
	if len(payload.CA.CertPEM) == 0 || len(payload.CA.KeyPEM) == 0 {
		t.Fatal("buildHandoffPayload did not populate CA material from the real PerimeterSupervisor")
	}
}
