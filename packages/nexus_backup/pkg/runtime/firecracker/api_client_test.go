package firecracker

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAPIClientPutReturnsEndpointStatusOnFailure(t *testing.T) {
	client := newAPIClient("/tmp/nonexistent.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := client.put(ctx, "/machine-config", map[string]any{"vcpu_count": 1})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "firecracker API PUT /machine-config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAPIClientGetReturnsEndpointStatusOnFailure(t *testing.T) {
	client := newAPIClient("/tmp/nonexistent.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var result map[string]any
	err := client.get(ctx, "/machine-config", &result)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "firecracker API GET /machine-config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAPIClientPatchReturnsEndpointStatusOnFailure(t *testing.T) {
	client := newAPIClient("/tmp/nonexistent.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := client.patch(ctx, "/machine-config", map[string]any{"vcpu_count": 2})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "firecracker API PATCH /machine-config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAPIClientPutSuccess(t *testing.T) {
	// Create a test server over unix socket
	tempDir := t.TempDir()
	sockPath := tempDir + "/test.sock"

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	// Server that accepts PUT requests
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				n, _ := c.Read(buf)
				req := string(buf[:n])
				if strings.Contains(req, "PUT /machine-config") {
					c.Write([]byte("HTTP/1.1 204 No Content\r\n\r\n"))
				} else {
					c.Write([]byte("HTTP/1.1 404 Not Found\r\n\r\n"))
				}
			}(conn)
		}
	}()

	client := newAPIClient(sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.put(ctx, "/machine-config", map[string]any{"vcpu_count": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAPIClientGetSuccess(t *testing.T) {
	tempDir := t.TempDir()
	sockPath := tempDir + "/test.sock"

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	// Server that returns JSON response
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				n, _ := c.Read(buf)
				req := string(buf[:n])
				if strings.Contains(req, "GET /machine-config") {
					body := `{"vcpu_count": 2, "mem_size_mib": 512}`
					c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " + fmt.Sprintf("%d", len(body)) + "\r\n\r\n" + body))
				} else {
					c.Write([]byte("HTTP/1.1 404 Not Found\r\n\r\n"))
				}
			}(conn)
		}
	}()

	client := newAPIClient(sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result map[string]any
	err = client.get(ctx, "/machine-config", &result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["vcpu_count"] != float64(2) {
		t.Fatalf("expected vcpu_count=2, got %v", result["vcpu_count"])
	}
}

func TestAPIClientContextCancellation(t *testing.T) {
	tempDir, err := os.MkdirTemp("/tmp", "nexus-api-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	sockPath := tempDir + "/test.sock"

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	// Slow server that never responds
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Just hold the connection open
			go func(c net.Conn) {
				time.Sleep(10 * time.Second)
				c.Close()
			}(conn)
		}
	}()

	client := newAPIClient(sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err = client.put(ctx, "/machine-config", map[string]any{"vcpu_count": 1})
	if err == nil {
		t.Fatal("expected error due to context timeout")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Fatalf("expected context error, got: %v", err)
	}
}

func TestAPIClientServerError(t *testing.T) {
	tempDir := t.TempDir()
	sockPath := tempDir + "/test.sock"

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	// Server that returns error status
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Read the request first to avoid broken pipe
				buf := make([]byte, 1024)
				c.Read(buf)
				c.Write([]byte("HTTP/1.1 500 Internal Server Error\r\n\r\n"))
			}(conn)
		}
	}()

	client := newAPIClient(sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.put(ctx, "/machine-config", map[string]any{"vcpu_count": 1})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error, got: %v", err)
	}
}
