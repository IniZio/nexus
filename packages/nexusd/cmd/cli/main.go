package main

import (
	"log"
	"os"

	"github.com/nexus/nexus/packages/nexusd/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		if err != cli.ErrDaemonNotRunning {
			log.Fatal(err)
		}
		os.Exit(1)
	}
}
