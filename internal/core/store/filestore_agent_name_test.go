package store_test

// TestAgentName_FilestoreRoundTrip is the regression guard ensuring that
// Sandbox.AgentName survives a filestore Create/Get round-trip through the
// real toRecord / toDomain mapping path (TBD-PD-32).
//
// The mapping in filestore.go is written by hand, field by field: a new domain
// field that is not added to BOTH toRecord and toDomain is silently dropped
// with no compile error and no other failing test. That is exactly the class of
// bug this test exists to catch.
//
// Mutation proof:
//
//  1. Remove `AgentName: sb.AgentName,` from toRecord in filestore.go
//     → this test goes RED.
//
//  2. Remove `AgentName: r.AgentName,` from toDomain in filestore.go
//     → this test goes RED.

import (
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/store"
)

func TestAgentName_FilestoreRoundTrip(t *testing.T) {
	ctx := context.Background()

	// The empty case is asserted alongside the populated one because "" is the
	// value that means "no agent, and therefore no credential seed". A mapping
	// bug that hardcoded a default would pass a populated-only test.
	cases := []struct {
		name string
		want string
	}{
		{name: "agent attached", want: cred.ClaudeCodeProfileName},
		{name: "no agent", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			st, err := store.NewFileStore(root)
			if err != nil {
				t.Fatalf("NewFileStore: %v", err)
			}

			sb := makeSandbox("agent-name", "agent-proj")
			sb.AgentName = tc.want
			if err := st.Create(ctx, sb); err != nil {
				t.Fatalf("Create: %v", err)
			}

			// Re-open against the same directory so the assertion runs against
			// bytes that actually went to disk, not an in-memory copy.
			st2, err := store.NewFileStore(root)
			if err != nil {
				t.Fatalf("NewFileStore (reopen): %v", err)
			}
			got, err := st2.Get(ctx, sb.ID)
			if err != nil {
				t.Fatalf("Get after reopen: %v", err)
			}
			if got.AgentName != tc.want {
				t.Errorf("AgentName after round-trip = %q, want %q", got.AgentName, tc.want)
			}
		})
	}
}
