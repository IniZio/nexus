# Secure Credential Handling Design

**Date:** 2026-04-12  
**Status:** Draft  
**Related:** Gondolin placeholder tokens, SSH agent forwarding

## Summary

Replace the current authbundle approach (real credentials inside guest) with a secure credential handling system using **placeholder tokens with host-side HTTP substitution** and **SSH agent forwarding over vsock**. This is a **hard-cutover** — the old authbundle mechanism will be removed entirely.

## Problem

The current `authbundle` package bundles real credential files (`.gitconfig`, `.git-credentials`, API tokens) into a base64 tar.gz and injects them into the guest workspace. This means:

- Real credentials are exposed inside the untrusted guest environment
- A compromised guest can exfiltrate all bundled credentials
- No per-credential access control or host allowlisting

## Solution

### Core Principles

1. **Real secrets never enter the guest** — only random placeholder tokens
2. **Host controls all credential usage** — substitution happens on the host side
3. **Per-credential host allowlists** — each secret has specific allowed destinations
4. **SSH keys stay on host** — agent forwarding over vsock, private keys never leave host

### Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              HOST (Nexus Daemon)                          │
│  ┌─────────────────┐  ┌──────────────────┐  ┌─────────────────────────────┐  │
│  │  Secret Vault   │  │  HTTP Interceptor│  │   SSH Agent Forwarder     │  │
│  │  (Placeholder   │  │  (Syscall trace/ │  │   (VSOCK bridge)          │  │
│  │   mapping)      │  │   packet rewrite)│  │                             │  │
│  └────────┬────────┘  └────────┬─────────┘  └─────────────┬───────────────┘  │
│           │                    │                          │                  │
│           │   NEXUS_SECRET_abc │   github_token_value     │  SSH_AUTH_SOCK   │
│           │   (fake)           │   (real, host-side)      │  (vsock)         │
│           ▼                    ▼                          ▼                  │
│  ┌─────────────────────────────────────────────────────────────────────┐  │
│  │                      WORKSPACE CONTAINER (Guest)                     │  │
│  │  ┌─────────────────┐  ┌──────────────────┐  ┌────────────────────┐ │  │
│  │  │ Env vars have   │  │ HTTP requests to │  │ git/ssh use agent  │ │  │
│  │  │ placeholders    │  │ github.com auto  │  │ socket, host signs │ │  │
│  │  │ only            │  │ get real token   │  │ each key request   │ │  │
│  │  └─────────────────┘  └──────────────────┘  └────────────────────┘ │  │
│  └─────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```

## Components

### 1. Secret Vault (`pkg/secrets/vault.go`)

```go
type Vault struct {
    placeholders map[string]*Placeholder // NEXUS_SECRET_xxx -> metadata
}

type Placeholder struct {
    Token     string            // Random 32-char string (e.g., "a7f3d2...")
    RealValue string            // Actual secret (only on host)
    Hosts     []string          // Allowed destination hosts (e.g., ["api.github.com"])
    Headers   []string          // Headers to substitute in (default: ["Authorization"])
}

func (v *Vault) Generate(name string, value string, hosts []string) string
func (v *Vault) Resolve(token string, destinationHost string) (string, error)
```

**Key behaviors:**
- Placeholders generated as `NEXUS_SECRET_<random>` where `<random>` is 32 alphanumeric chars
- Each placeholder has a host allowlist with glob support (`*.github.com`, `api.*.com`)
- Lookup fails if destination not in allowlist

### 2. HTTP Request Interceptor (`pkg/secrets/interceptor.go`)

For Firecracker VMs, we use the existing vsock connection plus syscall tracing via `ptrace` or `seccomp` to intercept guest network calls.

```go
type HTTPInterceptor struct {
    vault       *Vault
    guestCID    uint32
    vsockPort   uint32
}

func (i *HTTPInterceptor) Start() error
func (i *HTTPInterceptor) Stop() error
```

**Mechanism:**
1. Attach to guest process namespace via `/proc/<pid>/ns/net`
2. Use `netlink` socket monitoring or `ptrace` `connect` syscall interception
3. For each outbound HTTP connection:
   - Parse HTTP headers
   - Scan for placeholder patterns (`NEXUS_SECRET_`)
   - Check destination against placeholder's host allowlist
   - If allowed: substitute placeholder with real value
   - If not allowed: block request, log alert

**Header substitution:**
```
# Guest sends:
Authorization: Bearer NEXUS_SECRET_a7f3d2...
Host: api.github.com

# Host intercepts and rewrites:
Authorization: Bearer ghp_real_actual_token...
Host: api.github.com
```

**Query parameter substitution (opt-in per secret):**
```
# If secret has QueryParams: true
https://api.example.com/?token=NEXUS_SECRET_xxx
→ https://api.example.com/?token=real_token
```

### 3. SSH Agent Forwarder (`pkg/secrets/sshagent.go`)

SSH agent protocol forwarded over the existing vsock connection.

```go
type SSHAgentForwarder struct {
    vault       *Vault
    guestCID    uint32
    vsockPort   uint32  // Separate from agent vsock port
}

func (f *SSHAgentForwarder) Start() error
func (f *SSHAgentForwarder) Stop() error
```

**Mechanism:**
1. Host creates vsock listener on dedicated port (e.g., 10790)
2. Guest `SSH_AUTH_SOCK` env var points to vsock socket (via bind mount or env)
3. Guest SSH client connects to `SSH_AUTH_SOCK`
4. Host receives SSH agent protocol requests:
   - `SSH_AGENTC_REQUEST_IDENTITIES` — list available keys
   - `SSH_AGENTC_SIGN_REQUEST` — sign challenge with private key
5. Host performs signing operation (keys never leave host)
6. Host returns signature to guest

**Security:**
- Private keys remain in host's `~/.ssh/` directory
- Each signing request is logged
- Optional: prompt user for approval per-request or per-session

## Data Flows

### Git HTTPS Clone with Token

```
1. User: nexus workspace create --env GITHUB_TOKEN

2. Host vault generates:
   Placeholder: NEXUS_SECRET_a7f3d2e8b9c1...
   Real value:  ghp_abc123...
   Hosts:       ["github.com", "*.github.com"]

3. Guest env: GITHUB_TOKEN=NEXUS_SECRET_a7f3d2e8b9c1...

4. Guest runs: git clone https://$GITHUB_TOKEN@github.com/user/repo.git
   → git sends HTTPS request with placeholder in Authorization header

5. Host HTTP interceptor:
   - Sees destination: github.com (matches allowlist ✓)
   - Sees header: Authorization: Bearer NEXUS_SECRET_a7f3d2e8b9c1...
   - Substitutes: Authorization: Bearer ghp_abc123...
   - Forwards request

6. GitHub receives real token, guest never saw it
```

### Git SSH Clone with SSH Key

```
1. Host SSH agent forwarder starts on vsock port 10790

2. Guest env: SSH_AUTH_SOCK=vsock://2:10790 (or bind-mounted socket)

3. Guest runs: git clone git@github.com:user/repo.git

4. Guest SSH client connects to SSH_AUTH_SOCK (vsock)

5. Host receives SSH agent protocol:
   - REQUEST_IDENTITIES → Host lists available keys (fingerprints only)
   - SIGN_REQUEST → Host signs challenge with private key

6. Host signs, returns signature, private key never left host
```

### API Call with Bearer Token

```
1. User: nexus workspace create --env OPENAI_API_KEY

2. Host vault generates placeholder with hosts: ["api.openai.com"]

3. Guest env: OPENAI_API_KEY=NEXUS_SECRET_...

4. Guest script:
   curl -H "Authorization: Bearer $OPENAI_API_KEY" \
        https://api.openai.com/v1/chat/completions

5. Host HTTP interceptor substitutes token only for api.openai.com
   → Blocks if used for evil.com (substitution fails, request blocked)
```

## Security Properties

| Threat | Mitigation |
|--------|------------|
| Guest reads env vars | Gets only useless placeholders |
| Guest exfiltrates token | Placeholder only works for allowed hosts |
| Guest uses token for MITM | Request blocked, logged |
| Guest reads SSH key files | No keys in guest filesystem |
| Guest memory dumps agent | Only sees forwarded socket, no key material |
| Guest compromises host | Keys isolated in vault, need explicit approval |
| Guest enumerates valid hosts | Placeholders don't reveal allowlist |

## Changes Required

### Remove/Replace

1. **Remove `pkg/runtime/authbundle`** — entire package deleted
2. **Remove bootstrap authbundle injection** — `bootstrapGuestToolingAndAuth` no longer passes authbundle
3. **Remove credential file bundling** — no more reading `.git-credentials`, `.gitconfig`

### Add

1. **`pkg/secrets/vault.go`** — placeholder generation and resolution
2. **`pkg/secrets/interceptor.go`** — HTTP request interception and substitution
3. **`pkg/secrets/sshagent.go`** — SSH agent protocol over vsock
4. **`pkg/secrets/policy.go`** — per-credential host allowlists
5. **Guest agent changes** — proxy HTTP requests through vsock for interception

### Modify

1. **`pkg/runtime/firecracker/driver.go`** —
   - Start HTTP interceptor + SSH forwarder when creating workspace
   - Pass placeholder env vars (not real values) to guest
   
2. **Guest agent (`internal/agent` or similar)** —
   - HTTP proxy mode: forward all HTTP through vsock to host
   - Host substitutes placeholders before forwarding to destination

## Configuration

No configuration needed for hard-cutover. All credentials are handled securely by default.

Optional future enhancements:
- `NEXUS_SECRETS_STRICT_MODE` — block all outbound HTTP except through interceptor
- `NEXUS_SECRETS_LOG_LEVEL` — verbose logging of all credential usage

## Migration (Hard-Cutover)

1. Delete `pkg/runtime/authbundle/` directory entirely
2. Update all drivers to use new secrets vault
3. Update guest agent to proxy HTTP through vsock
4. Document breaking change: users must re-authenticate workspaces

## Testing Strategy

1. **Unit tests:**
   - Vault placeholder generation/resolution
   - Host allowlist matching
   - HTTP header substitution
   - SSH agent protocol message parsing

2. **Integration tests:**
   - Git clone over HTTPS with placeholder token
   - Git clone over SSH with agent forwarding
   - Blocked request when host not in allowlist
   - API call with substituted bearer token

3. **E2E tests:**
   - Full workspace create → git clone → push workflow
   - Verify no real credentials in guest filesystem
   - Verify placeholder substitution in HTTP traffic

## OAuth and Dynamic Credentials

The placeholder approach works for static tokens but fails for OAuth-based agents (Codex, Claude) that need to:
1. Initiate device flow and display user_code
2. Poll for tokens
3. Store and refresh tokens automatically
4. Handle single-use refresh token rotation (race conditions with multiple agents)

### Solution: Credential Vending Service

```go
// pkg/secrets/vending/service.go
type VendingService struct {
    vault      *Vault
    oauthBrokers map[string]OAuthBroker // provider -> broker
}

type OAuthBroker interface {
    // InitiateDeviceFlow starts OAuth device flow, returns user_code and verification URL
    InitiateDeviceFlow(ctx context.Context, scopes []string) (*DeviceFlowInit, error)
    
    // GetAccessToken returns short-lived access token (5-15 min TTL)
    // Handles refresh internally, guest never sees refresh_token
    GetAccessToken(ctx context.Context, workspaceID string) (*AccessToken, error)
    
    // Revoke invalidates all tokens for this workspace
    Revoke(ctx context.Context, workspaceID string) error
}

type AccessToken struct {
    Token     string    // Short-lived access token (ghu_..., gho_...)
    ExpiresAt time.Time // 5-15 minutes from now
    Scopes    []string  // Granted scopes
}
```

### OAuth Broker Implementation (Codex Example)

```go
// pkg/secrets/vending/codex_broker.go
type CodexBroker struct {
    clientID     string
    tokenStore   *TokenStore  // Encrypted storage for refresh tokens
    mu           sync.RWMutex // Serializes refresh operations
}

func (b *CodexBroker) GetAccessToken(ctx context.Context, workspaceID string) (*AccessToken, error) {
    // 1. Check if we have cached access token that's not expired
    if cached := b.getCached(workspaceID); cached != nil && !cached.isExpired() {
        return cached, nil
    }
    
    // 2. Serialize refresh to avoid race conditions
    // (Multiple agents sharing same OAuth session)
    b.mu.Lock()
    defer b.mu.Unlock()
    
    // 3. Double-check after acquiring lock
    if cached := b.getCached(workspaceID); cached != nil && !cached.isExpired() {
        return cached, nil
    }
    
    // 4. Perform refresh with stored refresh_token
    refreshToken, err := b.tokenStore.GetRefreshToken(workspaceID)
    if err != nil {
        return nil, fmt.Errorf("no refresh token available, need re-auth: %w", err)
    }
    
    newToken, err := b.refresh(refreshToken)
    if err != nil {
        return nil, fmt.Errorf("token refresh failed: %w", err)
    }
    
    // 5. Store new refresh_token (it's single-use, rotated)
    b.tokenStore.StoreRefreshToken(workspaceID, newToken.RefreshToken)
    
    // 6. Cache short-lived access token
    b.cacheToken(workspaceID, newToken.AccessToken, newToken.ExpiresIn)
    
    return &AccessToken{
        Token:     newToken.AccessToken,
        ExpiresAt: time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second),
        Scopes:    newToken.Scopes,
    }, nil
}
```

### Guest Integration

The guest doesn't run `codex login` directly. Instead:

1. **Host runs device flow** when workspace is created with `--oauth codex`
2. **Host displays user_code** to user via CLI/notification
3. **User authenticates** in browser
4. **Host stores refresh_token** securely (encrypted, outside sandbox)
5. **Guest has "fake" Codex endpoint** at `localhost:8091` (vending client proxy)
6. **Codex CLI configured** to talk to `localhost:8091` instead of real API

```go
// Guest agent runs local proxy that forwards to host via vsock
type VendingClient struct {
    vsockConn net.Conn
}

func (c *VendingClient) GetToken(ctx context.Context, provider string) (string, error) {
    // gRPC/JSON-RPC over vsock to host
    // Returns short-lived access token
}
```

### Data Flow: Codex CLI in Workspace

```
1. Workspace creation with OAuth:
   $ nexus workspace create --oauth codex
   
2. Host initiates device flow:
   → POST https://github.com/login/device/code
   ← {device_code: "...", user_code: "ABCD-1234", verification_uri: "https://github.com/login/device"}

3. Host displays to user:
   "Open https://github.com/login/device and enter code: ABCD-1234"

4. User authenticates in browser

5. Host polls and receives:
   {access_token: "ghu_...", refresh_token: "ghr_...", expires_in: 28800}

6. Host stores encrypted refresh_token, discards access_token

7. Guest workspace starts with:
   - CODEX_API_URL=http://localhost:8091 (vending proxy)
   - No ~/.config/codex/auth.json file
   
8. Guest runs: codex "fix this bug"
   → Codex CLI requests http://localhost:8091/token
   → Vending proxy forwards over vsock to host
   → Host returns fresh access_token (ghu_..., 10 min TTL)
   → Codex CLI uses token, makes real API calls
   
9. Token expires mid-operation:
   → Codex CLI requests new token from localhost:8091
   → Host returns fresh token (no refresh in guest)
   
10. Race condition avoided:
    → All refresh operations serialized on host
    → Single source of truth for refresh_token
```

### Supported Providers

Initial implementation targets:
- **GitHub** (ghu_ tokens for Codespaces, Copilot, CLI)
- **OpenAI** (Codex CLI)
- **Anthropic** (Claude CLI)
- **Generic OAuth 2.0** (configurable client_id/authorization_endpoint)

### Security Properties for OAuth

| Threat | Mitigation |
|--------|------------|
| Guest steals refresh_token | Never enters guest, stored encrypted on host |
| Guest exfiltrates access_token | Short TTL (5-15 min), limited blast radius |
| Multiple agents race on refresh | Host serializes all refresh operations |
| Guest forges token requests | Vsock connection authenticated per-workspace |
| User re-auth required | Only when refresh_token expires/revoked |

## Component Summary

| Component | Purpose | Location |
|-----------|---------|----------|
| `SecretVault` | Placeholder generation, static token storage | Host |
| `HTTPInterceptor` | Header substitution, host allowlist enforcement | Host |
| `SSHAgentForwarder` | SSH key signing over vsock | Host |
| `VendingService` | gRPC service for dynamic credentials | Host |
| `OAuthBroker` | Provider-specific OAuth flow handling | Host |
| `VendingClient` | Guest-side proxy to request tokens | Guest |

## Open Questions

1. Should we support the Gondolin-style `createHttpHooks` explicit API or transparent interception?
2. How to handle non-HTTP protocols (raw TCP, gRPC without HTTP/2)?
3. Should SSH agent support key filtering (only certain keys forwarded to certain guests)?
4. Should OAuth brokers support "just-in-time" consent (user approval per token request)?
5. How to handle token revocation across multiple workspaces (single user, multiple agents)?

## References

- Gondolin secrets handling: https://earendil-works.github.io/gondolin/secrets/
- Gondolin security design: https://earendil-works.github.io/gondolin/security/
- SSH agent protocol: https://tools.ietf.org/html/draft-miller-ssh-agent-02
- OAuth 2.0 Device Authorization Grant: https://tools.ietf.org/html/rfc8628
- Sandbox0 credential injection: https://sandbox0.ai/blog/2026-03/keep-api-keys-out-of-ai-agents
- Token broker pattern: https://github.com/openclaw/openclaw/issues/47908
