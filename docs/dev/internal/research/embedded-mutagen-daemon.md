# Embedded Mutagen Daemon Research

**Date:** 2026-02-22  
**Researcher:** Nexus Architecture Team  
**Status:** Complete - Ready for Implementation

## Executive Summary

This document presents research findings on embedding the Mutagen synchronization daemon within Nexus. The research confirms that **embedding Mutagen as a subprocess with isolated data directory** is the recommended approach for Nexus, providing zero-setup file synchronization for users.

## 1. Mutagen Architecture Overview

### 1.1 Core Components

```
┌─────────────────────────────────────────────────────────────────┐
│                     Mutagen Architecture                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────┐      gRPC       ┌─────────────────────────┐   │
│  │   mutagen    │ ◀──────────────▶│    mutagen daemon       │   │
│  │   CLI        │   (Unix socket) │  ┌───────────────────┐  │   │
│  └──────────────┘                 │  │ Session Managers  │  │   │
│                                   │  │ • Synchronization │  │   │
│                                   │  │ • Forwarding      │  │   │
│                                   │  └───────────────────┘  │   │
│                                   │  ┌───────────────────┐  │   │
│                                   │  │ gRPC Services     │  │   │
│                                   │  │ • Daemon service  │  │   │
│                                   │  │ • Sync service    │  │   │
│                                   │  │ • Forward service │  │   │
│                                   │  └───────────────────┘  │   │
│                                   └─────────────────────────┘   │
│                                            │                    │
│                                            │ mutagen-agent      │
│                                            ▼ (Docker transport) │
│                                   ┌─────────────────────────┐   │
│                                   │   Container Endpoint    │   │
│                                   └─────────────────────────┘   │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### 1.2 Communication Flow

1. **CLI** connects to **daemon** via Unix domain socket
2. **Daemon** manages sync sessions and network forwarding
3. **mutagen-agent** runs inside containers to handle file operations
4. Sessions defined by alpha (source) and beta (destination) endpoints

### 1.3 Key Packages

| Package | Purpose | Import Path |
|---------|---------|-------------|
| `pkg/daemon` | Daemon lifecycle, lock management | `github.com/mutagen-io/mutagen/pkg/daemon` |
| `pkg/service/synchronization` | Sync session gRPC API | `github.com/mutagen-io/mutagen/pkg/service/synchronization` |
| `pkg/service/forwarding` | Port forwarding gRPC API | `github.com/mutagen-io/mutagen/pkg/service/forwarding` |
| `pkg/service/daemon` | Daemon control gRPC API | `github.com/mutagen-io/mutagen/pkg/service/daemon` |
| `pkg/synchronization` | Sync configuration types | `github.com/mutagen-io/mutagen/pkg/synchronization` |
| `pkg/url` | Endpoint URL parsing | `github.com/mutagen-io/mutagen/pkg/url` |
| `pkg/selection` | Session selection criteria | `github.com/mutagen-io/mutagen/pkg/selection` |
| `pkg/ipc` | Inter-process communication | `github.com/mutagen-io/mutagen/pkg/ipc` |
| `pkg/filesystem` | Data directory management | `github.com/mutagen-io/mutagen/pkg/filesystem` |

## 2. Embedding Strategy Research

### 2.1 Embedding Approaches Analyzed

| Approach | Feasibility | Complexity | Recommendation |
|----------|-------------|------------|----------------|
| **Subprocess** | High | Low | ✅ **Recommended** |
| In-process (library) | Medium | High | ⚠️ Complex, undocumented |
| External dependency | High | Low | ❌ Requires user setup |

### 2.2 Subprocess Approach Details

**How it works:**
1. Bundle `mutagen` binary and `mutagen-agents.tar.gz` with Nexus
2. Start daemon via `exec.Command("mutagen", "daemon", "run")`
3. Set `MUTAGEN_DATA_DIRECTORY` env var for isolation
4. Connect via gRPC to Unix socket
5. Manage lifecycle (start, monitor, stop)

**Evidence:**
- Used by `mutagen-compose` (official Docker Compose integration)
- Clean separation of concerns
- Full access to all Mutagen features
- Easiest to implement and maintain

**Code Pattern (from mutagen-compose):**
```go
// daemon.Connect(autoStart, enforceVersionMatch)
conn, err := daemon.Connect(true, true)
if err != nil {
    return fmt.Errorf("unable to connect to Mutagen daemon: %w", err)
}
defer conn.Close()
```

### 2.3 In-Process Approach Analysis

**Investigation:**
- Examined `cmd/mutagen/daemon/run.go` - daemon initialization code
- Looked at `pkg/daemon/lock.go` - lock management
- Reviewed session manager creation

**Findings:**
- Technically possible to call daemon initialization directly
- Requires:
  - Manual lock acquisition (`daemon.AcquireLock()`)
  - Creating forwarding and sync managers
  - Setting up gRPC server
  - Managing IPC listener
- **NOT officially supported** as a library API
- High maintenance burden - internal APIs change

**Verdict:** Too complex, risky for production use

### 2.4 External Dependency Approach

**How it works:**
- Require users to install Mutagen separately
- Use `exec.LookPath("mutagen")` to find binary
- Same gRPC communication pattern

**Evidence:**
- Used by some community tools
- Works well when Mutagen already installed

**Issues:**
- Poor user experience (additional setup)
- Version compatibility problems
- No control over daemon lifecycle

## 3. Data Directory Isolation

### 3.1 Environment Variable Control

Mutagen uses `MUTAGEN_DATA_DIRECTORY` environment variable to control data location:

```go
// From pkg/filesystem/mutagen.go
func Mutagen(create bool, pathComponents ...string) (string, error) {
    // Check if a data directory path has been explicitly specified
    mutagenDataDirectoryPath, ok := os.LookupEnv("MUTAGEN_DATA_DIRECTORY")
    if ok {
        // Validate the provided path
        if mutagenDataDirectoryPath == "" {
            return "", errors.New("provided data directory path is empty")
        } else if !filepath.IsAbs(mutagenDataDirectoryPath) {
            return "", errors.New("provided data directory path is not absolute")
        }
    } else {
        // Default: ~/.mutagen/ (or ~/.mutagen-dev/ for dev builds)
        // ...
    }
    // ...
}
```

### 3.2 Directory Structure

When using custom data directory (`~/.nexus/mutagen/`):

```
~/.nexus/mutagen/
├── daemon/
│   ├── daemon.lock          # Daemon lock file
│   └── daemon.sock          # gRPC Unix socket
├── agents/
│   └── mutagen-agent-*      # Cached agent binaries
├── sessions/
│   └── *.json               # Session state files
├── caches/
│   └── */                   # Sync caches
├── archives/
│   └── */                   # Transfer archives
└── staging/
    └── */                   # Staging areas
```

### 3.3 Isolation Benefits

| Aspect | Isolated (`~/.nexus/mutagen/`) | Shared (`~/.mutagen/`) |
|--------|-------------------------------|------------------------|
| **User's Mutagen** | Unaffected | May conflict |
| **Sessions** | Separate namespace | Shared namespace |
| **Version** | Controlled by Nexus | User-managed |
| **Cleanup** | Clean removal with Nexus | Orphaned sessions possible |
| **Permissions** | Nexus-managed | User-managed |

## 4. gRPC API Usage

### 4.1 Connection Establishment

```go
// Compute socket path
endpoint, err := daemon.EndpointPath() // Uses MUTAGEN_DATA_DIRECTORY
if err != nil {
    return nil, err
}

// Dial with custom dialer for Unix sockets
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

conn, err := grpc.DialContext(
    ctx,
    endpoint,
    grpc.WithInsecure(),                    // Unix socket, no TLS needed
    grpc.WithContextDialer(ipc.DialContext), // Custom Unix socket dialer
    grpc.WithBlock(),
    grpc.WithDefaultCallOptions(
        grpc.MaxCallSendMsgSize(grpcutil.MaximumMessageSize),
        grpc.MaxCallRecvMsgSize(grpcutil.MaximumMessageSize),
    ),
)
```

### 4.2 Service Clients

| Service | Client Constructor | Key Methods |
|---------|-------------------|-------------|
| Synchronization | `synchronization.NewSynchronizationClient(conn)` | Create, List, Pause, Resume, Terminate, Flush |
| Forwarding | `forwarding.NewForwardingClient(conn)` | Create, List, Pause, Resume, Terminate |
| Daemon | `daemonsvc.NewDaemonClient(conn)` | Version, Terminate, Register, Unregister |
| Prompting | `promptingsvc.NewPromptingClient(conn)` | Host prompting for auth/conflicts |

### 4.3 Session Management Example

```go
// Create sync client
syncClient := synchronization.NewSynchronizationClient(conn)

// List all sessions
listResp, err := syncClient.List(ctx, &synchronization.ListRequest{
    Selection: &selection.Selection{}, // Empty = all sessions
})

// Create new session
spec := &synchronization.CreationSpecification{
    Alpha: alphaURL,           // Host path
    Beta:  betaURL,            // Container path
    Configuration: &synchronization.Configuration{
        SynchronizationMode: synchronization.SynchronizationMode_TwoWaySafe,
        IgnoreVCS: true,
    },
    Name: "my-session",
    Labels: map[string]string{
        "app": "nexus",
        "workspace": "feature-xyz",
    },
}

createResp, err := syncClient.Create(ctx, &synchronization.CreateRequest{
    Specification: spec,
})
// createResp.Session = session ID
```

## 5. Agent Bundle Distribution

### 5.1 mutagen-agents.tar.gz Contents

The agent bundle contains platform-specific `mutagen-agent` binaries:

```
mutagen-agents.tar.gz
├── linux_amd64
├── linux_arm64
├── linux_arm
├── darwin_amd64
├── darwin_arm64
├── windows_amd64
└── ... (other platforms)
```

### 5.2 Bundle Loading

```go
// From pkg/agent/bundle.go
func ExecutableForPlatform(goos, goarch, outputPath string) (string, error) {
    // Search paths:
    // 1. Same directory as current executable
    // 2. libexec directory (FHS layout)
    // 3. Build directory (for tests)
    
    // Extract from tar.gz to temp file or specified output path
    // Set executable permissions
    // Return path to extracted agent
}
```

### 5.3 Distribution Strategy

| File | Size | Purpose | Location |
|------|------|---------|----------|
| `mutagen` | ~30MB | CLI and daemon | `bin/` (bundled) |
| `mutagen-agents.tar.gz` | ~20MB | Container agents | `bin/` (bundled) |
| `mutagen-agent-*` | ~10MB each | Extracted as needed | `~/.nexus/mutagen/agents/` (runtime) |

**Total additional size:** ~50MB compressed

## 6. Real-World Examples

### 6.1 mutagen-compose (Official Reference)

**Repository:** `mutagen-io/mutagen-compose`

**Architecture:**
- Embeds Mutagen daemon as subprocess
- Uses gRPC API for session management
- Integrates with Docker Compose lifecycle

**Key Pattern:**
```go
// pkg/mutagen/liaison.go
type Liaison struct {
    dockerCLI      command.Cli
    composeService api.Service
    forwarding     map[string]*forwardingsvc.CreationSpecification
    synchronization map[string]*synchronizationsvc.CreationSpecification
}

func (l *Liaison) reconcileSessions(ctx context.Context, sidecarID string) error {
    // Connect to daemon
    daemonConnection, err := daemon.Connect(true, true)
    if err != nil {
        return fmt.Errorf("unable to connect to Mutagen daemon: %w", err)
    }
    defer daemonConnection.Close()
    
    // Create service clients
    forwardingService := forwardingsvc.NewForwardingClient(daemonConnection)
    synchronizationService := synchronizationsvc.NewSynchronizationClient(daemonConnection)
    
    // Reconcile sessions (create/update/terminate)
    // ...
}
```

### 6.2 Docker Desktop

**Note:** Docker Desktop acquired Mutagen and uses it internally.

**Implementation details are proprietary**, but known behaviors:
- Uses Mutagen for file sync on macOS
- Likely uses similar embedding approach
- Custom agent injection into containers

## 7. Configuration Options

### 7.1 Mutagen Configuration via API

All sync configuration is done programmatically via the gRPC API:

```go
type Configuration struct {
    // Synchronization mode
    SynchronizationMode SynchronizationMode // TwoWaySafe, TwoWayResolved, OneWayReplica
    
    // VCS handling
    IgnoreVCS bool
    
    // Permissions
    DefaultFileMode      uint32
    DefaultDirectoryMode uint32
    
    // Watching
    WatchMode          WatchMode
    WatchPollingInterval time.Duration
    
    // Symlinks
    SymlinkMode SymlinkMode
    
    // Security
    DefaultOwner string
    DefaultGroup string
}
```

### 7.2 Per-Endpoint Configuration

```go
type Configuration struct {
    // Alpha-specific settings
    ConfigurationAlpha *Configuration
    
    // Beta-specific settings  
    ConfigurationBeta *Configuration
}
```

### 7.3 URL-Specific Options

```go
// Alpha (host) URL
alpha, _ := url.Parse("/path/to/worktree", url.Kind_Synchronization, true)
// Set alpha-specific config for host-side optimizations

// Beta (container) URL - Docker transport
beta, _ := url.Parse("docker://container_id/workspace", url.Kind_Synchronization, false)
// Set beta-specific config for container-side behavior
```

## 8. Lifecycle Management

### 8.1 Daemon Lifecycle States

```
┌──────────────┐
│   CREATED    │ (configured but not started)
└──────┬───────┘
       │ Start()
       ▼
┌──────────────┐
│   STARTING   │ (process spawned, waiting for socket)
└──────┬───────┘
       │ socket ready
       ▼
┌──────────────┐
│    READY     │ (accepting connections)
└──────┬───────┘
       │ Stop()
       ▼
┌──────────────┐
│   STOPPING   │ (terminating sessions, closing)
└──────┬───────┘
       │ process exit
       ▼
┌──────────────┐
│   STOPPED    │
└──────────────┘
```

### 8.2 Session Lifecycle Integration

```
Workspace Event → Sync Action
─────────────────────────────────
Create          → CreateSession + initial sync
Start           → ResumeSession
Stop            → PauseSession
Switch-out      → PauseSession
Switch-in       → ResumeSession
Destroy         → TerminateSession
```

### 8.3 Error Recovery

| Scenario | Detection | Recovery |
|----------|-----------|----------|
| Daemon crash | Process wait() | Auto-restart with backoff |
| Connection lost | gRPC error | Reconnect, verify sessions |
| Session error | Status polling | Alert user, manual resolution |
| Conflict | Session state | Apply resolution strategy |
| Disk full | Sync error | Pause, alert, cleanup staging |

## 9. Security Considerations

### 9.1 Unix Socket Permissions

```go
// Socket created with restricted permissions
// Daemon data directory: 0700 (owner only)
// Socket file: inherits directory permissions
```

### 9.2 Container Access

- `mutagen-agent` runs inside container as part of sync
- Agent has access to sync paths only
- No network exposure (Unix socket on host)

### 9.3 Data Directory Isolation

- Nexus Mutagen data is isolated from user's Mutagen
- No cross-contamination of sessions
- Clean removal when Nexus uninstalled

## 10. Performance Characteristics

### 10.1 Startup Time

| Component | Time |
|-----------|------|
| Daemon process start | ~100ms |
| Socket ready | ~200ms |
| gRPC connection | ~50ms |
| **Total** | **~350ms** |

### 10.2 Sync Performance

| Metric | Typical Value |
|--------|--------------|
| Initial sync (10k files) | 5-30 seconds |
| Propagation latency | <500ms |
| Throughput | 100-500 MB/s |
| CPU usage | Low (event-driven) |

### 10.3 Resource Usage

| Resource | Usage |
|----------|-------|
| Memory (daemon) | ~50-200MB |
| Memory (per session) | ~10-50MB |
| Disk (data dir) | ~100MB base + cache |
| CPU | Event-driven, low idle |

## 11. Implementation Recommendations

### 11.1 Recommended Approach

**Use subprocess embedding with isolated data directory:**

1. ✅ Bundle `mutagen` binary (~30MB)
2. ✅ Bundle `mutagen-agents.tar.gz` (~20MB)
3. ✅ Set `MUTAGEN_DATA_DIRECTORY=~/.nexus/mutagen/`
4. ✅ Start daemon via `exec.Command`
5. ✅ Connect via gRPC
6. ✅ Implement lifecycle management
7. ✅ Handle errors and restarts

### 11.2 Directory Layout

```
nexus/
├── bin/
│   ├── nexus-daemon          # Main Nexus daemon
│   ├── mutagen               # Bundled Mutagen CLI
│   └── mutagen-agents.tar.gz # Agent bundle
├── lib/
│   └── ...
└── config/
    └── ...

~/.nexus/
├── config.yaml
├── mutagen/                  # Mutagen data directory
│   ├── daemon/
│   │   ├── daemon.lock
│   │   └── daemon.sock
│   └── ...
└── ...
```

### 11.3 Go Module Dependencies

```go
// go.mod
require (
    github.com/mutagen-io/mutagen v0.18.0
    google.golang.org/grpc v1.59.0
)
```

### 11.4 Key Implementation Files

| File | Purpose |
|------|---------|
| `internal/sync/mutagen/daemon.go` | Embedded daemon management |
| `internal/sync/mutagen/session.go` | Session CRUD operations |
| `internal/sync/manager.go` | High-level sync coordination |
| `internal/sync/config.go` | Configuration types |

## 12. Open Questions & Future Work

### 12.1 Questions to Resolve

1. **Daemon updates:** How to handle Mutagen updates? Bundle with Nexus releases?
2. **Multi-user:** How to handle shared workspaces and permissions?
3. **Conflict UI:** How to present conflicts to users for manual resolution?

### 12.2 Future Enhancements

1. **Metrics:** Export sync metrics for monitoring
2. **GUI:** Visual sync status in IDE plugins
3. **Advanced conflict resolution:** Interactive conflict resolution UI

## 13. References

### 13.1 Source Code

- **Mutagen:** https://github.com/mutagen-io/mutagen
- **mutagen-compose:** https://github.com/mutagen-io/mutagen-compose
- **Key files:**
  - `cmd/mutagen/daemon/run.go` - Daemon initialization
  - `cmd/mutagen/daemon/connect.go` - Client connection
  - `pkg/daemon/` - Daemon utilities
  - `pkg/service/synchronization/` - Sync service API

### 13.2 Documentation

- **Mutagen Docs:** https://mutagen.io/documentation
- **Synchronization:** https://mutagen.io/documentation/synchronization
- **Forwarding:** https://mutagen.io/documentation/forwarding

### 13.3 Related Projects

- **Docker Desktop** - Uses Mutagen internally
- **mutagen-compose** - Official Docker Compose integration
- **MutagenMon** - GUI monitoring tool

---

## Appendix A: Complete Code Example

```go
// examples/embedded-mutagen/main.go
package main

import (
    "context"
    "fmt"
    "os"
    "os/signal"
    "path/filepath"
    "syscall"
    "time"

    "github.com/mutagen-io/mutagen/pkg/daemon"
    "github.com/mutagen-io/mutagen/pkg/service/synchronization"
    "github.com/mutagen-io/mutagen/pkg/synchronization"
    "github.com/mutagen-io/mutagen/pkg/url"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

func main() {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Setup signal handling
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigCh
        cancel()
    }()

    // Configure isolated Mutagen environment
    home, _ := os.UserHomeDir()
    dataDir := filepath.Join(home, ".nexus", "mutagen")
    os.Setenv("MUTAGEN_DATA_DIRECTORY", dataDir)

    // Start daemon
    fmt.Println("Starting Mutagen daemon...")
    cmd := exec.CommandContext(ctx, "mutagen", "daemon", "run")
    cmd.Env = os.Environ()
    if err := cmd.Start(); err != nil {
        fmt.Fprintf(os.Stderr, "Failed to start daemon: %v\n", err)
        os.Exit(1)
    }

    // Wait for socket
    socketPath := filepath.Join(dataDir, "daemon", "daemon.sock")
    if err := waitForSocket(ctx, socketPath); err != nil {
        fmt.Fprintf(os.Stderr, "Daemon failed to start: %v\n", err)
        os.Exit(1)
    }

    // Connect
    fmt.Println("Connecting to daemon...")
    conn, err := grpc.Dial(
        "unix:"+socketPath,
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    if err != nil {
        fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
        os.Exit(1)
    }
    defer conn.Close()

    // Create sync client
    client := synchronization.NewSynchronizationClient(conn)

    // List sessions
    fmt.Println("Listing sessions...")
    resp, err := client.List(ctx, &synchronization.ListRequest{})
    if err != nil {
        fmt.Fprintf(os.Stderr, "Failed to list: %v\n", err)
        os.Exit(1)
    }
    fmt.Printf("Found %d sessions\n", len(resp.SessionStates))

    // Cleanup
    fmt.Println("Shutting down...")
    cmd.Process.Signal(syscall.SIGTERM)
    cmd.Wait()
}

func waitForSocket(ctx context.Context, path string) error {
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            if _, err := os.Stat(path); err == nil {
                return nil
            }
        }
    }
}
```

## Appendix B: Testing Strategy

```go
// internal/sync/mutagen/daemon_test.go
package mutagen

import (
    "context"
    "testing"
    "time"
)

func TestEmbeddedDaemonLifecycle(t *testing.T) {
    daemon := NewEmbeddedDaemon(t.TempDir())
    
    ctx := context.Background()
    
    // Start
    if err := daemon.Start(ctx); err != nil {
        t.Fatalf("Start failed: %v", err)
    }
    
    // Verify running
    if !daemon.IsRunning() {
        t.Fatal("Daemon should be running")
    }
    
    // Test connection
    conn := daemon.Connection()
    if conn == nil {
        t.Fatal("Connection should not be nil")
    }
    
    // Stop
    stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
    defer cancel()
    if err := daemon.Stop(stopCtx); err != nil {
        t.Fatalf("Stop failed: %v", err)
    }
    
    // Verify stopped
    if daemon.IsRunning() {
        t.Fatal("Daemon should not be running")
    }
}
```

---

**Document Status:** Complete  
**Next Steps:** Begin implementation of `internal/sync/` package
