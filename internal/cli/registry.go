package cli

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Command is a CLI subcommand. Each command registers itself via Register,
// called from an init() function in its own cmd_*.go file. This allows new
// commands to be added without editing any shared file, preventing merge
// conflicts between parallel development agents.
//
// Commands must support per-subcommand flag parsing via flag.FlagSet inside
// their Run implementation.
type Command struct {
	Name    string
	Summary string
	// Hidden suppresses the command from the usage banner. A hidden command is
	// still runnable and still resolved by Lookup — hidden means absent from the
	// listing, not disabled.
	Hidden bool
	// Run executes the command. args are the arguments that follow the command
	// name on the command line. Use flag.FlagSet to parse subcommand-specific
	// flags from args.
	Run func(ctx context.Context, args []string, out *Output) error
}

var (
	registryMu sync.RWMutex
	commands   = make(map[string]Command)
)

// Register adds a command to the global registry. It panics if a command with
// the same name has already been registered; a silent overwrite would hide
// real bugs (e.g. two init() functions defining the same verb).
func Register(c Command) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := commands[c.Name]; exists {
		panic(fmt.Sprintf("cli: duplicate command registration: %q", c.Name))
	}
	commands[c.Name] = c
}

// resolveCommandName picks the registry key the argument list addresses, and
// returns it with the arguments that follow it.
//
// Command names are usually a single token, but a command may register a
// two-token name ("sandbox agent-upgrade") to hang a subcommand off an existing
// verb without editing that verb's dispatcher. Such a name is only reachable if
// dispatch tries the two-token key BEFORE the one-token key — otherwise the
// parent verb ("sandbox") wins, its own switch does not know the subcommand,
// and the registered command is dead code that still prints in the usage
// banner. Longest match first is what makes registration alone sufficient.
func resolveCommandName(args []string) (name string, rest []string) {
	if len(args) >= 2 {
		two := args[0] + " " + args[1]
		if _, ok := Lookup(two); ok {
			return two, args[2:]
		}
	}
	return args[0], args[1:]
}

// Lookup returns the command registered under name, if any.
func Lookup(name string) (Command, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	c, ok := commands[name]
	return c, ok
}

// All returns all registered commands sorted by name, including hidden ones.
func All() []Command {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Command, 0, len(commands))
	for _, c := range commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// AllVisible returns all non-hidden registered commands sorted by name.
// This is the set printed in the usage banner.
func AllVisible() []Command {
	all := All()
	out := all[:0:0]
	for _, c := range all {
		if !c.Hidden {
			out = append(out, c)
		}
	}
	return out
}
