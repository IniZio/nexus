package main

import (
	"os"

	"github.com/IniZio/nexus3/internal/cli"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
)

func main() {
	// Sentinel dispatch: when re-exec'd into a user+network namespace by
	// StartNetnsRuntime, this binary runs as the netns child process.
	// This must be first — before any flag/cobra/CLI parsing.
	if os.Getenv(cloudhypervisor.NetnsRunEnv) == "1" {
		cloudhypervisor.RunNetnsChild()
		return
	}
	os.Exit(cli.Run(os.Args[1:]))
}
