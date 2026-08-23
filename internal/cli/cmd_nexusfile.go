package cli

// cmd_nexusfile.go — shared helpers for Nexusfile verbs (bake/up/down).
// None of these helpers registers a command; they are internal utilities.

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/IniZio/nexus3/internal/cli/nexusfile"
	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/service"
)

// defaultNexusfile is the Nexusfile path resolved when --nexusfile is not set.
const defaultNexusfile = "Nexusfile"

// defaultNexusSection is the profile section used when --section is not set.
const defaultNexusSection = "dev"

// nexusVerbFlags holds the shared flags for bake/up/down.
type nexusVerbFlags struct {
	nexusfilePath string
	section       string
}

// parseNexusVerbFlags parses the flags common to all Nexusfile verbs.
// verb is the command name used in error messages.
func parseNexusVerbFlags(verb string, args []string) (nexusVerbFlags, []string, error) {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	var f nexusVerbFlags
	fs.StringVar(&f.nexusfilePath, "nexusfile", defaultNexusfile, "path to the Nexusfile")
	fs.StringVar(&f.section, "section", defaultNexusSection, "Nexusfile profile section to use")
	if err := fs.Parse(args); err != nil {
		return nexusVerbFlags{}, nil, &UsageError{Msg: verb + ": " + err.Error()}
	}
	return f, fs.Args(), nil
}

// runNexusVerbWithSvc loads the Nexusfile, resolves the section, and executes
// the selected command list sequentially inside the named sandbox via the
// agent exec path. It fails fast on the first non-zero exit.
//
// cmdsFrom is a function that extracts the appropriate command slice from a
// parsed Section (e.g. func(s nexusfile.Section) []string { return s.Bake }).
func runNexusVerbWithSvc(
	ctx context.Context,
	verb string,
	ref string,
	f nexusVerbFlags,
	cmdsFrom func(nexusfile.Section) []string,
	out *Output,
	svc *service.Service,
) error {
	nf, err := nexusfile.Load(f.nexusfilePath)
	if err != nil {
		return &CodedError{Code: ErrCodeInvalidArgument, Msg: fmt.Sprintf("%s: %v", verb, err), Err: err}
	}

	sec, err := nf.Section(f.section)
	if err != nil {
		return &CodedError{Code: ErrCodeInvalidArgument, Msg: fmt.Sprintf("%s: %v", verb, err), Err: err}
	}

	cmds := cmdsFrom(sec)
	for i, cmd := range cmds {
		opts := agent.ExecOptions{
			Argv:   []string{"bash", "-lc", cmd},
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		}
		exitCode, err := svc.Exec(ctx, ref, opts)
		if err != nil {
			return &CodedError{
				Code: agentCodeFor(err),
				Msg:  fmt.Sprintf("%s: command %d: %v", verb, i, err),
				Err:  err,
			}
		}
		if exitCode != 0 {
			return &ExitCodeError{Code: exitCode}
		}
	}
	return nil
}

// nexusVerbRun is the common Run body for bake/up/down. It builds the service,
// parses shared flags, resolves the sandbox ref, and delegates to
// runNexusVerbWithSvc.
func nexusVerbRun(verb string, cmdsFrom func(nexusfile.Section) []string) func(ctx context.Context, args []string, out *Output) error {
	return func(ctx context.Context, args []string, out *Output) error {
		f, positional, err := parseNexusVerbFlags(verb, args)
		if err != nil {
			return err
		}
		if len(positional) < 1 {
			return &UsageError{Msg: fmt.Sprintf("%s: usage: %s [--nexusfile <path>] [--section <name>] <sandbox-ref>", verb, verb)}
		}
		ref := positional[0]

		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: verb + ": " + err.Error(), Err: err}
		}

		return runNexusVerbWithSvc(ctx, verb, ref, f, cmdsFrom, out, svc)
	}
}
