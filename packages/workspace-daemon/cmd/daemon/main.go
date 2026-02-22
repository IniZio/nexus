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
	jwtSecretFile := flag.String("jwt-secret-file", "", "Path to file containing JWT secret")
	flag.Parse()

	var tokenSecret string

	if *jwtSecretFile != "" {
		data, err := os.ReadFile(*jwtSecretFile)
		if err != nil {
			log.Fatalf("Error reading JWT secret file: %v", err)
		}
		tokenSecret = string(data)
		tokenSecret = fmt.Sprintf("\n%s", tokenSecret)
	} else if *token != "" {
		tokenSecret = *token
	}

	if tokenSecret == "" {
		log.Fatal("Error: either --token or --jwt-secret-file is required")
	}

	if err := runServer(*port, *workspaceDir, tokenSecret); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func runServer(port int, workspaceDir string, token string) error {
	srv, err := server.NewServer(port, workspaceDir, token)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	shutdownCh := make(chan struct{})

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		srv.Shutdown()
		close(shutdownCh)
	}()

	log.Printf("Workspace daemon started on port %d", port)
	if err := srv.Start(); err != nil {
		return err
	}

	<-shutdownCh
	return nil
}
