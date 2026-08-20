package cli

// stripArgvSeparator removes a single leading "--" from a guest command.
//
// Go's flag package stops parsing at the first non-flag argument, so for
//
//	nexus3 exec myproject/hello -- uname -r
//
// parsing halts on "myproject/hello" and the "--" is never consumed. It lands
// in fs.Args() and, taken as argv, becomes the executable name — so the
// documented invocation failed inside the guest with:
//
//	cmd.Start: exec: "--": executable file not found in $PATH
//
// Both `exec` and `run` document the "--" form (run's own usage string spells
// it "run [flags] <image-ref> -- <command> [args...]"), and 19 invocations
// across the manual use it, so this broke the documented shape of both.
//
// Only ONE leading separator is stripped, and only in the leading position. A
// later "--" belongs to the guest command — `nexus3 exec box -- git log --`
// must reach git intact — and so must a second one, as in
// `nexus3 exec box -- sh -c 'cmd -- arg'`. Stripping more than the first would
// silently rewrite the operator's command.
func stripArgvSeparator(argv []string) []string {
	if len(argv) > 0 && argv[0] == "--" {
		return argv[1:]
	}
	return argv
}
