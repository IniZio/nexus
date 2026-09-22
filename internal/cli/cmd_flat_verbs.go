package cli

import (
	"context"
)

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
	target  string
	summary string
}

var flatVerbs = []flatVerb{
	{"create", "create", "Create a sandbox (flat spelling of `sandbox create`)"},
	{"ps", "list", "List sandboxes (flat spelling of `sandbox list`)"},
	{"ls", "list", "List sandboxes (alias of `ps`)"},
	{"rm", "rm", "Remove a sandbox (flat spelling of `sandbox rm`)"},
	{"start", "start", "Start a stopped sandbox (flat spelling of `sandbox start`)"},
	{"stop", "stop", "Stop a running sandbox (flat spelling of `sandbox stop`)"},
}

func flatVerbRunner(target string) func(context.Context, []string, *Output) error {
	return func(ctx context.Context, args []string, out *Output) error {
		return runSandbox(ctx, append([]string{target}, args...), out)
	}
}
