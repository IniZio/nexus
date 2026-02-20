# Nexus Agent Plugin - Technical Research Report

**Date:** 2026-02-20  
**Status:** Research Complete  
**Scope:** Agent Proxy, OCI Snapshots, E2E Testing, Auth Forwarding

---

## Executive Summary

This research evaluates technical approaches for the Nexus Agent Plugin's key challenges:

1. **Agent Proxy:** WebSocket-based reverse tunnel (frp/wstunnel pattern) is most reliable
2. **Snapshots:** OCI with umoci provides portable, layered workspace state
3. **E2E Testing:** Testcontainers for integration, Docker Compose for E2E
4. **Auth Forwarding:** Hybrid approach - 1Password for dev, Vault for prod, mTLS for services

---

## 1. Agent Proxy Architecture

### 1.1 Problem Statement

Users have agents (OpenCode, Claude, Cursor) installed locally with:
- Configuration files (`~/.opencode/config.json`)
- Authentication tokens (API keys, service accounts)
- Custom tools and plugins

When workspaces run remotely, these agents must be accessible without requiring users to reinstall/reconfigure in each workspace.

### 1.2 Solution: Agent Proxy Pattern

**Architecture:**
```
Local Machine (Node Layer)          Remote Workspace
┌─────────────────────────┐        ┌─────────────────────────┐
│ OpenCode/Claude/Cursor  │        │ Nexus Agent Stub        │
│ (Full installation)     │        │ (Minimal forwarder)     │
│                         │        │                         │
│ ┌─────────────────────┐ │        │ ┌─────────────────────┐ │
│ │ Config Files        │ │        │ │ Proxied Tools       │ │
│ │ Auth Tokens         │ │◄──────►│ │ Proxied Auth        │ │
│ │ Custom Tools        │ │ WebSocket/gRPC/mTLS   │
│ └─────────────────────┘ │        │ └─────────────────────┘ │
└───────────┬─────────────┘        └───────────┬─────────────┘
            │                                  │
            │    Secure Tunnel                 │
            │    (Port 443 - Firewall Friendly)│
            ▼                                  ▼
┌─────────────────────────────────────────────────────────────┐
│                 Coordination Layer                          │
│          (Connection management, routing)                   │
└─────────────────────────────────────────────────────────────┘
```

### 1.3 Protocol Comparison

| Protocol | Firewall | Latency | Complexity | Best For |
|----------|----------|---------|------------|----------|
| **WebSocket** | ✅ Port 80/443 | Low | Medium | **General use** |
| **gRPC** | ⚠️ Often blocked | Very Low | High | Internal networks |
| **SSH Reverse** | ❌ Port 22 blocked | Low | Low | SSH-friendly networks |
| **Raw TCP** | ❌ Easily blocked | Lowest | Low | Controlled networks |

**Recommendation:** WebSocket over TLS (wss://) on port 443

### 1.4 Existing Tools Analysis

| Tool | Stars | Language | Protocol | Best For |
|------|-------|----------|----------|----------|
| **frp** | 105k | Go | TCP/UDP/WS/gRPC | Production, feature-rich |
| **chisel** | 15.6k | Go | SSH over HTTP | Security-focused |
| **wstunnel** | 6.4k | Rust | WebSocket | Firewalls, simple |
| **bore** | 10.8k | Rust | TCP | Minimal, learning |

**Recommendation:** 
- **Phase 1:** Use `wstunnel` or `frp` for quick validation
- **Phase 2:** Build custom lightweight proxy using Go's `gorilla/websocket` + `yamux`

### 1.5 Cursor Remote Support

**Finding:** Cursor uses SSH-based remote development with custom protocol extensions.

**Implication:** We cannot directly leverage Cursor's approach (closed protocol), but we can provide similar UX through our Agent Proxy.

### 1.6 Claude Code Compatibility

**Finding:** Claude Code does NOT natively support remote workspaces.

**Workaround Required:**
- Option A: Mount remote filesystem locally (SSHFS, NFS)
- Option B: Proxy layer that presents remote as local

**Recommendation:** Option B (Agent Proxy) provides better UX.

### 1.7 Implementation Approach

**JSON-RPC over WebSocket Protocol:**
```typescript
// Request from remote workspace to local agent
interface AgentRequest {
  id: string;
  method: string;
  params: unknown;
}

// Methods exposed
interface AgentMethods {
  "tool.execute": (tool: string, args: unknown) => Promise<unknown>;
  "config.get": (key: string) => Promise<unknown>;
  "auth.getToken": (service: string) => Promise<string>;
  "file.read": (path: string) => Promise<string>;
}
```

---

## 2. Workspace Snapshots

### 2.1 Problem Statement

Need to capture and restore workspace state:
- Filesystem state
- Configuration
- Installed dependencies
- Not running processes (those are ephemeral)

### 2.2 Options Comparison

| Approach | Speed | Portability | Deduplication | Complexity |
|----------|-------|-------------|---------------|------------|
| **OCI (umoci)** | Medium | ✅ Universal | ✅ Layer-based | Medium |
| **LXC Snapshot** | Fast | ❌ LXC only | ⚠️ OverlayFS | Low |
| **Btrfs/ZFS** | Instant | ❌ FS-specific | ✅ Copy-on-write | Low |
| **Tar archives** | Slow | ✅ Universal | ❌ None | Low |

### 2.3 OCI Deep Dive

**OCI Image Layout:**
```
workspace-snapshot/
├── oci-layout          # Version marker
├── index.json          # Image index
├── blobs/
│   └── sha256/
│       ├── abc...      # Config blob (JSON)
│       ├── def...      # Layer 1 (tar.gz)
│       └── ghi...      # Layer 2 (tar.gz)
```

**Advantages:**
- Content-addressable (deduplication)
- Layer-based (incremental updates)
- Universal format (Docker, containerd, podman compatible)
- Registry support for distribution

**Tools:**
- **umoci** - Reference implementation, rootless, no daemon
- **skopeo** - Registry operations
- **buildah** - Building images

### 2.4 Single-Node Decision

**For single-node setup (Phase 1):**

Option A: **OCI + umoci** (Recommended)
- Pros: Future-proof, portable, registry-ready
- Cons: Slightly more complex than tar

Option B: **Btrfs snapshots** (If available)
- Pros: Instant, space-efficient
- Cons: Requires Btrfs, not portable

Option C: **Tar + simple versioning**
- Pros: Simplest
- Cons: No deduplication, full copies

**Recommendation:** Start with OCI + umoci for future-proofing.

### 2.5 Snapshot Workflow

```bash
# Create snapshot
umoci init --layout ./snapshots/base
umoci new --image ./snapshots/base:latest
umoci unpack --image ./snapshots/base:latest ./workspace-bundle
# ... work happens in workspace-bundle/rootfs ...
umoci repack --image ./snapshots/base:v2 ./workspace-bundle

# Distribute (optional)
skopeo copy oci:./snapshots/base:v2 docker://registry/nexus/workspace:v2

# Restore
umoci unpack --image ./snapshots/base:v2 ./restored-workspace
```

---

## 3. E2E Testing Strategy

### 3.1 Testing Pyramid

```
        ┌─────────────┐
        │    E2E      │  ← Full system, critical paths
        │  (Slower)   │    Docker Compose
        └──────┬──────┘
               │
        ┌──────┴──────┐
        │ Integration │  ← Service interactions
        │   (Medium)  │    Testcontainers
        └──────┬──────┘
               │
        ┌──────┴──────┐
        │    Unit     │  ← Individual components
        │   (Fast)    │    Standard testing
        └─────────────┘
```

### 3.2 Integration Testing with Testcontainers

**Go Example:**
```go
func TestAgentProxyIntegration(t *testing.T) {
    ctx := context.Background()
    
    // Start workspace container
    workspace, err := testcontainers.GenericContainer(ctx, 
        testcontainers.ContainerRequest{
            Image: "nexus-workspace:test",
            ExposedPorts: []string{"8080/tcp"},
            WaitingFor: wait.ForHTTP("/health"),
        })
    require.NoError(t, err)
    defer workspace.Terminate(ctx)
    
    // Start agent proxy
    proxy, err := testcontainers.GenericContainer(ctx,
        testcontainers.ContainerRequest{
            Image: "nexus-agent-proxy:test",
            Env: map[string]string{
                "WORKSPACE_ENDPOINT": getEndpoint(workspace),
            },
        })
    require.NoError(t, err)
    defer proxy.Terminate(ctx)
    
    // Test proxy connection
    client := createProxyClient(getEndpoint(proxy))
    err = client.ExecuteTool(ctx, "read", map[string]string{"path": "/test.txt"})
    require.NoError(t, err)
}
```

**Benefits:**
- Real containers, real network
- Automatic cleanup
- Parallel test execution
- 50+ pre-built modules

### 3.3 E2E Testing with Docker Compose

**docker-compose.test.yml:**
```yaml
version: '3.8'

services:
  # Mock agent (local side)
  mock-agent:
    build: ./test/mocks/agent
    volumes:
      - ./test/fixtures:/fixtures
    environment:
      - MOCK_CONFIG=/fixtures/opencode.json

  # Agent proxy
  agent-proxy:
    build: ./agent-proxy
    depends_on:
      - mock-agent
      - workspace
    environment:
      - AGENT_ENDPOINT=mock-agent:8080
      - WORKSPACE_ENDPOINT=workspace:8081

  # Remote workspace
  workspace:
    build: ./workspace
    volumes:
      - workspace-data:/data

  # Test runner
  e2e-tests:
    build: ./e2e
    depends_on:
      - agent-proxy
    volumes:
      - ./test-results:/results
    command: ["npm", "run", "test:e2e"]

volumes:
  workspace-data:
```

**Run:**
```bash
docker-compose -f docker-compose.test.yml up --abort-on-container-exit
```

### 3.4 GitHub Actions Setup

```yaml
name: E2E Tests
on: [push, pull_request]

jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.21'
      
      - name: Setup Node.js
        uses: actions/setup-node@v4
        with:
          node-version: '20'
      
      - name: Install dependencies
        run: |
          go mod download
          cd e2e && npm ci
      
      - name: Run integration tests
        run: go test -tags=integration ./...
      
      - name: Run E2E tests
        run: |
          docker-compose -f docker-compose.test.yml up --build --abort-on-container-exit
      
      - name: Upload test results
        uses: actions/upload-artifact@v4
        if: always()
        with:
          name: test-results
          path: test-results/
```

### 3.5 Test Coverage Goals

| Layer | Target Coverage | Tools |
|-------|-----------------|-------|
| Unit | 80%+ | Go testing, Jest |
| Integration | 70%+ | Testcontainers |
| E2E | Critical paths | Docker Compose |

---

## 4. Auth Forwarding

### 4.1 Problem Statement

Forward credentials from user's local machine to remote workspace without:
- Persisting secrets in workspace
- Exposing secrets in environment variables
- Manual configuration in each workspace

### 4.2 Threat Model

| Threat | Mitigation |
|--------|------------|
| Man-in-the-middle | mTLS encryption |
| Workspace compromise | Short-lived tokens |
| Token theft | Scoped tokens, rotation |
| Replay attacks | Nonce/timestamp validation |

### 4.3 Solutions Comparison

| Solution | Security | UX | Complexity | Best For |
|----------|----------|-----|------------|----------|
| **SSH Agent** | High | Good | Low | Git/SSH ops |
| **Vault Agent** | Very High | Medium | High | Enterprise |
| **1Password CLI** | High | Excellent | Low | Development |
| **mTLS** | Very High | Good | Medium | Service auth |
| **Token forwarding** | Medium | Good | Low | Simple cases |

### 4.4 Recommended Hybrid Approach

**Architecture:**
```
User's Machine
├── 1Password CLI (Primary secrets store)
│   └── Biometric unlock
├── SSH Agent (SSH keys for git)
└── Short-lived token generator

    │ Secure tunnel (mTLS)
    │ with token forwarding
    ▼

Remote Workspace
├── Token validator
├── Token-to-secret exchange
└── Workspace-local secrets (ephemeral)
```

**Token Flow:**
1. User authenticates with 1Password (biometric)
2. 1Password provides short-lived token (5 min TTL)
3. Token is forwarded through secure tunnel (mTLS)
4. Workspace validates token with coordination service
5. Workspace receives workspace-scoped secrets
6. Secrets expire on disconnect

### 4.5 Implementation: Token Forwarding

**Local (Node Layer):**
```typescript
// TokenManager.ts
class TokenManager {
  async getWorkspaceToken(workspaceId: string): Promise<Token> {
    // Get base token from 1Password
    const baseToken = await op.read(`op://vault/nexus/credential`);
    
    // Exchange for workspace-scoped token
    const response = await fetch(`${COORDINATION_URL}/token`, {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${baseToken}` },
      body: JSON.stringify({ workspaceId, ttl: 300 })
    });
    
    return response.json(); // { token: 'ws_...', expires: '...' }
  }
}
```

**Remote (Workspace):**
```go
// TokenValidator
func ValidateWorkspaceToken(ctx context.Context, token string) (*WorkspaceClaims, error) {
    // Verify JWT signature
    claims, err := jwt.Parse(token, keyFunc)
    if err != nil {
        return nil, fmt.Errorf("invalid token: %w", err)
    }
    
    // Check expiration
    if claims.ExpiresAt.Time.Before(time.Now()) {
        return nil, errors.New("token expired")
    }
    
    // Fetch workspace-scoped secrets
    secrets, err := vault.GetWorkspaceSecrets(claims.WorkspaceID)
    if err != nil {
        return nil, err
    }
    
    return &WorkspaceClaims{
        WorkspaceID: claims.WorkspaceID,
        Secrets: secrets,
    }, nil
}
```

### 4.6 Port Forwarding

**Dynamic Port Allocation:**
```go
// Local proxy exposes ports dynamically
func (p *Proxy) AllocatePort(service string) (int, error) {
    listener, err := net.Listen("tcp", "127.0.0.1:0") // OS assigns port
    if err != nil {
        return 0, err
    }
    
    port := listener.Addr().(*net.TCPAddr).Port
    p.ports[service] = port
    
    // Forward to remote
    go p.forward(listener, service)
    
    return port, nil
}

// Workspace connects to localhost:${port}
// Traffic forwarded to actual service on user's machine
```

---

## 5. Implementation Roadmap

### Phase 1: Foundation (Weeks 1-2)

**Goals:**
- Basic Agent Proxy with WebSocket
- OCI snapshot support (umoci)
- Single-node coordination

**Tasks:**
- [ ] Implement WebSocket proxy (frp or custom)
- [ ] Create workspace state serialization
- [ ] Integrate umoci for snapshots
- [ ] Basic enforcer with idle detection

### Phase 2: Auth & Testing (Weeks 3-4)

**Goals:**
- Secure auth forwarding
- Comprehensive E2E tests

**Tasks:**
- [ ] Implement token forwarding protocol
- [ ] mTLS between proxy and workspace
- [ ] 1Password CLI integration
- [ ] E2E test suite with Testcontainers

### Phase 3: Multi-Agent Support (Weeks 5-6)

**Goals:**
- Support OpenCode, Claude, Cursor adapters
- Remote workspace PoC

**Tasks:**
- [ ] OpenCode SDK adapter
- [ ] Claude Code adapter (via proxy)
- [ ] Cursor adapter
- [ ] Remote workspace on single node

### Phase 4: Production (Weeks 7-8)

**Goals:**
- Production-ready coordination
- Documentation

**Tasks:**
- [ ] Distributed coordination service
- [ ] Vault integration for secrets
- [ ] Performance optimization
- [ ] Complete documentation

---

## 6. Key Decisions

### 6.1 Agent Proxy: Custom vs Off-the-shelf

**Decision:** Start with **frp** for validation, build custom for production.

**Rationale:**
- frp is mature and feature-complete
- Custom implementation needed for Nexus-specific protocol
- Building custom teaches us the domain

### 6.2 Snapshots: OCI vs Simple

**Decision:** Use **OCI + umoci** from start.

**Rationale:**
- Future-proof for multi-node
- Industry standard
- Layer-based deduplication worth the complexity

### 6.3 Auth: Token Forwarding vs Vault

**Decision:** Start with **token forwarding**, add Vault later.

**Rationale:**
- Token forwarding simpler for MVP
- Vault adds infrastructure complexity
- Can add Vault as optional integration

---

## 7. Open Questions

1. **Offline Mode:** How should workspace behave when node loses connection?
2. **Multi-User:** Same workspace accessed by multiple users simultaneously?
3. **Rate Limiting:** Protect coordination service from abuse?

---

## 8. References

### Tools
- [frp](https://github.com/fatedier/frp) - Fast reverse proxy
- [wstunnel](https://github.com/erebe/wstunnel) - WebSocket tunnel
- [umoci](https://github.com/opencontainers/umoci) - OCI manipulation
- [testcontainers-go](https://github.com/testcontainers/testcontainers-go) - Integration testing

### Documentation
- [OCI Image Spec](https://github.com/opencontainers/image-spec)
- [WebSocket RFC 6455](https://tools.ietf.org/html/rfc6455)
- [SPIFFE/SPIRE](https://spiffe.io/) - mTLS identity framework

---

## Appendix: Quick Commands

```bash
# Test WebSocket tunnel
wstunnel client -L localhost:8080:localhost:8081 wss://remote:443

# Create OCI snapshot
umoci init --layout ./snap
umoci new --image ./snap:v1
umoci unpack --image ./snap:v1 ./bundle

# Run E2E tests
docker-compose -f docker-compose.test.yml up --abort-on-container-exit

# Test with testcontainers
go test -tags=integration ./...
```
