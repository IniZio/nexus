package perimeter_test

// allowall_test.go proves that perimeter.Start with proxy=nil (the AllowAll
// path — sandboxes with empty AllowedHosts that carry no real credentials)
// produces a supervisor whose CACert is nil and MitmAddr is empty, confirming
// that no MITM certificate is injected and no credential swap can occur.
//
// No KVM, no network, no credentials. Pure unit test; runs on any developer
// machine and in CI without special privileges.

import (
	"context"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netstack"
)

// TestAllowAll_NilProxy_NoCACert_NoMitmAddr asserts that when perimeter.Start
// receives a nil proxy argument (the AllowAll/--file sandbox path):
//  1. CACert() returns nil  — no CA certificate is presented to the guest.
//  2. MitmAddr() returns "" — no MITM listener is bound.
//
// These two invariants guarantee that AllowAll sandboxes cannot intercept TLS
// or inject credentials, regardless of what the guest sends.
func TestAllowAll_NilProxy_NoCACert_NoMitmAddr(t *testing.T) {
	t.Parallel()

	// Build an AF_UNIX SOCK_DGRAM socketpair as a stand-in for the guest
	// network fd. The perimeter reads from one end; we hold the other so the
	// fd is valid without a real TAP device.
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	guestFD := os.NewFile(uintptr(fds[0]), "socketpair-guest")
	hostFD := os.NewFile(uintptr(fds[1]), "socketpair-host")
	t.Cleanup(func() { hostFD.Close() })

	// AllowList with no hosts — replicates the real AllowAll state (plus the
	// production code calls AllowAllFor, but for accessor assertions we only
	// need a valid non-nil *AllowList).
	al, err := netfilter.NewAllowList(nil, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}

	stack := netstack.New(al, nil)

	// proxy=nil is the production AllowAll path: no MITM interception.
	id := domain.SandboxID{}
	id[0] = 0xAA // deterministic test sandbox ID

	sup, err := perimeter.Start(context.Background(), id, guestFD, stack, nil, al)
	if err != nil {
		t.Fatalf("perimeter.Start: %v", err)
	}
	t.Cleanup(func() { sup.Close() })

	// Core contract: AllowAll supervisor must expose no CA certificate and no
	// MITM listener address.
	if cert := sup.CACert(); cert != nil {
		t.Errorf("CACert: got non-nil certificate (subject %q), want nil — AllowAll must not inject a CA", cert.Subject.CommonName)
	}
	if addr := sup.MitmAddr(); addr != "" {
		t.Errorf("MitmAddr: got %q, want empty — AllowAll must not bind a MITM listener", addr)
	}
}
