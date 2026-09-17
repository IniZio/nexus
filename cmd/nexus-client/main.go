// nexus-client: laptop-side companion for herdr remote clients (macOS port-forward mirror + ABI probe).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/IniZio/nexus/internal/clientagent"
)

const abi = "3"

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nexus-client: "+err.Error())
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
			pidPath := clientagent.DefaultPidPath()
			clientagent.ReapPrevious(ctx, pidPath)
			if err := clientagent.WritePidfile(pidPath); err != nil {
				fmt.Fprintf(os.Stderr, "nexus-client: pidfile: %v\n", err)
			}
			defer os.Remove(pidPath)
			return clientagent.RunStartup(ctx)
		}
	}
	if len(args) == 1 && args[0] == "version" {
		fmt.Println("nexus-client " + version)
		return nil
	}
	return fmt.Errorf("usage: nexus-client herdr <abi|local-agent-startup> | version")
}
