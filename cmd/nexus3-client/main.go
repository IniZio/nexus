// nexus3-client is the laptop-side companion to nexus3 for herdr remote
// clients. It carries only the pieces that must run where the full nexus3
// CLI cannot build (macOS): the herdr startup hook that mirrors a nexus3
// host's auto port-forwards onto 127.0.0.1 of the client, and the ABI probe
// plugins/herdr/build.sh uses at install time.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/IniZio/nexus3/internal/clientagent"
)

// abi mirrors plugins/herdr/abi; build.sh compares the two.
const abi = "3"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nexus3-client: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) >= 2 && args[0] == "herdr" {
		switch args[1] {
		case "abi":
			fmt.Println(abi)
			return nil
		case "local-agent-startup":
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			return clientagent.RunStartup(ctx)
		}
	}
	if len(args) == 1 && args[0] == "version" {
		fmt.Println("nexus3-client (herdr remote-client companion)")
		return nil
	}
	return fmt.Errorf("usage: nexus3-client herdr <abi|local-agent-startup> | version")
}
