package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/server"
)

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	workspaceDir := flag.String("workspace-dir", "/workspace", "Workspace directory path")
	token := flag.String("token", "", "JWT secret token for authentication")
	flag.Parse()

	if *token == "" {
		log.Fatal("Error: --token is required")
	}

	if err := runServer(*port, *workspaceDir, *token); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func runServer(port int, workspaceDir string, token string) error {
	srv, err := server.NewServer(port, workspaceDir, token)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		srv.Shutdown()
	}()

	log.Printf("Workspace daemon started on port %d", port)
	return srv.Start()
}
