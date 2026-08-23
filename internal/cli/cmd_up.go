package cli

import "github.com/IniZio/nexus3/internal/cli/nexusfile"

func init() {
	Register(Command{
		Name:    "up",
		Summary: "Run the Nexusfile up commands inside a sandbox (build images + start services)",
		Run:     nexusVerbRun("up", func(s nexusfile.Section) []string { return s.Up }),
	})
}
