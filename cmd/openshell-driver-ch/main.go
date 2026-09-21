package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	pb "github.com/IniZio/nexus/internal/openshell/computedriverpb"
	"github.com/IniZio/nexus/internal/openshell/driver"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

func main() {
	root := &cobra.Command{
		Use:          "openshell-driver-ch",
		SilenceUsage: true,
	}
	root.AddCommand(serveCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func serveCmd() *cobra.Command {
	var (
		socketPath     string
		stateDir       string
		gatewayAddr    string
		rootfsStaging  string
		rootfsMaxBytes uint64
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the nexus-ch compute driver gRPC server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if stateDir == "" {
				return fmt.Errorf("--state-dir is required")
			}
			if gatewayAddr == "" {
				gatewayAddr = os.Getenv("OPENSHELL_ENDPOINT")
			}
			if err := os.MkdirAll(stateDir, 0700); err != nil {
				return fmt.Errorf("mkdir state-dir: %w", err)
			}
			if rootfsStaging == "" {
				rootfsStaging = filepath.Join(stateDir, "rootfs-staging")
			}

			dbPath := filepath.Join(stateDir, "driver.db")
			st, err := driver.NewBboltStore(dbPath)
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}
			defer st.Close()

			if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove stale socket: %w", err)
			}
			ln, err := net.Listen("unix", socketPath)
			if err != nil {
				return fmt.Errorf("listen %s: %w", socketPath, err)
			}
			slog.Info("driver listening", "socket", socketPath, "state_dir", stateDir)

			srv := driver.New(st, nil, gatewayAddr, rootfsStaging, rootfsMaxBytes)
			grpcSrv := grpc.NewServer()
			pb.RegisterComputeDriverServer(grpcSrv, srv)

			go func() {
				if err := grpcSrv.Serve(ln); err != nil {
					slog.Error("serve", "err", err)
				}
			}()

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			<-sig
			slog.Info("shutting down")
			grpcSrv.GracefulStop()
			return nil
		},
	}
	cmd.Flags().StringVar(&socketPath, "socket", "/tmp/nexus-ch-driver.sock", "unix socket path for the gRPC listener")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "directory for driver.db and staging files (required)")
	cmd.Flags().StringVar(&gatewayAddr, "gateway-addr", "", "OpenShell gateway address (overrides OPENSHELL_ENDPOINT)")
	cmd.Flags().StringVar(&rootfsStaging, "rootfs-staging-dir", "", "staging dir for rootfs tar uploads (default: <state-dir>/rootfs-staging)")
	cmd.Flags().Uint64Var(&rootfsMaxBytes, "rootfs-max-bytes", 10*1024*1024*1024, "max rootfs tar size in bytes")
	return cmd
}
