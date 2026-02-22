# 4. Security

## 4.1 SSH-Based Workspace Security

Nexus workspaces use **SSH as the primary access mechanism**, providing secure, standard-based access to containers. This section covers the SSH security model, key injection, and agent forwarding.

### 4.1.1 SSH Access Architecture

Nexus workspaces run an OpenSSH server in each container, with user access via SSH protocol:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        SSH Security Architecture                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  User Machine:                                                              │
│  ┌─────────────────┐                                                        │
│  │  SSH Client     │────── SSH Protocol ──────▶ Workspace Container        │
│  │  (any client)   │       (port 32801)                                     │
│  └────────┬────────┘                                                        │
│           │                                                                 │
│  ┌────────▼────────┐                                                        │
│  │  SSH Agent      │◀──── ForwardAgent ─────── Access to keys (via agent)  │
│  │  (host keys)    │                                                        │
│  └─────────────────┘                                                        │
│                                                                             │
│  Security Properties:                                                       │
│  ✅ Private keys NEVER leave host machine                                   │
│  ✅ Public keys injected into container authorized_keys                     │
│  ✅ Agent forwarding provides secure key access                             │
│  ✅ All SSH traffic encrypted                                               │
│  ✅ Per-workspace host keys (isolation)                                     │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 4.1.2 Key Injection (Primary Authentication)

Nexus automatically injects user SSH **public keys** into workspace containers for authentication:

**How it works:**

1. **Key Collection** (on workspace create):
   - Discover user's public keys (`~/.ssh/*.pub`)
   - Collect keys from SSH agent (`ssh-add -L`)
   - Use configured keys from `~/.nexus/config.yaml`

2. **Key Injection** (into container):
   - Write to `/home/nexus/.ssh/authorized_keys`
   - Set permissions: `600`, owner: `nexus:nexus`
   - Keys available immediately after container start

3. **SSH Access**:
   - User connects with private key (on host)
   - Container authenticates against authorized_keys
   - Optional: Agent forwarding for git operations

**Implementation:**

```go
// SSHKeyInjector manages key injection into containers
type SSHKeyInjector struct {
    keySources []string  // Paths to public keys
}

func (i *SSHKeyInjector) InjectKeys(workspaceID string) error {
    // 1. Collect public keys
    keys, err := i.collectPublicKeys()
    if err != nil {
        return err
    }
    
    // 2. Format authorized_keys
    authorizedKeys := formatAuthorizedKeys(keys)
    
    // 3. Write to container with secure permissions
    return i.writeToContainer(workspaceID, 
        "/home/nexus/.ssh/authorized_keys",
        authorizedKeys, 
        0600, 
        "nexus:nexus")
}

func (i *SSHKeyInjector) collectPublicKeys() ([]string, error) {
    var keys []string
    
    // Source 1: Filesystem keys
    pubKeyFiles, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".ssh/*.pub"))
    for _, f := range pubKeyFiles {
        content, _ := os.ReadFile(f)
        keys = append(keys, string(content))
    }
    
    // Source 2: SSH agent (public keys only)
    if agentKeys, err := i.getAgentKeys(); err == nil {
        keys = append(keys, agentKeys...)
    }
    
    // Source 3: Configured keys
    for _, path := range i.config.SSH.Injection.Sources {
        content, _ := os.ReadFile(path)
        keys = append(keys, string(content))
    }
    
    return keys, nil
}
```

**Security Properties:**

| Aspect | Implementation | Security Level |
|--------|---------------|----------------|
| **Private Keys** | Never leave host machine | 🔒 **Maximum** |
| **Public Keys** | Injected to authorized_keys | 🔒 **Maximum** |
| **Key Storage** | In-memory only (tmpfs on macOS) | 🔒 **High** |
| **Permissions** | 600, owned by nexus user | 🔒 **High** |
| **Key Rotation** | Automatic on host key change | 🔒 **High** |

### 4.1.3 SSH Agent Forwarding

For git operations requiring SSH authentication, Nexus supports agent forwarding:

**When to Use Agent Forwarding:**
- ✅ Git clone/push to private repositories
- ✅ SSH to other servers from within workspace
- ✅ Using passphrase-protected keys
- ✅ FIDO/U2F security keys (YubiKey, etc.)

**How It Works:**

```
┌─────────────────────────────────────────────────────────────┐
│                 SSH Agent Forwarding Flow                    │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Host Machine:                                              │
│  ┌──────────────────┐                                      │
│  │  SSH Agent       │                                      │
│  │  (ssh-agent)     │                                      │
│  │  • Keys in memory│                                      │
│  │  • Signs requests│                                      │
│  └────────┬─────────┘                                      │
│           │ Unix socket (SSH_AUTH_SOCK)                    │
│           │                                                 │
│           │◀── 1. SSH connection with -A flag              │
│           │                                                 │
│           │── 2. Request: "Sign this challenge"            │
│           │                                                 │
│           │◀── 3. Response: Signed challenge                │
│           │                                                 │
│  ┌────────▼─────────┐                                      │
│  │  SSH Client      │                                      │
│  │  (in container)  │                                      │
│  └────────┬─────────┘                                      │
│           │ 4. Use signed challenge                         │
│           ▼                                                 │
│  ┌──────────────────┐                                      │
│  │  Git/SSH Server  │                                      │
│  │  (github.com)    │                                      │
│  └──────────────────┘                                      │
│                                                             │
│  Note: Private keys NEVER leave the host. Only signed      │
│  challenges flow through the agent forwarding channel.      │
└─────────────────────────────────────────────────────────────┘
```

**Configuration:**

```yaml
# ~/.nexus/config.yaml
ssh:
  connection:
    forward_agent: true   # Enable by default
    
  # Additional SSH client options
  client_options:
    - "AddKeysToAgent=yes"
    - "IdentitiesOnly=yes"
```

**Agent Forwarding Security:**

| Risk | Mitigation |
|------|------------|
| Agent hijacking | Unix socket permissions (user-only) |
| Key extraction | Impossible - agent only signs, never exports |
| Unauthorized signing | Agent confirms user presence for FIDO keys |
| Session hijacking | Encrypted SSH channel |

### 4.1.4 Comparison: Key Injection vs Agent Forwarding

| Feature | Key Injection Only | Agent Forwarding |
|---------|-------------------|------------------|
| **Authentication** | Works without agent | Requires agent running |
| **Git operations** | ✅ Yes (direct key) | ✅ Yes (via agent) |
| **Passphrase keys** | ❌ No | ✅ Yes |
| **FIDO/U2F keys** | ❌ No | ✅ Yes |
| **Key rotation** | Requires recreation | Automatic |
| **Security** | High | Very High |
| **Startup time** | Slightly faster | Negligible |

**Recommendation:** Enable agent forwarding for development workstations. Use key injection only for CI/CD environments without agents.



### 4.1.5 Git Credential Handling

For HTTPS-based git operations, forward credentials securely via environment variables or credential helpers.

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

### 4.1.6 SSH Configuration

```yaml
# ~/.nexus/config.yaml
ssh:
  # Key injection configuration
  injection:
    enabled: true                 # Enable key injection
    sources:                      # Additional public key files
      - ~/.ssh/id_ed25519.pub
      - ~/.ssh/id_rsa.pub
      - ~/.ssh/custom_key.pub
    include_agent_keys: true      # Include keys from ssh-add -L
    
  # Connection settings
  connection:
    user: nexus                   # SSH user in container
    forward_agent: true           # Enable agent forwarding
    server_alive_interval: 30     # Keepalive seconds
    server_alive_count_max: 3     # Max missed keepalives
    
  # Security settings
  security:
    strict_host_key_checking: accept-new  # yes | no | accept-new | ask
    user_known_hosts_file: ~/.nexus/known_hosts
    identities_only: true         # Only use specified keys
    
  # SSH client options (passed to ssh command)
  client_options:
    - "AddKeysToAgent=yes"
    - "IdentitiesOnly=yes"
    - "BatchMode=no"

# Secrets configuration (non-SSH)
secrets:
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

### 4.1.5 SSH Security Model

**Core Principles:**

1. **Public Key Injection Only**
   - Only public keys are injected into containers (never private keys)
   - Private keys remain exclusively on the host machine
   - Keys injected at workspace creation, not during runtime

2. **SSH Agent for Private Key Operations**
   - Agent forwarding provides secure access to private keys
   - Keys never leave the host (agent only signs challenges)
   - Supports passphrase-protected and hardware keys (FIDO/U2F)

3. **Per-Workspace Isolation**
   - Each workspace has unique SSH host keys
   - Separate port allocation prevents cross-workspace access
   - authorized_keys scoped per workspace

4. **Defense in Depth**
   - SSH server runs as non-root user (nexus)
   - Password authentication disabled (keys only)
   - Host keys generated per workspace
   - Strict file permissions enforced

5. **Standard SSH Security**
   - All SSH traffic encrypted
   - Host key verification prevents MITM attacks
   - Standard SSH client/server hardening applied

**Threat Model:**

```
Threat: Malicious container process steals SSH keys
├── Mitigation 1: Public key injection only
│   └── Container only has public keys (useless to attacker)
├── Mitigation 2: Private keys never in container
│   └── Agent forwarding keeps keys on host
├── Mitigation 3: Authorized keys read-only
│   └── Cannot add new keys without recreating workspace
├── Mitigation 4: Non-root SSH user
│   └── Limited container access even if authenticated
└── Mitigation 5: Workspace isolation
    └── Per-workspace keys and ports prevent lateral movement

Threat: Container escape to host
├── Mitigation 1: User namespace remapping
├── Mitigation 2: Seccomp profiles
├── Mitigation 3: AppArmor/SELinux
├── Mitigation 4: Non-root container execution
└── Mitigation 5: Keys still protected by host permissions

Threat: SSH MITM attack
├── Mitigation 1: Per-workspace host keys
│   └── Each workspace has unique host key pair
├── Mitigation 2: Host key verification
│   └── Client verifies host key on first connect
├── Mitigation 3: Known hosts management
│   └── Workspace keys stored in ~/.nexus/known_hosts
└── Mitigation 4: Strict key checking
    └── Configurable: accept-new or strict verification

Threat: Unauthorized SSH access
├── Mitigation 1: Key-based auth only
│   └── Password authentication completely disabled
├── Mitigation 2: Authorized keys controlled by host
│   └── Only host-injected keys accepted
├── Mitigation 3: Localhost-only binding
│   └── SSH ports only on 127.0.0.1 (no external access)
└── Mitigation 4: Network isolation
    └── Per-workspace ports prevent scanning

Threat: Secrets in workspace snapshots
├── Mitigation 1: authorized_keys excluded from snapshots
├── Mitigation 2: Snapshots contain only workspace state
└── Mitigation 3: Host keys regenerated on restore
```

**Security Comparison by Platform:**

| Platform | Key Storage | Agent Support | Security Level |
|----------|-------------|---------------|----------------|
| Linux | Host only | Native | 🔒 **Maximum** |
| macOS | Host only | Native | 🔒 **Maximum** |
| Windows (WSL2) | Host only | Native | 🔒 **Maximum** |
| All platforms | Container has only public keys | Optional | 🔒 **Maximum** |

**Recommendations:**

1. **Development workstations**: Enable agent forwarding for convenience
2. **CI/CD environments**: Use key injection without agent (public keys only)
3. **High-security environments**: Audit authorized_keys, rotate frequently
4. **Shared machines**: Use agent forwarding with key confirmation (`ssh-add -c`)
5. **All environments**: Never copy private keys into containers

### 4.1.6 SSH Server Hardening

Nexus workspaces apply comprehensive SSH server hardening by default:

**sshd_config Settings:**

```bash
# Nexus Workspace SSH Hardening Configuration

# === Authentication ===
PermitRootLogin no                    # Disable root login
PasswordAuthentication no             # Keys only, no passwords
ChallengeResponseAuthentication no    # Disable PAM challenges
UsePAM no                             # Disable PAM entirely
AuthenticationMethods publickey       # Only public key auth

# === User Access ===
AllowUsers nexus                      # Only nexus user allowed
DenyUsers root                        # Explicit root deny

# === Agent Forwarding ===
AllowAgentForwarding yes              # Enable for convenience

# === Protocol Security ===
X11Forwarding no                      # Disable X11
PermitTunnel no                       # Disable tunneling
GatewayPorts no                       # No remote port forwarding

# === Timeouts ===
ClientAliveInterval 300               # 5 minute keepalive
ClientAliveCountMax 2                 # Disconnect after 2 missed
LoginGraceTime 60                     # 1 minute to authenticate
MaxAuthTries 3                        # Max 3 auth attempts
MaxSessions 10                        # Limit concurrent sessions

# === Cryptography ===
Ciphers chacha20-poly1305@openssh.com,aes256-gcm@openssh.com
MACs hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com
KexAlgorithms curve25519-sha256@libssh.org,ecdh-sha2-nistp521

# === Host Keys ===
HostKey /etc/ssh/ssh_host_ed25519_key
HostKey /etc/ssh/ssh_host_rsa_key

# === Logging ===
SyslogFacility AUTH
LogLevel VERBOSE

# === Environment ===
PermitUserEnvironment no              # Don't allow user env files
```

**Per-Workspace Host Keys:**

```go
// Host key generation on workspace creation
func generateHostKeys(workspaceID string) (*HostKeys, error) {
    keys := &HostKeys{
        WorkspaceID: workspaceID,
    }
    
    // Generate Ed25519 key (modern, secure)
    ed25519Key, err := generateKey("ed25519")
    if err != nil {
        return nil, err
    }
    keys.Ed25519Private = ed25519Key.Private
    keys.Ed25519Public = ed25519Key.Public
    
    // Generate RSA key (legacy compatibility)
    rsaKey, err := generateKey("rsa", 4096)
    if err != nil {
        return nil, err
    }
    keys.RSAPrivate = rsaKey.Private
    keys.RSAPublic = rsaKey.Public
    
    // Store securely
    return keys, storeHostKeys(keys)
}
```

**Host Key Storage:**

```
~/.nexus/workspaces/
└── feature-auth/
    └── ssh/
        ├── ssh_host_ed25519_key       # Private (encrypted at rest)
        ├── ssh_host_ed25519_key.pub   # Public
        ├── ssh_host_rsa_key           # Private (encrypted at rest)
        ├── ssh_host_rsa_key.pub       # Public
        └── fingerprint                # SHA256 fingerprint for display
```

**Security Benefits:**

| Setting | Purpose | Risk Mitigated |
|---------|---------|----------------|
| PermitRootLogin no | Disable root access | Privilege escalation |
| PasswordAuthentication no | Force key auth | Brute force attacks |
| Ciphers/MACs/Kex | Modern crypto | Downgrade attacks |
| ClientAliveInterval | Connection monitoring | Abandoned sessions |
| MaxAuthTries | Rate limiting | Brute force |
| Per-workspace keys | Isolation | Key reuse attacks |

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

## 4.5 File Sync Security

### 4.5.1 Sync Architecture Security

Mutagen file sync operates entirely over local Unix sockets or named pipes—**never over the network** for local Docker deployments.

```
┌─────────────────────────────────────────────────────────────┐
│                         Host                                 │
│  ┌─────────────────┐         ┌───────────────────────┐     │
│  │  Worktree       │         │  Mutagen Daemon       │     │
│  │  (.nexus/)      │◀───────▶│  (local socket only)  │     │
│  └─────────────────┘   Sync  └───────────┬───────────┘     │
│                                          │ Unix socket     │
│                                          ▼                  │
│  ┌───────────────────────────────────────────────────────┐  │
│  │              Docker Volume (nexus-sync)               │  │
│  │         (Kernel-level isolation)                      │  │
│  └───────────────────────────┬───────────────────────────┘  │
│                              │ Bind mount                   │
│                              ▼                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │              Workspace Container                      │  │
│  │              (files accessible)                       │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**Security Properties:**
- ✅ No network exposure (Unix sockets only)
- ✅ Kernel-enforced isolation (Docker volumes)
- ✅ No elevated privileges required
- ✅ No SSH keys or secrets in sync path
- ✅ Read-only sync to container is not supported (prevents container-initiated attacks)

### 4.5.2 Path Traversal Protection

Sync paths are validated to prevent directory traversal attacks:

```go
func validateSyncPath(hostPath, containerPath string) error {
    // Resolve to absolute paths
    hostAbs, err := filepath.Abs(hostPath)
    if err != nil {
        return err
    }
    
    // Ensure host path is within allowed directory
    worktreeRoot := "/Users/user/project/.worktree"
    if !strings.HasPrefix(hostAbs, worktreeRoot) {
        return fmt.Errorf("host path outside worktree root: %s", hostAbs)
    }
    
    // Container path is always /workspace (controlled)
    if containerPath != "/workspace" && !strings.HasPrefix(containerPath, "/workspace/") {
        return fmt.Errorf("invalid container path: %s", containerPath)
    }
    
    return nil
}
```

### 4.5.3 Excluded Sensitive Paths

By default, the following are excluded from sync:
- `.git/` - Prevents accidental git repo corruption
- `.ssh/` - Never sync SSH keys
- `.env*` - Environment files with secrets
- `.nexus/secrets/` - Nexus secrets directory
- `*.key`, `*.pem` - Key files

### 4.5.4 Sync Session Isolation

Each workspace has an isolated sync session:

```go
type SyncSession struct {
    ID        string    // UUID, not guessable
    Workspace string    // Associated workspace
    
    // Paths strictly controlled
    HostPath       string   // Verified within worktree
    ContainerPath  string   // Always /workspace
    
    // No cross-worktree access
    Isolated  bool      // true for all sessions
}
```

## 4.6 Audit Logging

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

## 4.7 Threat Model Summary

### SSH-Based Workspace Threats

| Threat | Likelihood | Impact | Mitigation |
|--------|------------|--------|------------|
| **SSH key theft** | Low | Critical | Public key injection only, agent forwarding |
| **Private key exposure** | Very Low | Critical | Private keys NEVER leave host |
| **Container escape** | Low | Critical | User namespaces, seccomp, AppArmor |
| **SSH MITM attack** | Low | High | Per-workspace host keys, strict verification |
| **Unauthorized SSH access** | Low | High | Key-only auth, localhost binding |
| **Data exfiltration** | Low | High | Network isolation, audit logs |
| **Secret exposure** | Low | Critical | No secrets in container images |
| **Host key compromise** | Low | Medium | Per-workspace keys, automatic rotation |

### General Workspace Threats

| Threat | Likelihood | Impact | Mitigation |
|--------|------------|--------|------------|
| **Unauthorized access** | Medium | High | JWT tokens, permission system |
| **Sync interception** | Very Low | Medium | Local-only sync (Mutagen over Unix socket) |
| **File traversal via sync** | Low | High | Path validation, chroot jail |
| **Snapshot data leakage** | Low | Medium | No secrets in snapshots, encryption |
