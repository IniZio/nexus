package cred

import (
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestBrokerPlaceholder_ReturnsPlaceholderNeverRealToken is the guard for the
// zero-credential-in-guest invariant. Broker.Placeholder exists so callers can
// build a guest environment; if it ever returned the real token, every caller
// would silently plant a live credential inside a VM.
func TestBrokerPlaceholder_ReturnsPlaceholderNeverRealToken(t *testing.T) {
	b := NewBroker()
	id := domain.NewSandboxID()
	const host = "api.anthropic.com"
	const realToken = "sk-ant-REAL-TOKEN-MUST-NEVER-LEAVE-THE-HOST"

	rec, err := b.RegisterPlaceholder(id, host, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	got, ok := b.Placeholder(id, host)
	if !ok {
		t.Fatal("Placeholder: want ok=true for a registered scope, got false")
	}
	if got != rec.Placeholder {
		t.Errorf("Placeholder = %q, want the registered placeholder %q", got, rec.Placeholder)
	}
	if got == realToken || strings.Contains(got, "REAL-TOKEN") {
		t.Fatalf("Placeholder LEAKED THE REAL TOKEN: %q", got)
	}
	// The reverse direction still works host-side — this is what the MITM proxy
	// uses — proving the real token is reachable by the proxy but not by the
	// guest-env builder.
	if resolved, rok := b.Resolve(got); !rok || resolved != realToken {
		t.Errorf("Resolve(placeholder) = %q, %v; want the real token back host-side", resolved, rok)
	}
}

func TestBrokerPlaceholder_UnknownScope(t *testing.T) {
	b := NewBroker()
	id := domain.NewSandboxID()

	if _, ok := b.Placeholder(id, "api.anthropic.com"); ok {
		t.Error("Placeholder: want ok=false for an unregistered scope, got true")
	}

	if _, err := b.RegisterPlaceholder(id, "api.anthropic.com", "tok"); err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}
	// Right sandbox, wrong host must not match.
	if _, ok := b.Placeholder(id, "example.com"); ok {
		t.Error("Placeholder: want ok=false for a different host, got true")
	}
	// Right host, wrong sandbox must not match.
	if _, ok := b.Placeholder(domain.NewSandboxID(), "api.anthropic.com"); ok {
		t.Error("Placeholder: want ok=false for a different sandbox, got true")
	}
}
