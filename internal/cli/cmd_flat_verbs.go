package cli

import (
	"context"
)

// Flat lifecycle verbs: `nexus3 create`, `nexus3 ps`, `nexus3 rm`, and so on.
//
// # Why these exist
//
// The manual has documented the flat spelling as the target surface since
// D-PD-57, and 53 fenced invocations across the docs use it. None of them
// worked: the binary only ever had `nexus3 sandbox create`, so an operator
// following the manual hit "unknown command: create" on the first line of the
// quickstart. The docs were not wrong about the destination — the CLI had
// simply never caught up, and the validator could not see the gap because it
// resolves flat verbs to their `sandbox/<verb>` flag sets before checking.
//
// This is also the shape every neighbouring runtime uses: microsandbox spells
// it `msb create` / `msb ps`, not `msb sandbox create`.
//
// # Relationship to the `sandbox` group
//
// These are not reimplementations. Each delegates to exactly the same
// run* function the `sandbox` group calls, so behaviour, flags, error codes
// and JSON envelopes cannot drift between the two spellings — a flat verb and
// its grouped equivalent are the same code path reached by a different name.
//
// The `sandbox` group is KEPT rather than removed. It is the spelling the MCP
// tools, the herdr plugin and the existing scripts use, and the docs call it
// the legacy grouping rather than a mistake. Removing it would break working
// callers to no benefit.
//
// `ls` is registered as an alias of `ps` because both spellings are muscle
// memory (docker uses `ps`, microsandbox accepts both).
func init() {
	for _, fv := range flatVerbs {
		Register(Command{
			Name:    fv.name,
			Summary: fv.summary,
			Run:     flatVerbRunner(fv.target),
		})
	}
}

type flatVerb struct {
	name    string
	target  string // the `sandbox` subcommand this delegates to
	summary string
}

var flatVerbs = []flatVerb{
	{"create", "create", "Create a sandbox (flat spelling of `sandbox create`)"},
	{"ps", "list", "List sandboxes (flat spelling of `sandbox list`)"},
	{"ls", "list", "List sandboxes (alias of `ps`)"},
	{"rm", "rm", "Remove a sandbox (flat spelling of `sandbox rm`)"},
	{"start", "start", "Start a stopped sandbox (flat spelling of `sandbox start`)"},
	{"stop", "stop", "Stop a running sandbox (flat spelling of `sandbox stop`)"},
	{"pause", "pause", "Pause a running sandbox (flat spelling of `sandbox pause`)"},
	{"resume", "resume", "Resume a paused sandbox (flat spelling of `sandbox resume`)"},
}

// flatVerbRunner returns a Run function that prepends the target subcommand
// and hands off to runSandbox.
//
// Delegating through runSandbox rather than to the run* functions directly is
// deliberate: runSandbox also performs the substrate preflight for
// start/stop/pause/resume, and duplicating that here is exactly how the two
// spellings would come to report different errors for the same condition.
func flatVerbRunner(target string) func(context.Context, []string, *Output) error {
	return func(ctx context.Context, args []string, out *Output) error {
		return runSandbox(ctx, append([]string{target}, args...), out)
	}
}
