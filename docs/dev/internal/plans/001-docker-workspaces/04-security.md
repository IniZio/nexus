# 4. Security

## 4.1 SSH Key and Secret Handling

**Critical Design Issue:** Workspaces need SSH keys to clone private repositories, but containers don't have access to host SSH keys. Without proper handling, this creates a workflow downgrade from normal local development.

### 4.1.1 SSH Agent Forwarding (Preferred Method)

The most secure approach leverages the host's SSH agent to provide key access without copying keys into the container.

**Architecture:**

```
┌─────────────────────────────────────────────────────────────┐
│                         Host Machine                         │
│  ┌───────────────────────────────────────────────────────┐  │
│  │              SSH Agent (ssh-agent)                     │  │
│  │  • Private keys stored in memory only                  │  │
│  │  • Unix socket: /tmp/ssh-XXXXXX/agent.XXXXXX          │  │
│  │  • Keys never written to disk in container            │  │
│  └──────────────────────┬────────────────────────────────┘  │
│                         │ SSH_AUTH_SOCK                      │
│                         ▼                                    │
│  ┌───────────────────────────────────────────────────────┐  │
│  │           Docker Container (Workspace)                │  │
│  │  ┌───────────────────────────────────────────────┐   │  │
│  │  │  SSH Client (git, ssh)                         │   │  │
│  │  │  • Connects via mounted SSH_AUTH_SOCK         │   │  │
│  │  │  • No private keys in container               │   │  │
│  │  └───────────────────────────────────────────────┘   │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**Implementation:**

```go
func (p *DockerProvider) configureSSHAgentForwarding(
    ctx context.Context,
    containerConfig *container.Config,
    hostConfig *container.HostConfig,
) error {
    // 1. Detect SSH agent socket on host
    socketPath := os.Getenv("SSH_AUTH_SOCK")
    if socketPath == "" {
        socketPath = p.findSSHAgentSocket()
        if socketPath == "" {
            return fmt.Errorf("SSH agent not running. Start with: eval $(ssh-agent -s)")
        }
    }
    
    // 2. Mount socket into container
    hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
        Type:     mount.TypeBind,
        Source:   socketPath,
        Target:   "/tmp/ssh-agent.sock",
        ReadOnly: false,
    })
    
    // 3. Set environment variable in container
    containerConfig.Env = append(containerConfig.Env,
        "SSH_AUTH_SOCK=/tmp/ssh-agent.sock",
    )
    
    return nil
}
```

**Advantages:**
- ✅ Keys never leave the host
- ✅ No keys written to container layers or volumes
- ✅ Works with all SSH key types (RSA, Ed25519, ECDSA, FIDO/U2F)
- ✅ Supports passphrase-protected keys
- ✅ Automatic key rotation on host propagates to containers

**Requirements:**
- SSH agent must be running on host (`ssh-agent`)
- Keys must be added to agent (`ssh-add`)
- For macOS: May need to grant keychain access

### 4.1.2 SSH Key Mounting (Fallback Method)

When agent forwarding is not available, mount SSH keys as read-only volumes.

**Architecture:**

```
┌─────────────────────────────────────────────────────────────┐
│                         Host Machine                         │
│  ┌─────────────────┐                                       │
│  │  ~/.ssh/        │                                       │
│  │  ├── id_rsa     │                                       │
│  │  ├── id_ed25519 │                                       │
│  │  └── config     │                                       │
│  └────────┬────────┘                                       │
│           │ (read-only bind mount)                          │
│           ▼                                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │           Docker Container (Workspace)                │  │
│  │  ┌───────────────────────────────────────────────┐   │  │
│  │  │  ~/.ssh/ (mounted read-only)                  │   │  │
│  │  │  ├── id_rsa (mode 600)                        │   │  │
│  │  │  ├── id_ed25519                               │   │  │
│  │  │  └── config                                   │   │  │
│  │  └───────────────────────────────────────────────┘   │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**Implementation:**

```go
func (p *DockerProvider) configureSSHKeyMount(
    ctx context.Context,
    containerConfig *container.Config,
    hostConfig *container.HostConfig,
) error {
    home, _ := os.UserHomeDir()
    hostSSHDir := filepath.Join(home, ".ssh")
    
    // Mount entire .ssh directory as read-only
    hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
        Type:     mount.TypeBind,
        Source:   hostSSHDir,
        Target:   "/home/user/.ssh",
        ReadOnly: true,
    })
    
    // Add init script to fix permissions
    initScript := `#!/bin/sh
chmod 700 ~/.ssh
chmod 600 ~/.ssh/id_rsa ~/.ssh/id_ed25519 2>/dev/null || true
chmod 644 ~/.ssh/*.pub ~/.ssh/config 2>/dev/null || true
`
    containerConfig.Entrypoint = []string{"/bin/sh", "-c"}
    containerConfig.Cmd = []string{initScript + " && exec /bin/bash"}
    
    return nil
}
```

### 4.1.3 Git Credential Handling

For HTTPS-based git operations, forward credentials securely.

```go
// GitHub Token via Environment
func configureGitHubToken(containerConfig *container.Config, token string) {
    containerConfig.Env = append(containerConfig.Env,
        fmt.Sprintf("GITHUB_TOKEN=%s", token),
    )
    
    // Configure git to use token
    containerConfig.Cmd = append(containerConfig.Cmd,
        "git config --global url.\"https://oauth2:${GITHUB_TOKEN}@github.com/\".insteadOf \"https://github.com/\"",
    )
}
```

### 4.1.4 Configuration

```yaml
# ~/.nexus/config.yaml
secrets:
  # SSH authentication method
  ssh:
    mode: agent                   # agent | mount | auto
    
    # For mount mode: specific keys to mount (optional)
    # If omitted, mounts entire ~/.ssh directory
    paths:
      - ~/.ssh/id_ed25519_github
      - ~/.ssh/id_rsa_work
    
    # Include SSH config and known_hosts
    include_config: true
    include_known_hosts: true
    
    # Verify host keys
    strict_host_key_checking: yes  # yes | no | ask
    
  # Environment files to load
  env_files:
    - ~/.env
    - ~/.env.local
    
  # Named secrets from keychain
  named:
    NPM_TOKEN:
      source: keychain
      service: npm
      account: auth-token
      
    DATABASE_PASSWORD:
      source: file
      path: ~/.secrets/db-password.txt
      
    STRIPE_SECRET_KEY:
      source: env
      var: STRIPE_SECRET_KEY
```

### 4.1.5 Security Model

**Core Principles:**

1. **Default to Agent Forwarding**
   - SSH agent forwarding is the default and preferred method
   - Falls back to key mounting only when agent unavailable
   - User can explicitly override in configuration

2. **Keys Never Written to Container Layers**
   - All secrets mounted at runtime via bind mounts
   - Never baked into Docker images
   - Never committed to workspace state

3. **Read-Only Mounts Where Possible**
   - SSH keys: Read-only (except agent socket which requires RW)
   - Environment files: Read-only
   - Configuration: Read-only

4. **Minimal Secret Exposure**
   - Mount only required keys, not entire ~/.ssh directory when possible
   - Use selective key mounting for high-security environments
   - Support secret scoping (per-workspace secrets)

**Threat Model:**

```
Threat: Malicious container process steals SSH keys
├── Mitigation 1: Agent forwarding - keys never in container
├── Mitigation 2: Read-only mounts - prevents key exfiltration
├── Mitigation 3: Non-root container - limits access
└── Mitigation 4: Short-lived keys - rotate frequently

Threat: Container escape to host
├── Mitigation 1: User namespace remapping
├── Mitigation 2: Seccomp profiles
├── Mitigation 3: AppArmor/SELinux
└── Mitigation 4: Keys still protected by host permissions

Threat: Secrets in workspace snapshots
├── Mitigation 1: Secrets excluded from snapshots
├── Mitigation 2: Snapshots contain only references, not values
└── Mitigation 3: Encrypted at-rest if stored remotely
```

---

## 4.2 Authentication

### 4.2.1 Token-Based Authentication

```typescript
// JWT Token Structure
interface NexusToken {
  // Header
  alg: 'ES256';           // ECDSA with P-256
  typ: 'JWT';
  kid: string;            // Key ID for rotation
  
  // Payload
  sub: string;            // User ID
  workspace_id: string;   // Scoped to workspace
  permissions: string[];  // ['fs:read', 'fs:write', 'exec']
  
  // Time constraints
  iat: number;            // Issued at
  exp: number;            // Expiration (1 hour)
}
```

### 4.2.2 Permission System

```typescript
// Permission hierarchy
const PERMISSIONS = {
  // File system
  'fs:read': ['fs.readFile', 'fs.readdir', 'fs.stat'],
  'fs:write': ['fs.writeFile', 'fs.mkdir', 'fs.rm'],
  'fs:admin': ['fs:*'],
  
  // Execution
  'exec:read': ['exec.list', 'exec.logs'],
  'exec:write': ['exec.run', 'exec.kill'],
  
  // Workspace
  'workspace:read': ['workspace.get', 'workspace.list'],
  'workspace:write': ['workspace.create', 'workspace.update'],
  'workspace:admin': ['workspace:*'],
};

// Role definitions
const ROLES = {
  'developer': ['fs:*', 'exec:*', 'workspace:read'],
  'maintainer': ['fs:*', 'exec:*', 'workspace:*'],
  'viewer': ['fs:read', 'exec:read', 'workspace:read'],
  'agent': ['fs:read', 'fs:write', 'exec:write'],
};
```

---

## 4.3 Container Isolation

### 4.3.1 Docker Security Profile

```yaml
# Default security options for all containers
security_opts:
  # No new privileges
  - no-new-privileges:true
  
  # Seccomp profile
  - seccomp:./profiles/seccomp-default.json
  
  # AppArmor profile
  - apparmor:nexus-default
  
  # Capabilities
cap_drop:
  - ALL
cap_add:
  - CHOWN
  - DAC_OVERRIDE
  - FSETID
  - FOWNER
  - SETGID
  - SETUID
  - SETPCAP
  - NET_BIND_SERVICE
  
# Resource limits
resources:
  limits:
    cpus: '2.0'
    memory: 4G
    pids: 1000
  
# Network isolation
network_mode: bridge
networks:
  - nexus-workspace-net
  
# Filesystem
read_only_rootfs: true
tmpfs:
  - /tmp:noexec,nosuid,size=100m
  - /run:noexec,nosuid,size=100m
  
# User
user: "1000:1000"  # Non-root
```

### 4.3.2 Workspace Network Isolation

```
Network Architecture:

┌─────────────────────────────────────────────────────────────┐
│                         Host                                 │
│  ┌───────────────────────────────────────────────────────┐  │
│  │             Docker Network: nexus-isolated            │  │
│  │  (No external connectivity by default)               │  │
│  │                                                        │  │
│  │  ┌──────────────┐     ┌──────────────┐               │  │
│  │  │  Workspace A │     │  Workspace B │               │  │
│  │  │  (isolated)  │     │  (isolated)  │               │  │
│  │  └──────────────┘     └──────────────┘               │  │
│  │                                                        │  │
│  └───────────────────────────────────────────────────────┘  │
│                            │                                │
│                            ▼                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │           Docker Network: nexus-shared                │  │
│  │  (Controlled external access)                        │  │
│  │                                                        │  │
│  │  ┌─────────────────────────────────────────────────┐  │  │
│  │  │              Proxy Container                     │  │  │
│  │  │  - Outbound HTTPS only                          │  │  │
│  │  │  - Domain whitelist                             │  │  │
│  │  └─────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

---

## 4.4 Data Protection

### 4.4.1 Encryption

```yaml
# Encryption at rest
encryption:
  volumes:
    enabled: true
    algorithm: 'aes-256-gcm'
    
  state:
    enabled: true
    algorithm: 'aes-256-gcm'
    sensitive_fields: ['env_vars', 'volumes', 'backend_metadata']
    
  backups:
    enabled: true
    algorithm: 'aes-256-gcm'
    passphrase_required: true

# Encryption in transit
tls:
  min_version: 'TLSv1.3'
  cipher_suites:
    - 'TLS_AES_256_GCM_SHA384'
    - 'TLS_CHACHA20_POLY1305_SHA256'
```

### 4.4.2 Secret Management

```typescript
// Secret handling
interface SecretStore {
  // Supported backends
  backends: {
    'keychain': macOS Keychain / Windows Credential / Linux Keyring;
    'file': Encrypted file;
    'env': Environment variables (dev only);
    'vault': HashiCorp Vault (enterprise);
  };
}

// Usage - only references stored in config
const config = {
  env: {
    DATABASE_URL: { ref: 'secret://keychain/database-url' },
  },
};
```

---

## 4.5 Audit Logging

```typescript
interface AuditEvent {
  id: string;                    // UUID
  timestamp: ISO8601Timestamp;
  severity: 'info' | 'warning' | 'error' | 'critical';
  
  actor: {
    type: 'user' | 'agent' | 'system';
    id: string;
    ip?: string;
  };
  
  resource: {
    type: 'workspace' | 'file' | 'exec' | 'port';
    id: string;
    workspaceId?: string;
  };
  
  action: string;                // e.g., 'workspace.start'
  status: 'success' | 'failure' | 'denied';
  
  // Sanitized details (no passwords, tokens)
  details: Record<string, unknown>;
  
  // Retention
  retention: number;             // Days to retain
}

// Retention policy
const RETENTION_POLICIES = {
  'security_critical': 2555,     // 7 years
  'workspace_lifecycle': 365,    // 1 year
  'file_operations': 90,         // 90 days
};
```

---

## 4.6 Threat Model Summary

| Threat | Likelihood | Impact | Mitigation |
|--------|------------|--------|------------|
| **SSH key theft** | Low | Critical | Agent forwarding, read-only mounts |
| **Container escape** | Low | Critical | User namespaces, seccomp, AppArmor |
| **Unauthorized access** | Medium | High | JWT tokens, permission system |
| **Data exfiltration** | Low | High | Network isolation, audit logs |
| **Secret exposure** | Low | Critical | Keychain integration, no secrets in state |
