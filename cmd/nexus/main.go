package main

import (
	"os"
	"path/filepath"

	_ "github.com/IniZio/nexus/internal/core/hostbin/embedded"

	"github.com/IniZio/nexus/internal/cli"
	"github.com/IniZio/nexus/internal/core/driver/backends"
	"github.com/IniZio/nexus/internal/supervisor"
)

func main() {
	// Re-exec'd netns/virtiofsd children must dispatch before any CLI parsing.
	backends.MaybeRunChild()

	// Hidden subcommand: detached per-sandbox supervisor.
	// Dispatched before CLI routing so the supervisor process never enters the
	// CLI machinery (JSON flag scanning, command registry, etc.).
	if len(os.Args) > 1 && os.Args[1] == supervisor.HiddenSubcommand {
		runSupervisorMain(os.Args[2:])
		return
	}

	// argv[0] dispatch: when hard-linked as "nexus-guest-shell" by
	// "nexus herdr install-default-shell", run the fail-open guest-shell
	// entry point. This path has a top-level panic recovery and no PATH lookup.
	if filepath.Base(os.Args[0]) == "nexus-guest-shell" {
		cli.RunHerdrGuestShell()
		return
	}

	os.Exit(cli.Run(os.Args[1:]))
}
