// Package main is the Nexus daemon entry point.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/inizio/nexus/packages/nexus/internal/daemon"
)

func main() {
	defaultData := defaultDataDir()

	dbPath := flag.String("db", filepath.Join(defaultData, "nexus.db"), "SQLite database path")
	socketPath := flag.String("socket", filepath.Join(defaultData, "nexusd.sock"), "Unix socket path")
	firecracker := flag.Bool("firecracker", false, "Enable Firecracker VM backend")
	fcBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary name")
	kernelPath := flag.String("kernel", os.Getenv("NEXUS_FIRECRACKER_KERNEL"), "Firecracker kernel image path")
	rootfsPath := flag.String("rootfs", os.Getenv("NEXUS_FIRECRACKER_ROOTFS"), "Firecracker rootfs image path")
	workDirRoot := flag.String("workdir-root", filepath.Join(defaultData, "firecracker-vms"), "Firecracker VM work dir root")
	nodeName := flag.String("node-name", hostName(), "Node name for identity")
	flag.Parse()

	cfg := daemon.Config{
		DBPath:             *dbPath,
		SocketPath:         *socketPath,
		FirecrackerEnabled: *firecracker,
		FirecrackerBin:     *fcBin,
		KernelPath:         *kernelPath,
		RootFSPath:         *rootfsPath,
		WorkDirRoot:        *workDirRoot,
		NodeName:           *nodeName,
	}

	d, err := daemon.New(cfg)
	if err != nil {
		log.Fatalf("daemon: init: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := d.Start(ctx); err != nil && err != context.Canceled {
		log.Printf("daemon: stopped: %v", err)
	}

	if err := d.Stop(); err != nil {
		log.Printf("daemon: stop: %v", err)
	}
}

func defaultDataDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "nexus")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/var/lib/nexus"
	}
	return filepath.Join(home, ".local", "state", "nexus")
}

func hostName() string {
	name, _ := os.Hostname()
	return name
}
