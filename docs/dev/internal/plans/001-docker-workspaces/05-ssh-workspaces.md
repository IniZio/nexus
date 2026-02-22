# 5. SSH-Based Workspaces

## 5.1 Overview

Nexus workspaces use **SSH as the primary access mechanism**, replacing `docker exec` for command execution, shell access, and port forwarding. Each workspace container runs an OpenSSH server, enabling native SSH workflows with full agent forwarding support.

### Why SSH?

| Feature | `docker exec` | SSH | Benefit |
|---------|--------------|-----|---------|
| **Agent Forwarding** | ❌ Broken on macOS | ✅ Works natively | SSH keys accessible in container |
| **IDE Support** | ❌ Limited | ✅ Universal | Any IDE with SSH support |
| **Familiar Workflow** | ❌ Custom | ✅ Standard SSH | No learning curve |
| **Port Forwarding** | ❌ Manual | ✅ `-L`/`-R` flags | Native tunneling |
| **File Transfer** | ❌ `docker cp` | ✅ `scp`/`rsync` | Standard tools |
| **Debugging** | ❌ Custom | ✅ Standard tools | `ssh -v`, `ssh-agent -l` |

### Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           SSH-Based Workspace Access                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                           User Machine                                 │  │
│  │                                                                        │  │
│  │  ┌─────────────────┐  ┌─────────────────┐  ┌───────────────────────┐  │  │
│  │  │  CLI (boulder)  │  │  IDE (VS Code)  │  │  ssh -A -p 32801 ...  │  │  │
│  │  │                 │  │                 │  │                       │  │  │
│  │  │  nexus ssh ws1  │  │  Remote-SSH     │  │  (native SSH client)  │  │  │
│  │  └────────┬────────┘  └────────┬────────┘  └───────────┬───────────┘  │  │
│  │           │                    │                       │               │  │
│  │           └────────────────────┼───────────────────────┘               │  │
│  │                                │                                       │  │
│  │                      ┌─────────▼──────────┐                           │  │
│  │                      │  SSH Agent         │                           │  │
│  │                      │  (keys in memory)  │                           │  │
│  │                      └─────────┬──────────┘                           │  │
│  │                                │ ForwardAgent yes                     │  │
│  └────────────────────────────────┼───────────────────────────────────────┘  │
│                                   │                                          │
│                                   │ SSH Protocol (port 32801)                │
│                                   ▼                                          │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                           Workspace Container                          │  │
│  │                                                                        │  │
│  │  ┌─────────────────────────────────────────────────────────────────┐  │  │
│  │  │  OpenSSH Server (sshd)                                          │  │  │
│  │  │  • Port 22 (mapped to host:32801)                               │  │  │
│  │  │  • Key-based auth only (no passwords)                           │  │  │
│  │  │  • Agent forwarding enabled                                     │  │  │
│  │  └────────────────────┬────────────────────────────────────────────┘  │  │
│  │                       │                                                │  │
│  │           ┌───────────┼───────────┐                                    │  │
│  │           ▼           ▼           ▼                                    │  │
│  │  ┌─────────────┐ ┌─────────┐ ┌─────────────┐                          │  │
│  │  │  ~/.ssh/    │ │  /work  │ │  /projects  │                          │  │
│  │  │  (mounted)  │ │         │ │             │                          │  │
│  │  │             │ │  Code   │ │  Services   │                          │  │
│  │  └─────────────┘ └─────────┘ └─────────────┘                          │  │
│  │                                                                        │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 5.2 User Experience

### Quick Start

```bash
# Create a workspace
boulder workspace create feature-auth

# SSH into workspace (automatic port allocation)
boulder ssh feature-auth

# Or use standard SSH with allocated port
ssh -A nexus@localhost -p 32801

# Execute single command
boulder ssh feature-auth -- npm test

# Port forwarding (automatic)
boulder ssh feature-auth -- -L 3000:localhost:3000
```

### IDE Integration

**VS Code:**
```bash
# VS Code automatically detects Nexus workspaces
boulder workspace up feature-auth
code --remote ssh-remote+nexus@localhost:32801 /work

# Or use Remote-SSH extension
# Add to ~/.ssh/config:
Host nexus-feature-auth
  HostName localhost
  Port 32801
  User nexus
  ForwardAgent yes
```

**Cursor:**
```bash
# Cursor with SSH support
cursor --remote ssh-remote+nexus@localhost:32801 /work
```

## 5.3 SSH Server Configuration

### Container Setup

Each workspace container includes OpenSSH server with security hardening:

```dockerfile
# Base image includes OpenSSH server
FROM nexus-workspace-base:latest

# Create nexus user (non-root)
RUN useradd -m -s /bin/bash -u 1000 nexus

# Setup SSH directory
RUN mkdir -p /home/nexus/.ssh && \
    chmod 700 /home/nexus/.ssh

# SSH server configuration
COPY sshd_config /etc/ssh/sshd_config

# Entrypoint starts SSH daemon
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
```

### SSH Hardening (`/etc/ssh/sshd_config`)

```bash
# Nexus Workspace SSH Configuration

# Authentication
PermitRootLogin no
PasswordAuthentication no
ChallengeResponseAuthentication no
UsePAM no
AuthenticationMethods publickey

# Allow only nexus user
AllowUsers nexus

# Agent forwarding
AllowAgentForwarding yes

# Security
X11Forwarding no
PrintMotd no
PrintLastLog no

# Timeouts
ClientAliveInterval 300
ClientAliveCountMax 2

# Logging
SyslogFacility AUTH
LogLevel VERBOSE

# Host keys (generated at runtime)
HostKey /etc/ssh/ssh_host_ed25519_key
HostKey /etc/ssh/ssh_host_rsa_key

# Use dedicated port (mapped to host)
Port 22
```

## 5.4 Port Allocation

### SSH Port Ranges

```
┌─────────────────────────────────────────────────────────────┐
│  Port Range Allocation (SSH-Enabled Workspaces)              │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  32768 - 32799  │  Reserved (system)                         │
│  32800 - 34999  │  Workspace SSH ports (Docker backend)      │
│                 │  - One SSH port per workspace              │
│                 │  - Auto-allocated from range               │
│                 │  - Deterministic: hash(name) + base        │
│  35000 - 39999  │  Service ports (web, api, db)              │
│  40000 - 65535  │  Dynamic allocation (fallback)             │
└─────────────────────────────────────────────────────────────┘
```

### Port Allocation Algorithm

```go
// Allocate SSH port for workspace
func (a *Allocator) AllocateSSHPort(workspace string) (int, error) {
    // Deterministic allocation based on workspace name
    base := hash(workspace) % 2200  // 0-2199
    port := 32800 + base
    
    // Check availability, increment if conflict
    for attempts := 0; attempts < 100; attempts++ {
        if !a.isPortInUse(port + attempts) {
            return port + attempts, nil
        }
    }
    
    return 0, fmt.Errorf("no available ports in range")
}
```

## 5.5 Key Injection

### SSH Key Management

Nexus automatically injects user SSH keys into workspace containers:

```go
// SSHKeyInjector manages authorized_keys in containers
type SSHKeyInjector struct {
    keyPaths []string  // User's SSH public keys
}

func (i *SSHKeyInjector) InjectKeys(workspaceID string) error {
    // 1. Collect user's public keys
    keys, err := i.collectPublicKeys()
    if err != nil {
        return err
    }
    
    // 2. Write to container's authorized_keys
    authorizedKeys := strings.Join(keys, "\n")
    
    // 3. Set proper permissions (nexus:nexus, 600)
    return i.writeToContainer(workspaceID, "/home/nexus/.ssh/authorized_keys", 
        authorizedKeys, 0600, "nexus:nexus")
}
```

### Key Sources

1. **Default keys**: `~/.ssh/id_*.pub`
2. **Configured keys**: From `~/.nexus/config.yaml`
3. **Agent keys**: `ssh-add -L` (if agent forwarding unavailable)

### Configuration

```yaml
# ~/.nexus/config.yaml
ssh:
  # Key injection method
  injection:
    enabled: true
    sources:
      - ~/.ssh/id_ed25519.pub
      - ~/.ssh/id_rsa.pub
    
    # Additional keys from SSH agent
    include_agent_keys: true
  
  # Port allocation
  port_range:
    start: 32800
    end: 34999
  
  # Connection settings
  connection:
    user: nexus
    forward_agent: true
    server_alive_interval: 30
```

## 5.6 SSH Proxy & Local Proxy

### Local Proxy for Seamless Access

Nexus provides a local SSH proxy for automatic workspace resolution:

```go
// SSHProxy provides transparent workspace access
type SSHProxy struct {
    workspaceManager *workspace.Manager
    portAllocator    *ports.Allocator
}

// Resolve workspace name to actual host:port
func (p *SSHProxy) Resolve(workspace string) (host string, port int, error) {
    ws, err := p.workspaceManager.Get(workspace)
    if err != nil {
        return "", 0, err
    }
    
    if ws.Status != "running" {
        return "", 0, fmt.Errorf("workspace %s is not running", workspace)
    }
    
    // Get allocated SSH port
    sshPort := ws.Ports["ssh"]
    return "localhost", sshPort, nil
}
```

### SSH Config Generation

```bash
# Auto-generate SSH config for all workspaces
boulder ssh-config generate >> ~/.ssh/config

# Generated config:
Host nexus-feature-auth
  HostName localhost
  Port 32801
  User nexus
  ForwardAgent yes
  StrictHostKeyChecking accept-new
  UserKnownHostsFile ~/.nexus/known_hosts

Host nexus-feature-ui
  HostName localhost
  Port 32802
  User nexus
  ForwardAgent yes
  StrictHostKeyChecking accept-new
```

## 5.7 Connection Flow

### Startup Sequence

```
User: boulder workspace create feature-auth
            │
            ▼
┌─────────────────────────┐
│   Create git worktree   │
│   (.worktrees/feature)  │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Allocate SSH port     │
│   (port: 32801)         │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Create container      │
│   - Map port 32801:22   │
│   - Mount worktree      │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Start SSH daemon      │
│   (sshd on port 22)     │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Inject SSH keys       │
│   (authorized_keys)     │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Ready for SSH         │
│   boulder ssh feature   │
└─────────────────────────┘
```

### Connection Establishment

```bash
# 1. User initiates connection
$ boulder ssh feature-auth

# 2. CLI resolves workspace
#    - Looks up workspace state
#    - Gets SSH port (32801)
#    - Checks workspace is running

# 3. Execute SSH command
$ ssh -A \
    -p 32801 \
    -o StrictHostKeyChecking=accept-new \
    -o UserKnownHostsFile=~/.nexus/known_hosts \
    nexus@localhost

# 4. SSH handshake
#    - Server presents host key
#    - Client authenticates with injected key
#    - Agent forwarding established

# 5. Session active
#    - User in /work directory
#    - SSH agent available ($SSH_AUTH_SOCK)
#    - Can git clone, push, etc.
```

## 5.8 Integration with Existing Components

### File Sync (Mutagen)

File sync continues to operate independently via Mutagen:

```
┌─────────────────────────────────────────────────────────────┐
│                    File Sync + SSH Access                    │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│   Host Worktree         Mutagen Session      Container      │
│   (.worktrees/feat)  ←────────────────→  /work              │
│        │                                        │           │
│        │         (Bidirectional sync)          │           │
│        │                                        │           │
│   User edits ──────────────────────────────→   Accessed    │
│                                                via SSH      │
│                                                             │
│   SSH Access: ───────────────────────────────────────┐      │
│   $ boulder ssh feature                              │      │
│                                                      │      │
│   # Edit files via SSH (changes sync back to host)   │      │
│   $ echo "test" >> /work/README.md                   │      │
│                                                      │      │
└─────────────────────────────────────────────────────────────┘
```

### IDE Integration

**VS Code Remote-SSH:**
```json
// .vscode/settings.json (in workspace)
{
  "remote.SSH.configFile": "~/.nexus/ssh_config",
  "remote.SSH.defaultExtensions": [
    "dbaeumer.vscode-eslint",
    "bradlc.vscode-tailwindcss"
  ]
}
```

**Cursor:**
```bash
# Cursor automatically detects Nexus workspaces
# when .nexus/config.yaml is present
```

## 5.9 Troubleshooting

### Connection Issues

```bash
# Test SSH connectivity
boulder ssh feature-auth -- -v

# Check workspace status
boulder workspace show feature-auth

# Verify SSH port allocation
boulder workspace ports feature-auth

# View SSH logs
boulder workspace logs feature-auth --service=sshd
```

### Common Problems

| Problem | Cause | Solution |
|---------|-------|----------|
| Connection refused | Workspace not running | `boulder workspace up feature-auth` |
| Permission denied | Keys not injected | `boulder workspace restart feature-auth` |
| Agent not available | Forwarding disabled | Check `ForwardAgent yes` in config |
| Unknown host key | First connection | Accept new host key (stored in ~/.nexus/known_hosts) |

### Debug Mode

```bash
# Enable verbose SSH logging
NEXUS_SSH_DEBUG=1 boulder ssh feature-auth

# Check injected keys
boulder ssh feature-auth -- "cat ~/.ssh/authorized_keys"

# Verify agent forwarding
boulder ssh feature-auth -- "ssh-add -l"
```

## 5.10 Security Considerations

### Host Key Management

```bash
# Host keys generated per-workspace at creation
# Stored in: ~/.nexus/workspaces/<name>/ssh/

~/.nexus/workspaces/feature-auth/ssh/
├── ssh_host_ed25519_key      # Private (never leaves container)
├── ssh_host_ed25519_key.pub  # Public (stored for verification)
├── ssh_host_rsa_key
└── ssh_host_rsa_key.pub

# Known hosts managed automatically
~/.nexus/known_hosts
```

### Key Injection Security

- Only public keys are injected (never private keys)
- `authorized_keys` file is 600, owned by nexus user
- Keys injected at workspace creation only
- Changes to host keys require workspace recreation

### Network Isolation

- SSH ports only bound to localhost (127.0.0.1)
- No external network exposure
- Per-workspace isolated SSH ports
- Firewall rules prevent cross-workspace SSH access
