package cli

import "github.com/IniZio/nexus3/internal/cli/nexusfile"

func init() {
	Register(Command{
		Name:    "down",
		Summary: "Run the Nexusfile down commands inside a sandbox (stop services)",
		Run:     nexusVerbRun("down", func(s nexusfile.Section) []string { return s.Down }),
	})
}
