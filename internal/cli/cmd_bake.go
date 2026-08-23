package cli

import "github.com/IniZio/nexus3/internal/cli/nexusfile"

func init() {
	Register(Command{
		Name:    "bake",
		Summary: "Run the Nexusfile bake commands inside a sandbox (installs dependencies)",
		Run:     nexusVerbRun("bake", func(s nexusfile.Section) []string { return s.Bake }),
	})
}
