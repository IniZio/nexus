package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestHiddenCommand_NotInUsageBanner asserts that a Hidden command is absent
// from printUsage output. A mutation that removes Hidden:true would make the
// command appear in the banner, failing this test.
func TestHiddenCommand_NotInUsageBanner(t *testing.T) {
	const testVerb = "__test-hidden-verb"
	Register(Command{
		Name:    testVerb,
		Summary: "hidden test command",
		Hidden:  true,
		Run: func(_ context.Context, _ []string, _ *Output) error {
			return nil
		},
	})
	t.Cleanup(func() {
		registryMu.Lock()
		delete(commands, testVerb)
		registryMu.Unlock()
	})

	var buf bytes.Buffer
	printUsage(&buf)
	if bytes.Contains(buf.Bytes(), []byte(testVerb)) {
		t.Errorf("hidden command %q appeared in printUsage output:\n%s", testVerb, buf.String())
	}
}

// TestHiddenCommand_StillRunnable asserts that a Hidden command is still
// resolvable via Lookup and executes normally. A mutation that made Lookup
// skip hidden commands would return ok=false, failing this test.
func TestHiddenCommand_StillRunnable(t *testing.T) {
	const testVerb = "__test-hidden-runnable"
	var ran bool
	Register(Command{
		Name:    testVerb,
		Summary: "hidden runnable test command",
		Hidden:  true,
		Run: func(_ context.Context, _ []string, _ *Output) error {
			ran = true
			return nil
		},
	})
	t.Cleanup(func() {
		registryMu.Lock()
		delete(commands, testVerb)
		registryMu.Unlock()
	})

	cmd, ok := Lookup(testVerb)
	if !ok {
		t.Fatalf("Lookup(%q) returned ok=false; hidden commands must still be resolvable", testVerb)
	}
	out := NewOutput(new(bytes.Buffer), new(bytes.Buffer), false)
	if err := cmd.Run(context.Background(), nil, out); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !ran {
		t.Fatal("Run returned nil but the handler did not execute")
	}
}

// TestHerdrGroup_AgentRoutes verifies that `herdr agent` dispatches to the
// underlying space-agent handler rather than falling through to the unknown-
// subcommand error. A mutation that breaks the "agent" case in runHerdrGroup
// would produce "unknown subcommand" instead of the space-agent usage error,
// failing this test.
func TestHerdrGroup_AgentRoutes(t *testing.T) {
	out := NewOutput(new(bytes.Buffer), new(bytes.Buffer), false)
	// Call with zero non-flag args: space-agent will return a UsageError naming
	// "space-agent" in the message. If the dispatch case is broken, runHerdrGroup
	// returns its own "unknown subcommand" error instead.
	err := runHerdrGroup(context.Background(), []string{"agent"}, out)
	if err == nil {
		t.Fatal("herdr agent with no args: expected UsageError, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown subcommand") {
		t.Errorf("herdr agent fell through to unknown-subcommand error; dispatch case is broken: %v", err)
	}
	if !strings.Contains(msg, "space-agent") {
		t.Errorf("herdr agent did not reach the space-agent handler; got: %v", err)
	}
}
