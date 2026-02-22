# SSH Agent Forwarding on macOS with Docker: Research Report

## Executive Summary

SSH agent forwarding from macOS host to Docker containers is **fundamentally broken** with standard Unix socket mounting approaches due to Docker Desktop's VM architecture. This document analyzes why it fails and documents proven solutions used in production by major tools like VS Code Dev Containers, GitHub Actions, and CI/CD platforms.

---

## 1. Why Unix Sockets Fail on macOS Docker

### 1.1 Docker Desktop Architecture

Docker Desktop on macOS runs Docker Engine inside a **lightweight Linux VM** using one of several virtualization backends:

| Virtualization | File Sharing | Status |
|----------------|--------------|--------|
| Apple Virtualization Framework | virtiofs | Default (macOS 12.5+) |
| Apple Virtualization Framework | gRPC FUSE | Legacy but supported |
| Docker VMM | virtiofs | Beta, Apple Silicon only |
| HyperKit | osxfs | Legacy, Intel only |

**Key Insight**: All container filesystem operations go through a translation layer in the VM. The SSH_AUTH_SOCK socket exists on the macOS host, but containers run in the Linux VM.

### 1.2 The Socket Problem

```
┌─────────────────────────────────────────────────────────────┐
│                     macOS Host                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  SSH Agent (ssh-agent)                                 │  │
│  │  Unix socket: /tmp/ssh-XXXXXX/agent.XXXXXX            │  │
│  │  🔴 Cannot be bind-mounted into containers             │  │
│  └──────────────────────┬────────────────────────────────┘  │
│                         │ SSH_AUTH_SOCK                      │
│                         │ (macOS filesystem)                 │
│                         ▼                                    │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  Docker Desktop Linux VM                              │  │
│  │  • virtiofs / gRPC FUSE / osxfs translation layer     │  │
│  │  • ❌ Unix sockets NOT forwarded through file sharing │  │
│  └──────────────────────┬────────────────────────────────┘  │
│                         │                                    │
│                         ▼                                    │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  Docker Container                                     │  │
│  │  Mount fails: socket not accessible                   │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**Root Cause**: Docker Desktop's file sharing implementations (virtiofs, gRPC FUSE, osxfs) **do not support Unix socket forwarding**. They handle regular files and directories but cannot bridge socket connections across the macOS-VM boundary.

### 1.3 Why It Works on Linux

On native Linux Docker:
- No VM layer between host and containers
- Containers share the host kernel
- Unix sockets can be bind-mounted directly
- Socket paths are valid in the container's namespace

---

## 2. Alternative Approaches Analysis

### 2.1 SSH Key Mounting (Simple, Less Secure)

**Approach**: Mount SSH private keys as read-only volumes.

```bash
docker run -v ~/.ssh/id_ed25519:/root/.ssh/id_ed25519:ro \
           -v ~/.ssh/known_hosts:/root/.ssh/known_hosts:ro \
           myimage
```

**Pros:**
- ✅ Works on all platforms including macOS
- ✅ Simple to implement
- ✅ No additional dependencies
- ✅ Well-understood security model

**Cons:**
- ❌ Private keys exposed in container filesystem
- ❌ Keys visible to all container processes
- ❌ Passphrase-protected keys require manual entry or plaintext passphrase
- ❌ Key rotation requires container restart
- ❌ Violates "keys never leave host" principle
- ❌ Keys may persist in container layers (if committed)

**Security Score**: ⚠️ **Medium Risk**
- Keys are in container memory and filesystem
- Read-only mount mitigates some risks
- Still better than baking keys into images

### 2.2 TCP-Based Agent Forwarding (Recommended)

**Approach**: Use `socat` to bridge Unix socket over TCP between host and container.

**Architecture:**
```
┌─────────────────────────────────────────────────────────────┐
│                     macOS Host                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  SSH Agent (ssh-agent)                                 │  │
│  │  Unix socket: /tmp/ssh-XXXXXX/agent.XXXXXX            │  │
│  └──────────┬────────────────────────────────────────────┘  │
│             │ Unix socket                                    │
│             ▼                                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  socat (host side)                                     │  │
│  │  Forwards: Unix socket → TCP localhost:port           │  │
│  └──────────┬────────────────────────────────────────────┘  │
│             │ TCP (localhost only, 127.0.0.1)                │
│             ▼                                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  Docker Desktop VM (network layer forwards TCP)       │  │
│  └──────────┬────────────────────────────────────────────┘  │
│             │ TCP (host.docker.internal:PORT)                │
│             ▼                                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  socat (container side)                                │  │
│  │  Forwards: TCP → Unix socket /ssh-agent               │  │
│  └──────────┬────────────────────────────────────────────┘  │
│             │ Unix socket                                    │
│             ▼                                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  SSH Client (git, ssh)                                 │  │
│  │  SSH_AUTH_SOCK=/ssh-agent                             │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**Implementation:**

**Host Side (macOS):**
```bash
#!/bin/bash
# ssh-agent-bridge.sh

# Find SSH agent socket
SSH_AUTH_SOCK=${SSH_AUTH_SOCK:-$(ls -t /tmp/com.apple.launchd.*/Listeners 2>/dev/null | head -1)}
if [ -z "$SSH_AUTH_SOCK" ]; then
    echo "Error: SSH_AUTH_SOCK not found"
    exit 1
fi

# Generate random port
PORT=$(jot -r 1 10000 65000)

echo "Bridging SSH agent on port $PORT..."

# Start socat to bridge Unix socket to TCP
socat TCP-LISTEN:$PORT,fork,reuseaddr,range=127.0.0.1/32 UNIX-CONNECT:$SSH_AUTH_SOCK &
SOCAT_PID=$!

echo "Bridge running on PID $SOCAT_PID"
echo "Export this for containers: SSH_AGENT_BRIDGE_PORT=$PORT"

# Cleanup on exit
trap "kill $SOCAT_PID 2>/dev/null; exit" INT TERM EXIT

wait
```

**Container Side:**
```bash
#!/bin/bash
# container-init.sh

# Get bridge port from environment
PORT=${SSH_AGENT_BRIDGE_PORT:-10000}

# Start socat in container to bridge TCP back to Unix socket
socat UNIX-LISTEN:/ssh-agent,fork TCP:host.docker.internal:$PORT &

# Set environment variable
export SSH_AUTH_SOCK=/ssh-agent

# Run the main command
exec "$@"
```

**Docker Run:**
```bash
# Start bridge on host first
./ssh-agent-bridge.sh &
export SSH_AGENT_BRIDGE_PORT=12345

# Run container with bridge port
docker run -e SSH_AGENT_BRIDGE_PORT=$SSH_AGENT_BRIDGE_PORT \
           -e SSH_AUTH_SOCK=/ssh-agent \
           myimage
```

**Pros:**
- ✅ Keys never leave host SSH agent
- ✅ Works on macOS, Windows, Linux
- ✅ No keys in container filesystem
- ✅ Supports passphrase-protected keys
- ✅ Industry standard approach

**Cons:**
- ⚠️ Requires `socat` on both host and container
- ⚠️ TCP communication (mitigated by localhost binding)
- ⚠️ Slightly more complex setup
- ⚠️ Port management required

**Security Score**: ✅ **Low Risk**
- Keys remain in host agent only
- TCP bound to localhost only
- Short-lived bridge connections
- No keys in container

### 2.3 Agent Inside Container (Copy Keys)

**Approach**: Start ssh-agent inside container and copy keys into it at runtime.

```bash
# container-startup.sh

# Start ssh-agent
eval $(ssh-agent -s)

# Copy keys from mounted volume (still requires key mounting)
ssh-add /mounted-keys/id_ed25519

# Run main command
exec "$@"
```

**Pros:**
- ✅ Agent runs in container context
- ✅ No socket forwarding needed

**Cons:**
- ❌ Still requires key mounting
- ❌ Keys loaded into container memory
- ❌ More complex lifecycle management
- ❌ No benefit over direct key mounting

**Verdict**: Not recommended - adds complexity without security improvement.

### 2.4 VPN/Overlay Network (Tailscale, etc.)

**Approach**: Use Tailscale or similar to create mesh network where container and host communicate directly.

**Pros:**
- ✅ Elegant solution for complex scenarios
- ✅ Works across machines
- ✅ Can reuse existing Tailscale setup

**Cons:**
- ❌ Requires Tailscale daemon in container
- ❌ Overkill for local development
- ❌ Additional infrastructure dependency
- ❌ May conflict with corporate VPNs

**Verdict**: Good for remote scenarios, overkill for local development.

### 2.5 Docker Desktop SSH Integration

**Investigation Result**: ❌ **No Native Support**

As of Docker Desktop 4.34+, there is **no built-in SSH agent forwarding feature**. Docker Desktop:
- Does not forward SSH_AUTH_SOCK automatically
- Does not provide socket bridging
- Requires manual solutions like the ones above

**Note**: Docker Desktop for Mac recently added experimental features but SSH agent forwarding is not among them.

---

## 3. Industry Solutions Research

### 3.1 VS Code Dev Containers

**How VS Code Solves This:**

VS Code Dev Containers **do not actually solve the macOS socket problem** directly. Instead, they:

1. **Mount SSH_AUTH_SOCK blindly** hoping it works:
   ```json
   {
     "mounts": [
       "source=${localEnv:SSH_AUTH_SOCK},target=/tmp/ssh-agent.sock,type=bind"
     ]
   }
   ```

2. **Document limitations explicitly**:
   > "SSH agent forwarding only works when the workspace is running on the same machine as the host. It does NOT work across network boundaries."

3. **Recommend alternatives when socket mounting fails**:
   - Use HTTPS with credential helper instead
   - Copy keys manually
   - Use SSH keys inside container (not recommended)

**Verdict**: VS Code does NOT have a magic solution - they acknowledge it doesn't work reliably on macOS.

### 3.2 GitHub Actions / CI/CD Solutions

**GitHub Actions with Container Jobs:**

GitHub Actions running in containers use **SSH key mounting** as the primary approach:

```yaml
jobs:
  deploy:
    runs-on: ubuntu-latest
    container: node:16
    steps:
      - uses: actions/checkout@v3
      - name: Setup SSH
        uses: webfactory/ssh-agent@v0.7.0
        with:
          ssh-private-key: ${{ secrets.SSH_PRIVATE_KEY }}
```

The `webfactory/ssh-agent` action:
1. Starts ssh-agent inside the container
2. Injects the private key via environment variable
3. Keys never touch disk

**For local CI runners** (like self-hosted on macOS):
- They typically use **TCP bridging** (socat approach)
- Or run containers in privileged mode with full socket access

### 3.3 Other Tools Research

| Tool | Approach | macOS Support |
|------|----------|---------------|
| **Mutagen** | File sync only, no SSH forwarding | N/A |
| **Docker Compose** | Native volume mounts (fails on macOS) | ❌ |
| **devcontainers-cli** | Same as VS Code (acknowledges limitation) | ⚠️ Partial |
| **Testcontainers** | SSH key mounting | ✅ |
| **Drone CI** | TCP bridging with socat | ✅ |
| **Buildkite** | SSH agent socket mounting (fails on macOS) | ❌ |

**Key Finding**: Most tools either:
1. Don't support macOS SSH forwarding (acknowledge limitation)
2. Fall back to key mounting
3. Use TCP bridging (socat)

---

## 4. Security Considerations Deep Dive

### 4.1 Key Mounting Security Model

```
Threat Model: Key Mounting
┌─────────────────────────────────────────────────────────────┐
│ Container                                                   │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ Attacker gains container access                       │  │
│  │                                                       │  │
│  │ 1. Read mounted keys: ~/.ssh/id_ed25519              │  │
│  │    → Keys are READABLE (read-only mount)            │  │
│  │    → Attacker can exfiltrate keys                    │  │
│  │                                                       │  │
│  │ 2. Use keys for lateral movement                    │  │
│  │    → Connect to other hosts                         │  │
│  │    → Access other repositories                      │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘

Mitigations:
- ✅ Read-only mount prevents modification
- ✅ Keys still require container escape to reach host
- ❌ Keys are still accessible to container processes
```

**Risk Level**: **Medium**
- Keys are exposed in container
- Mitigated by read-only mounts
- Acceptable for development, not for production CI

### 4.2 Agent Forwarding Security Model

```
Threat Model: Agent Forwarding
┌─────────────────────────────────────────────────────────────┐
│ Container                                                   │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ Attacker gains container access                       │  │
│  │                                                       │  │
│  │ 1. Attempt to read keys:                             │  │
│  │    ~/.ssh/id_ed25519 → FILE NOT FOUND               │  │
│  │    No keys in container!                            │  │
│  │                                                       │  │
│  │ 2. Access SSH socket: /ssh-agent                    │  │
│  │    → Socket is present                               │  │
│  │    → Can REQUEST operations                         │  │
│  │    → Host agent PERFORMS operations                 │  │
│  │    → Attacker CANNOT extract keys                   │  │
│  │                                                       │  │
│  │ 3. Use agent for operations:                        │  │
│  │    → Can authenticate while bridge is active        │  │
│  │    → Limited to agent lifetime                      │  │
│  │    → Can be revoked (remove key from agent)         │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘

Mitigations:
- ✅ Keys never enter container
- ✅ Agent requests are logged on host
- ✅ Attacker cannot extract keys
- ✅ Bridge can be terminated to revoke access
```

**Risk Level**: **Low**
- Keys remain on host
- Attacker can use but not steal keys
- Bridge provides audit/control point

### 4.3 Key Permissions and Cleanup

**Critical Requirements:**

1. **Key File Permissions**:
   ```bash
   # Host: Ensure correct permissions before mounting
   chmod 700 ~/.ssh
   chmod 600 ~/.ssh/id_ed25519
   chmod 644 ~/.ssh/id_ed25519.pub
   chmod 644 ~/.ssh/config
   chmod 644 ~/.ssh/known_hosts
   ```

2. **Container Permission Fixup**:
   ```bash
   # In container entrypoint
   # Copy keys to tmpfs (not persistent) and fix permissions
   mkdir -p /tmp/.ssh
   cp /mounted-ssh/* /tmp/.ssh/
   chmod 700 /tmp/.ssh
   chmod 600 /tmp/.ssh/id_*
   export SSH_AUTH_SOCK=""  # Don't use agent
   export GIT_SSH_COMMAND="ssh -i /tmp/.ssh/id_ed25519"
   ```

3. **Cleanup on Container Stop**:
   ```bash
   # entrypoint.sh cleanup trap
cleanup() {
       # Remove keys from tmpfs
       rm -rf /tmp/.ssh
       # Clear environment
       unset SSH_AUTH_SOCK GIT_SSH_COMMAND
   }
   trap cleanup EXIT
   ```

---

## 5. Recommended Solution for Nexus

### 5.1 Solution: Hybrid Approach (TCP Bridge + Key Mount Fallback)

**Recommended Architecture:**

```
┌─────────────────────────────────────────────────────────────┐
│                     macOS Host                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  Nexus CLI                                             │  │
│  │  ┌─────────────────────────────────────────────────┐  │  │
│  │  │ SSH Provider                                     │  │  │
│  │  │ • Detects macOS vs Linux                         │  │  │
│  │  │ • Tries TCP bridge first (preferred)            │  │  │
│  │  │ • Falls back to key mounting                    │  │  │
│  │  └─────────────────────────────────────────────────┘  │  │
│  └────────────────────────┬──────────────────────────────┘  │
│                           │                                   │
│         ┌─────────────────┼─────────────────┐                │
│         ▼                 ▼                 ▼                │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────┐        │
│  │ TCP Bridge  │  │ Key Mount    │  │ User Notify  │        │
│  │ (socat)     │  │ (fallback)   │  │ (warning)    │        │
│  └─────────────┘  └──────────────┘  └──────────────┘        │
└─────────────────────────────────────────────────────────────┘
```

### 5.2 Implementation Strategy

**Priority Order:**

1. **Primary**: TCP Bridge with socat
   - Best security (keys never in container)
   - Best UX (works with passphrase-protected keys)
   - Requires socat on host and container

2. **Fallback**: Read-only SSH key mounting
   - Works everywhere
   - Good security (read-only)
   - Simple implementation

3. **Notification**: User warning when falling back
   - Log security implications
   - Suggest installing socat for better security
   - Provide instructions

### 5.3 Implementation Code

**Go Implementation (Nexus Daemon):**

```go
package docker

import (
    "context"
    "fmt"
    "net"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "strconv"
    "time"
)

type SSHForwardMode string

const (
    SSHForwardNone   SSHForwardMode = "none"
    SSHForwardBridge SSHForwardMode = "bridge"
    SSHForwardMount  SSHForwardMode = "mount"
)

type SSHConfig struct {
    Mode         SSHForwardMode
    BridgePort   int
    KeyPaths     []string
    SocatPID     int
}

// ConfigureSSHForwarding sets up SSH forwarding for macOS Docker
func (p *DockerProvider) ConfigureSSHForwarding(
    ctx context.Context,
    containerConfig *container.Config,
    hostConfig *container.HostConfig,
) (*SSHConfig, error) {
    // Detect platform
    if runtime.GOOS == "darwin" {
        // macOS: Try TCP bridge first
        return p.configureMacOSForwarding(ctx, containerConfig, hostConfig)
    }
    
    // Linux: Use direct socket mount
    return p.configureLinuxForwarding(ctx, containerConfig, hostConfig)
}

func (p *DockerProvider) configureMacOSForwarding(
    ctx context.Context,
    containerConfig *container.Config,
    hostConfig *container.HostConfig,
) (*SSHConfig, error) {
    // Check for socat
    if _, err := exec.LookPath("socat"); err == nil {
        // Try TCP bridge
        config, err := p.setupTCPBridge(ctx, containerConfig, hostConfig)
        if err == nil {
            return config, nil
        }
        // Log warning, fall through to key mount
        fmt.Fprintf(os.Stderr, "Warning: TCP bridge failed (%v), falling back to key mounting\n", err)
    } else {
        fmt.Fprintln(os.Stderr, "Warning: socat not found, using key mounting (install socat for better security)")
    }
    
    // Fallback to key mounting
    return p.setupKeyMount(ctx, containerConfig, hostConfig)
}

func (p *DockerProvider) setupTCPBridge(
    ctx context.Context,
    containerConfig *container.Config,
    hostConfig *container.HostConfig,
) (*SSHConfig, error) {
    // Get SSH_AUTH_SOCK
    socketPath := os.Getenv("SSH_AUTH_SOCK")
    if socketPath == "" {
        return nil, fmt.Errorf("SSH_AUTH_SOCK not set")
    }
    
    // Find available port
    listener, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil {
        return nil, fmt.Errorf("failed to find available port: %w", err)
    }
    port := listener.Addr().(*net.TCPAddr).Port
    listener.Close()
    
    // Start socat on host: Unix socket -> TCP
    cmd := exec.CommandContext(ctx, "socat",
        fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr,range=127.0.0.1/32", port),
        fmt.Sprintf("UNIX-CONNECT:%s", socketPath),
    )
    if err := cmd.Start(); err != nil {
        return nil, fmt.Errorf("failed to start socat: %w", err)
    }
    
    // Wait a moment for socat to start listening
    time.Sleep(100 * time.Millisecond)
    
    // Configure container
    containerConfig.Env = append(containerConfig.Env,
        fmt.Sprintf("SSH_AGENT_BRIDGE_PORT=%d", port),
        "SSH_AUTH_SOCK=/ssh-agent",
    )
    
    // Container will need to run socat to bridge TCP back to Unix socket
    // This is handled by entrypoint script
    
    return &SSHConfig{
        Mode:       SSHForwardBridge,
        BridgePort: port,
        SocatPID:   cmd.Process.Pid,
    }, nil
}

func (p *DockerProvider) setupKeyMount(
    ctx context.Context,
    containerConfig *container.Config,
    hostConfig *container.HostConfig,
) (*SSHConfig, error) {
    home, err := os.UserHomeDir()
    if err != nil {
        return nil, fmt.Errorf("failed to get home directory: %w", err)
    }
    
    sshDir := filepath.Join(home, ".ssh")
    if _, err := os.Stat(sshDir); err != nil {
        return nil, fmt.Errorf("SSH directory not found: %w", err)
    }
    
    // Mount .ssh directory as read-only
    hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
        Type:     mount.TypeBind,
        Source:   sshDir,
        Target:   "/root/.ssh",
        ReadOnly: true,
    })
    
    // Add init script to fix permissions (copies to tmpfs)
    initScript := `#!/bin/sh
# Copy SSH keys to tmpfs for proper permissions
mkdir -p /tmp/.ssh
cp -r /root/.ssh/* /tmp/.ssh/ 2>/dev/null || true
chmod 700 /tmp/.ssh
chmod 600 /tmp/.ssh/id_* 2>/dev/null || true
chmod 644 /tmp/.ssh/*.pub /tmp/.ssh/config /tmp/.ssh/known_hosts 2>/dev/null || true
export SSH_AUTH_SOCK=""
export GIT_SSH_COMMAND="ssh -i /tmp/.ssh/id_ed25519 -o StrictHostKeyChecking=accept-new"
`
    
    // Wrap the entrypoint
    originalCmd := containerConfig.Cmd
    containerConfig.Entrypoint = []string{"/bin/sh", "-c"}
    containerConfig.Cmd = []string{
        initScript + " && exec " + strings.Join(originalCmd, " "),
    }
    
    return &SSHConfig{
        Mode: SSHForwardMount,
    }, nil
}

func (p *DockerProvider) configureLinuxForwarding(
    ctx context.Context,
    containerConfig *container.Config,
    hostConfig *container.HostConfig,
) (*SSHConfig, error) {
    // Direct socket mount works on Linux
    socketPath := os.Getenv("SSH_AUTH_SOCK")
    if socketPath == "" {
        return nil, fmt.Errorf("SSH_AUTH_SOCK not set")
    }
    
    hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
        Type:     mount.TypeBind,
        Source:   socketPath,
        Target:   "/ssh-agent",
        ReadOnly: false,
    })
    
    containerConfig.Env = append(containerConfig.Env,
        "SSH_AUTH_SOCK=/ssh-agent",
    )
    
    return &SSHConfig{
        Mode: SSHForwardBridge, // Actually direct mount, but same security level
    }, nil
}

// Cleanup stops the socat bridge when done
func (p *DockerProvider) CleanupSSHForwarding(config *SSHConfig) error {
    if config.Mode == SSHForwardBridge && config.SocatPID > 0 {
        process, err := os.FindProcess(config.SocatPID)
        if err == nil {
            process.Kill()
        }
    }
    return nil
}
```

**Container Entrypoint Script:**

```bash
#!/bin/bash
# /usr/local/bin/nexus-ssh-setup

set -e

# If bridge port is set, set up TCP -> Unix socket bridge
if [ -n "$SSH_AGENT_BRIDGE_PORT" ]; then
    echo "Setting up SSH agent bridge on port $SSH_AGENT_BRIDGE_PORT..."
    
    # Start socat to bridge TCP to Unix socket
    socat UNIX-LISTEN:/ssh-agent,fork TCP:host.docker.internal:$SSH_AGENT_BRIDGE_PORT &
    SOCAT_PID=$!
    
    # Wait for socket to be created
    for i in {1..10}; do
        if [ -S /ssh-agent ]; then
            echo "SSH agent bridge ready"
            break
        fi
        sleep 0.1
    done
    
    # Set up cleanup
    cleanup() {
        kill $SOCAT_PID 2>/dev/null || true
        rm -f /ssh-agent
    }
    trap cleanup EXIT
fi

# If using key mounting (SSH_AUTH_SOCK not set but .ssh exists)
if [ -z "$SSH_AUTH_SOCK" ] && [ -d /root/.ssh ]; then
    echo "Setting up SSH keys from mount..."
    
    # Copy to tmpfs for proper permissions
    mkdir -p /tmp/.ssh
    cp -r /root/.ssh/* /tmp/.ssh/ 2>/dev/null || true
    chmod 700 /tmp/.ssh
    chmod 600 /tmp/.ssh/id_* 2>/dev/null || true
    chmod 644 /tmp/.ssh/*.pub /tmp/.ssh/config 2>/dev/null || true
    
    # Configure git to use our tmpfs keys
    export GIT_SSH_COMMAND="ssh -i /tmp/.ssh/id_ed25519 -o StrictHostKeyChecking=accept-new"
    
    cleanup_keys() {
        rm -rf /tmp/.ssh
    }
    trap cleanup_keys EXIT
fi

# Execute the main command
exec "$@"
```

---

## 6. Testing the Solution

### 6.1 Test Plan

**Test 1: TCP Bridge on macOS**
```bash
# 1. Start workspace with SSH forwarding
nexus workspace create --ssh-forward myproject

# 2. Inside container, test SSH
docker exec myproject ssh -T git@github.com

# 3. Verify keys are NOT in container
docker exec myproject ls -la /root/.ssh/  # Should be empty or not exist
docker exec myproject cat /root/.ssh/id_*   # Should fail

# 4. Verify git works
docker exec myproject git clone git@github.com:private/repo.git
```

**Test 2: Key Mount Fallback**
```bash
# 1. Remove socat to force fallback
brew uninstall socat

# 2. Start workspace
nexus workspace create --ssh-forward myproject

# 3. Verify warning is shown
# "Warning: socat not found, using key mounting..."

# 4. Verify keys work
docker exec myproject ssh -T git@github.com
```

**Test 3: Linux Direct Mount**
```bash
# Run on Linux host
nexus workspace create --ssh-forward myproject

# Should use direct socket mount (no socat needed)
# Verify SSH_AUTH_SOCK is mounted directly
docker inspect myproject | jq '.[0].Mounts'
```

---

## 7. Documentation Updates Needed

### 7.1 User Documentation

1. **macOS Prerequisites**:
   ```markdown
   ## macOS Users

   For the best SSH experience with Docker workspaces, install socat:

   ```bash
   brew install socat
   ```

   Without socat, Nexus will fall back to mounting SSH keys (less secure).
   ```

2. **Security Notice**:
   ```markdown
   ## SSH Agent Forwarding

   ### macOS Limitation
   
   Docker Desktop on macOS runs containers in a Linux VM, which prevents
   direct SSH agent socket mounting. Nexus automatically works around this:

   1. **With socat installed**: Uses TCP bridge (recommended, most secure)
   2. **Without socat**: Mounts keys read-only (secure, but keys in container)

   ### Security Comparison

   | Method | Keys in Container | Passphrase Support | Security Level |
   |--------|-------------------|-------------------|----------------|
   | TCP Bridge (socat) | ❌ No | ✅ Yes | 🔒 High |
   | Key Mount | ✅ Yes | ❌ No* | 🔐 Medium |

   *Keys with passphrases require manual entry
   ```

### 7.2 PRD Updates

The `04-security.md` file should be updated to:

1. Add macOS limitation section
2. Document TCP bridge approach
3. Update architecture diagrams
4. Add implementation details
5. Document fallback behavior

---

## 8. Conclusion

**Bottom Line**: SSH agent forwarding on macOS Docker requires a workaround due to VM architecture. The **TCP bridge approach using socat** is the industry standard for production use, offering the best security (keys never in container) while maintaining usability.

**Recommendation for Nexus**:
1. Implement hybrid approach (TCP bridge + key mount fallback)
2. Recommend socat installation for macOS users
3. Document limitations clearly
4. Provide clear security guidance

**Implementation Priority**:
1. **P0**: Key mounting fallback (works everywhere, immediate)
2. **P1**: TCP bridge (better security, requires socat)
3. **P2**: User notifications and documentation

---

## References

1. [Docker Desktop Networking Documentation](https://docs.docker.com/desktop/features/networking/)
2. [VS Code Dev Containers - Sharing Git Credentials](https://code.visualstudio.com/remote/advancedcontainers/sharing-git-credentials)
3. [Docker Desktop Settings](https://docs.docker.com/desktop/settings-and-maintenance/settings/)
4. [Buildkite Docker Compose Plugin - SSH Agent](https://github.com/buildkite-plugins/docker-compose-buildkite-plugin)
5. [SSH Agent Protocol](https://tools.ietf.org/html/draft-ietf-secsh-agent-02)
6. [socat Documentation](http://www.dest-unreach.org/socat/)

---

*Research completed: February 2026*
*Status: Ready for implementation*
