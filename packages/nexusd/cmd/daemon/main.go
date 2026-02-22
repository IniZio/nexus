package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/nexus/nexus/packages/nexusd/pkg/server"
)

func getDefaultWorkspaceDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/nexus-workspaces"
	}
	return filepath.Join(home, ".nexus", "workspaces")
}

func ensureWorkspaceDir(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create workspace directory %s: %w", dir, err)
	}
	testFile := filepath.Join(dir, ".write-test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return fmt.Errorf("workspace directory %s is not writable: %w", dir, err)
	}
	os.Remove(testFile)
	return nil
}

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	workspaceDir := flag.String("workspace-dir", getDefaultWorkspaceDir(), "Workspace directory path")
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

	if err := ensureWorkspaceDir(*workspaceDir); err != nil {
		log.Fatalf("Error: %v", err)
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
