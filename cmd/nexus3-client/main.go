// nexus3-client: laptop-side companion for herdr remote clients (macOS port-forward mirror + ABI probe).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/IniZio/nexus3/internal/clientagent"
)

const abi = "3"

var version = "dev"

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
		fmt.Println("nexus3-client " + version)
		return nil
	}
	return fmt.Errorf("usage: nexus3-client herdr <abi|local-agent-startup> | version")
}
