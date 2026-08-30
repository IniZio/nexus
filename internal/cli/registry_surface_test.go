package cli

import (
	"strings"
	"testing"
)

// TestEveryRegisteredCommandIsReachable is a surface-parity test: it fails when
// a command is defined and registered but no argument list can actually reach
// its Run function.
//
// This guards a defect class the repo has now hit twice — an implementation
// shipped with no reachable call site. `sandbox agent-upgrade` registered a
// two-token name while dispatch only ever looked up the first token, so the
// verb printed in the usage banner and returned "unknown subcommand" when run.
// The same shape sank --agent-egress earlier.
//
// The assertion deliberately goes through resolveCommandName, the real
// dispatcher path in Run, rather than re-implementing name splitting here: a
// checker that shares the broken mechanism proves nothing.
func TestEveryRegisteredCommandIsReachable(t *testing.T) {
	for _, cmd := range All() {
		t.Run(cmd.Name, func(t *testing.T) {
			argv := strings.Fields(cmd.Name)
			if len(argv) == 0 {
				t.Fatalf("command registered under an empty name")
			}

			got, rest := resolveCommandName(argv)
			if got != cmd.Name {
				t.Fatalf("dispatch resolves %q to command %q, so %q is unreachable: "+
					"register it under a name dispatch can address, or teach the parent verb's dispatcher about it",
					strings.Join(argv, " "), got, cmd.Name)
			}
			if len(rest) != 0 {
				t.Fatalf("dispatch left %v as arguments after resolving %q; the whole name must be consumed", rest, cmd.Name)
			}
			if resolved, ok := Lookup(got); !ok || resolved.Run == nil {
				t.Fatalf("command %q resolves but has no Run function", cmd.Name)
			}
		})
	}
}

// TestResolveCommandNamePrefersLongestMatch pins the ordering that makes
// two-token registration work: the two-token key must win over the parent
// verb's one-token key, and a one-token command must not swallow its own first
// argument.
func TestResolveCommandNamePrefersLongestMatch(t *testing.T) {
	if _, ok := Lookup("sandbox agent-upgrade"); !ok {
		t.Skip("no two-token command registered")
	}

	name, rest := resolveCommandName([]string{"sandbox", "agent-upgrade", "sb-1", "--force"})
	if name != "sandbox agent-upgrade" {
		t.Fatalf("two-token name lost to the parent verb: got %q", name)
	}
	if len(rest) != 2 || rest[0] != "sb-1" || rest[1] != "--force" {
		t.Fatalf("arguments after a two-token name = %v, want [sb-1 --force]", rest)
	}

	// A one-token verb whose next argument merely looks like a subcommand must
	// keep that argument.
	name, rest = resolveCommandName([]string{"sandbox", "create", "--name", "x"})
	if name != "sandbox" {
		t.Fatalf("one-token dispatch broke: got %q", name)
	}
	if len(rest) != 3 || rest[0] != "create" {
		t.Fatalf("arguments after a one-token name = %v, want [create --name x]", rest)
	}
}
