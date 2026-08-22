package main

import (
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/cli"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func main() {
	// Sentinel dispatch: when re-exec'd into a user+network namespace by
	// StartNetnsRuntime, this binary runs as the netns child process.
	// This must be first — before any flag/cobra/CLI parsing.
	if os.Getenv(cloudhypervisor.NetnsRunEnv) == "1" {
		cloudhypervisor.RunNetnsChild()
		return
	}

	// Hidden subcommand: detached per-sandbox supervisor.
	// Dispatched before CLI routing so the supervisor process never enters the
	// CLI machinery (JSON flag scanning, command registry, etc.).
	if len(os.Args) > 1 && os.Args[1] == supervisor.HiddenSubcommand {
		runSupervisorMain(os.Args[2:])
		return
	}

	// argv[0] dispatch: when hard-linked as "nexus3-guest-shell" by
	// "nexus3 herdr install-default-shell", run the fail-open guest-shell
	// entry point. This path has a top-level panic recovery and no PATH lookup.
	if filepath.Base(os.Args[0]) == "nexus3-guest-shell" {
		cli.RunHerdrGuestShell()
		return
	}

	os.Exit(cli.Run(os.Args[1:]))
}
