# Transport Layer

A protocol-agnostic transport layer for secure communication between coordination server and node agents. Supports SSH and HTTP protocols with connection pooling, retries, and robust error handling.

## Features

- **Protocol Abstraction**: Common interface for SSH and HTTP transports
- **SSH Transport**: Extracted from QEMU provider, made reusable  
- **HTTP Transport**: REST API communication with authentication and retries
- **Connection Pooling**: Efficient connection management and reuse
- **Error Handling**: Comprehensive retry logic with exponential backoff
- **Security**: Key-based authentication, token management, connection validation

## Architecture

```
pkg/transport/
├── interface.go      # Transport protocol definition
├── ssh.go          # SSH implementation 
├── http.go         # HTTP client implementation
├── pool.go         # Connection pooling and management
├── config.go       # Transport configuration and factory
├── example.go      # Usage examples
└── *_test.go       # Comprehensive tests
```

## Quick Start

### Basic Usage

```go
import "github.com/nexus/nexus/pkg/transport"

// Create transport manager
manager := transport.NewManager()

// Register SSH configuration
sshConfig := transport.CreateDefaultSSHConfig(
    "server.example.com:22", 
    "username", 
    "/home/user/.ssh/id_rsa",
)
err := manager.RegisterConfig("ssh-node", sshConfig)

// Create transport pool
pool, err := manager.CreatePool("ssh-node")
defer pool.Close()

// Get transport from pool
ctx := context.Background()
transport, err := pool.Get(ctx, "server.example.com:22")
defer transport.Disconnect(ctx)

// Execute command
result, err := transport.Execute(ctx, &transport.Command{
    Cmd:          []string{"ls", "-la"},
    CaptureOutput: true,
    Timeout:       30 * time.Second,
})
```

### HTTP Transport

```go
// Register HTTP configuration  
httpConfig := transport.CreateDefaultHTTPConfig(
    "https://api.example.com:8080", 
    "api-token-123",
)
err := manager.RegisterConfig("http-api", httpConfig)

// Use HTTP transport
transport, err := manager.CreateTransport("http-api")
result, err := transport.Execute(ctx, &transport.Command{
    Cmd: []string{"GET", "/api/v1/nodes"},
    CaptureOutput: true,
})
```

## Configuration

### SSH Configuration

```yaml
protocol: "ssh"
target: "server.example.com:22"
auth:
  type: "ssh_key"           # or "password"
  username: "user"  
  key_path: "/home/user/.ssh/id_rsa"
  # Alternative: key_data: "-----BEGIN RSA..."
timeout: 30s
retry_policy:
  max_retries: 3
  initial_delay: 1s
  max_delay: 10s
  backoff_factor: 2.0
connection:
  max_conns: 5
  max_idle: 2
  max_lifetime: 1h
  keep_alive: true
security:
  strict_host_key_checking: true
  host_key_algorithms: ["rsa-sha2-256", "rsa-sha2-512"]
```

### HTTP Configuration

```yaml
protocol: "http"  # or "https"
target: "https://api.example.com:8080"
auth:
  type: "token"             # or "header", "certificate"
  token: "api-token-123"
  # Alternative for header auth:
  # headers:
  #   Authorization: "Bearer token123"
  #   X-API-Key: "secret-key"
security:
  verify_certificate: true
  ca_cert_path: "/path/to/ca.crt"
  skip_tls_verify: false
```

## Connection Pooling

Transport layer provides efficient connection pooling:

```go
pool, err := manager.CreatePool("ssh-node")
defer pool.Close()

// Get pooled connection
transport, err := pool.Get(ctx, "server.example.com:22")
defer transport.Disconnect(ctx)

// Automatic reuse and cleanup
metrics := pool.GetMetrics()
fmt.Printf("Reused connections: %d", metrics.TotalReused)
```

## Error Handling

Comprehensive error types with retry information:

```go
result, err := transport.Execute(ctx, cmd)
if err != nil {
    if transportErr, ok := err.(*transport.TransportError); ok {
        if transportErr.Retryable {
            // Implement retry logic
            return retryWithBackoff()
        }
    }
    return err
}
```

## Integration with QEMU Provider

The transport layer extracts SSH functionality from QEMU provider:

```go
// Before: Direct SSH execution
sshCmd := fmt.Sprintf("ssh -o StrictHostKeyChecking=no %s '%s'", remote, cmd)
execCmd := exec.CommandContext(ctx, "sh", "-c", sshCmd)

// After: Transport layer usage
transport, err := p.CreateTransport("remote-qemu")
err = transport.Connect(ctx, p.remote)
defer transport.Disconnect(ctx)

result, err := transport.Execute(ctx, &transport.Command{
    Cmd: []string{"sh", "-c", cmd},
    CaptureOutput: true,
})
```

## Coordination Server Integration

For communication between coordination server and node agents:

```go
// Node configuration
nodeConfig := transport.CreateDefaultSSHConfig(
    "coordinator.example.com:3001",
    "nexus", 
    "/home/nexus/.ssh/id_rsa",
)

// Parallel execution across nodes
results := make(chan *transport.Result, len(nodes))
for name, pool := range pools {
    go func(nodeName string, p *transport.Pool) {
        transport, _ := p.Get(ctx, "")
        defer transport.Disconnect(ctx)
        
        result, _ := transport.Execute(ctx, &transport.Command{
            Cmd: []string{"ps", "aux"},
            CaptureOutput: true,
        })
        results <- result
    }(name, pool)
}
```

## Security Features

- **SSH Key Authentication**: RSA/ED25519 key support with strict host key checking
- **Token-based HTTP Auth**: Bearer tokens and custom headers
- **TLS Certificate Validation**: CA certificate support and custom verification
- **Connection Security**: Configurable ciphers, KEX algorithms, and host key policies

## Performance Optimizations

- **Connection Reuse**: Efficient pooling reduces connection overhead
- **Exponential Backoff**: Intelligent retry with configurable limits  
- **Connection Limits**: Configurable max connections and idle timeouts
- **Resource Cleanup**: Automatic connection lifecycle management

## Testing

Comprehensive test coverage including:

```bash
# Run all tests
go test ./pkg/transport/ -v

# Run specific test categories
go test ./pkg/transport/ -run TestSSHTransport
go test ./pkg/transport/ -run TestHTTPTransport  
go test ./pkg/transport/ -run TestPool
go test ./pkg/transport/ -run TestManager
```

## Migration from Direct SSH

To migrate existing SSH usage:

1. Replace direct `ssh` command calls with transport layer
2. Use `transport.Command` instead of command string construction
3. Handle `transport.TransportError` for retry logic
4. Implement connection pooling for better performance

Before:
```go
cmd := fmt.Sprintf("ssh -i %s %s@%s '%s'", key, user, host, command)
execCmd := exec.CommandContext(ctx, "sh", "-c", cmd)
```

After:
```go
transport, _ := manager.CreateTransport("ssh")
transport.Connect(ctx, fmt.Sprintf("%s@%s", user, host))
defer transport.Disconnect(ctx)

result, _ := transport.Execute(ctx, &transport.Command{
    Cmd: []string{"sh", "-c", command},
})
```

## Configuration Management

Save and load transport configurations:

```go
// Save configurations
err := manager.SaveConfig("/etc/nexus/transports.yaml")

// Load configurations  
err := manager.LoadConfig("/etc/nexus/transports.yaml")

// List available configurations
configs := manager.ListConfigs()
```

This transport layer provides a solid foundation for secure, efficient communication in distributed nexus environments.
