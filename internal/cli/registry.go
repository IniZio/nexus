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

// Lookup returns the command registered under name, if any.
func Lookup(name string) (Command, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	c, ok := commands[name]
	return c, ok
}

// All returns all registered commands sorted by name.
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
