# Egress and Perimeter

nexus3 enforces a per-sandbox egress perimeter. Every sandbox has a hostname allowlist; traffic to non-listed hosts is dropped at the L4 layer before it reaches the network.

## Two operating modes

The perimeter has two modes, selected by whether `AllowedHosts` is set at sandbox creation.

| Mode | Condition | MITM proxy | Credential seeding |
|---|---|---|---|
| **Curated** | `AllowedHosts` non-empty | Yes — swaps placeholder → real bearer | Yes — `SeedGuest` mints one placeholder per host |
| **AllowAll** | `AllowedHosts` empty | No | No |

**AllowAll mode** is used by builder VMs (`AllowAllFor(24h)`) and by `--file`-path sandboxes (`AllowAllFor(72h)`). These sandboxes have unrestricted egress and no credential injection; the MITM proxy is not instantiated. The time window is generous; supervisor restarts reset it.

**Curated mode** is used by agent sandboxes. `AgentEgressHosts()` returns `api.anthropic.com` and `platform.claude.com`. The MITM proxy is instantiated and intercepts all HTTPS to allowed hosts.

> **Code state (verified)**: `service.go:676` — `allowAll := len(sb.Envelope.AllowedHosts) == 0`. Comments at `service.go:667-684` confirm AllowAll sandboxes skip MITM instantiation entirely. This is intentional: builder VMs need unrestricted egress per spec 07; it is not a gap to close.

## Network stack

The perimeter is assembled from three vetted libraries, not hand-rolled:

| Layer | Library | Role |
|---|---|---|
| L3/L4 userspace net | **gvproxy** (`containers/gvisor-tap-vsock`, Apache-2.0) | Owns all guest packets. `tcp.NewForwarder` is the per-connection accept/deny hook. DNS rides gvproxy's bundled `miekg/dns`. |
| L4 allowlist | **clawk `netfilter.AllowList`** (Apache-2.0, copied with attribution) | Default-deny L4 host:port allowlist on every SYN/UDP/ICMP/DNS. `ObserveDNSAnswer` builds the DNS name→IP table so IP-only connections are still matched. |
| L7 MITM | **`elazarl/goproxy`** (BSD-3) | Local CA + leaf minting + per-host `Authorization` rewrite. Enforces hostname allowlist on real SNI/Host — closing the shared-CDN/IP hole that L4 IP-allowlisting alone leaves. |

The only custom code is a transparent SNI→CONNECT shim (an adversarial guest will not honour `HTTPS_PROXY`, so traffic must be intercepted transparently) plus hook wiring. `gomitmproxy` (GPL) and `martian` (archived) were rejected.

## Credential model

### Overview

The normative model is **TLS-MITM placeholder substitution**. No real credential ever exists in a guest.

1. At sandbox start, `SeedGuest` (`internal/core/service/seed.go`) mints one high-entropy placeholder per allowed host via the host credential broker and writes an env-file into the guest:

   ```
   NEXUS3_CRED_API_ANTHROPIC_COM_TOKEN=<64-hex placeholder>
   NEXUS3_CRED_API_ANTHROPIC_COM_EXPIRES_AT=2099-12-31T23:59:59Z
   ```

   The env-file is seeded once. There is no re-pull loop, no `oauth_shim`, no guest→host vsock credential channel.

2. For **allowed hosts only**, the MITM proxy swaps `Authorization: Bearer <placeholder>` for the real current bearer on the wire. A placeholder sent to a non-allowed host is never swapped — it is useless off the allowlist.

3. **All dynamism is host-side.** The host broker keeps the real token fresh. The synthetic far-future `expiresAt` (`2099-12-31`) stops the agent from self-refreshing (which would require a real refreshToken it does not hold).

### Credential kinds

Two credential kinds coexist at runtime (mutually exclusive per sandbox):

| Kind | Guest env var | Use case |
|---|---|---|
| OAuth | `CLAUDE_CODE_OAUTH_TOKEN=<placeholder>` | Default; Claude Code agent using OAuth flow |
| Direct API | `ANTHROPIC_AUTH_TOKEN=<placeholder>` | API-key rail; guest sends `Authorization: Bearer <placeholder>`, proxy swaps |

### Verified facts

- The Anthropic subscription `accessToken` is an **opaque 108-char string, not a JWT**. Claude Code sends it verbatim and trusts the file's numeric `expiresAt` without decoding the token. Both conditions the placeholder model requires hold.
- Proven in production: 6 OAuth token rotations, 0 client refresh requests — synthetic far-future expiry is effective.

`github.com` is **absent from agent `AllowedHosts`** (D-PD-22 / D-PD-23).
`AgentEgressHosts()` and the orca path (`gitHostsFromURL`) never add it.
A future `--secret ENV@hosts` bind on a human/git sandbox is the only way
GitHub hosts enter an envelope. The guest holds a placeholder; the host
broker holds `gh auth token`. SSH is rejected.

## Credential file freshness

The host broker refreshes the real token using the standard OAuth token refresh flow. The guest env-file is seeded once at sandbox start (`--cache=never`, `rename(2)` + close-to-open semantics). The broker's refresh is transparent to the guest.

## SSH identity

SSH identity (key forwarding) is a separate concern from the credential broker. The Orca workspace `proxyCommand` path (`cmd_orca.go:129`, `buildOrcaConnectionJSON`) wires SSH over vsock so that a workspace opened via direct SSH can reach the guest.

## v1 non-goals

These are accepted gaps for v1 (see [Accepted Risks](accepted-risks.md)):

- **Allowed-host exfiltration**: a placeholder-bearing request to an *allowed* host can carry data out. Under MITM the guest holds only placeholders — allowlist scoping, not the transport, is the residual control.
- **DoS / resource governance**: no rate limiting, memory caps, or CPU caps on egress traffic.
- **Covert channels**: no restriction on side-channel communication.

The guarantee is: **allowlist + audit**. Default-deny per-sandbox hostname allowlists, enforced host-side, plus audit logging on the MITM path.
