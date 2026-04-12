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

## Open Questions

1. Should we support the Gondolin-style `createHttpHooks` explicit API or transparent interception?
2. How to handle non-HTTP protocols (raw TCP, gRPC without HTTP/2)?
3. Should SSH agent support key filtering (only certain keys forwarded to certain guests)?

## References

- Gondolin secrets handling: https://earendil-works.github.io/gondolin/secrets/
- Gondolin security design: https://earendil-works.github.io/gondolin/security/
- SSH agent protocol: https://tools.ietf.org/html/draft-miller-ssh-agent-02
