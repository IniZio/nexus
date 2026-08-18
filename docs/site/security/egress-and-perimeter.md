---
title: "Egress and Perimeter"
description: "Default-deny egress, MITM credential substitution, and the GitHub request allowlist"
---

# Egress and Perimeter

> Per-sandbox default-deny: sandboxes reach only the hosts you name; credentials never cross the guest boundary.

Every sandbox starts with no external network access. Outbound connections are evaluated host-side against a per-sandbox hostname allowlist before they reach the wire. Real credentials stay on the host — the guest holds only high-entropy placeholders that the MITM proxy swaps in on the wire.

```sh
# Agent sandbox: api.anthropic.com and platform.claude.com are built-in
nexus3 create my-agent

# Add an extra host to the curated allowlist
nexus3 create --allow-host registry.npmjs.org my-sandbox

# Add a GitHub repo (host-side token; MITM swaps on egress)
nexus3 create --repo owner/repo my-sandbox
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

The `--egress` flag selects the mode at creation time. `--allow-host` and `--repo` extend the allowlist for curated sandboxes.

## Egress modes

| Mode | Condition | MITM proxy | Credential seeding |
|---|---|---|---|
| **Curated** | `AllowedHosts` non-empty | Yes — swaps placeholder → real bearer | Yes — `SeedGuest` mints one placeholder per host |
| **AllowAll** | `AllowedHosts` empty | No | No |

**AllowAll** is used by builder VMs (`AllowAllFor(24h)`) and by `--context`-path sandboxes (`AllowAllFor(72h)`). <Badge type="warning" text="partial" /> — current implementation uses `--file`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping. These sandboxes have unrestricted egress and no credential injection; the MITM proxy is not instantiated. The time window is generous; supervisor restarts reset it.

**Curated** is used by agent sandboxes. `AgentEgressHosts()` returns `api.anthropic.com` and `platform.claude.com`. The MITM proxy intercepts all HTTPS to allowed hosts.

> **Code state (verified)**: `service.go:676` — `allowAll := len(sb.Envelope.AllowedHosts) == 0`. AllowAll sandboxes skip MITM instantiation entirely. This is intentional — builder VMs need unrestricted egress; it is not a gap to close.

## Network stack

The perimeter is assembled from three vetted libraries, not hand-rolled:

| Layer | Library | Role |
|---|---|---|
| L3/L4 userspace net | **gvproxy** (`containers/gvisor-tap-vsock`, Apache-2.0) | Owns all guest packets. `tcp.NewForwarder` is the per-connection accept/deny hook. DNS rides gvproxy's bundled `miekg/dns`. |
| L4 allowlist | **clawk `netfilter.AllowList`** (Apache-2.0, copied with attribution) | Default-deny L4 host:port allowlist on every SYN/UDP/ICMP/DNS. `ObserveDNSAnswer` builds the DNS name→IP table so IP-only connections are still matched. |
| L7 MITM | **`elazarl/goproxy`** (BSD-3) | Local CA + leaf minting + per-host `Authorization` rewrite. Enforces hostname allowlist on real SNI/Host — closing the shared-CDN/IP hole that L4 IP-allowlisting alone leaves. |

The only custom code is a transparent SNI→CONNECT shim (an adversarial guest will not honour `HTTPS_PROXY`, so traffic must be intercepted transparently) plus hook wiring. `gomitmproxy` (GPL) and `martian` (archived) were evaluated and rejected.

## Credential model

The normative model is **TLS-MITM placeholder substitution**. No real credential ever exists in a guest.

1. At sandbox start, `SeedGuest` (`internal/core/service/seed.go`) mints one high-entropy placeholder per allowed host via the host credential broker and writes an env-file into the guest:

   ```
   NEXUS3_CRED_API_ANTHROPIC_COM_TOKEN=<64-hex placeholder>
   NEXUS3_CRED_API_ANTHROPIC_COM_EXPIRES_AT=2099-12-31T23:59:59Z
   ```

   The env-file is seeded once at sandbox start. There is no re-pull loop, no `oauth_shim`, no guest→host vsock credential channel.

2. For **allowed hosts only**, the MITM proxy swaps `Authorization: Bearer <placeholder>` for the real current bearer on the wire. A placeholder sent to a non-allowed host is never swapped — it is useless off the allowlist.

3. **All dynamism is host-side.** The host broker keeps the real token fresh. The synthetic far-future `expiresAt` (`2099-12-31`) stops the agent from self-refreshing (which would require a real refreshToken it does not hold).

### Credential kinds

Two credential kinds are supported (mutually exclusive per sandbox):

| Kind | Guest env var | Use case |
|---|---|---|
| OAuth | `CLAUDE_CODE_OAUTH_TOKEN=<placeholder>` | Default; Claude Code agent using OAuth flow |
| Direct API | `ANTHROPIC_AUTH_TOKEN=<placeholder>` | API-key rail; guest sends `Authorization: Bearer <placeholder>`, proxy swaps |

### Verified properties

- The Anthropic subscription `accessToken` is an **opaque 108-char string, not a JWT**. Claude Code sends it verbatim and trusts the file's numeric `expiresAt` without decoding the token. Both conditions the placeholder model requires hold.
- Proven in production: 6 OAuth token rotations, 0 client refresh requests — synthetic far-future expiry is effective.

## Secret rotation

Rotating a credential used via `--secret` takes effect for any sandbox created after the rotation — new sandboxes receive a fresh placeholder minted from the updated host credential. Running sandboxes hold the placeholder issued at start; stop and restart them to pick up the rotated value. The named secret store (`nexus3 secret set`) <Badge type="danger" text="not built" /> will provide the same guarantee once built: rotate once, the new value applies to all subsequently created sandboxes.

## GitHub and the request allowlist

`github.com` is **absent from agent `AllowedHosts`** by default. `AgentEgressHosts()` and the orca path (`gitHostsFromURL`) never add it. GitHub hosts enter a sandbox envelope only via `--repo` or `--allow-host` — the guest holds a placeholder; the host broker holds the real `gh auth token`. SSH is rejected.

## Credential file freshness

The host broker refreshes the real token using the standard OAuth token refresh flow. The guest env-file is seeded once at sandbox start (`--cache=never`, `rename(2)` + close-to-open semantics). The broker's refresh is transparent to the guest.

## SSH identity

SSH identity (key forwarding) is a separate concern from the credential broker. The Orca workspace `proxyCommand` path (`cmd_orca.go:129`, `buildOrcaConnectionJSON`) wires SSH over vsock so that a workspace opened via direct SSH can reach the guest.

## v1 scope

The guarantee is: **allowlist + audit**. Default-deny per-sandbox hostname allowlists are enforced host-side, and the MITM path provides audit logging.

Gaps accepted for v1 (see [Known Risks](accepted-risks.md)):

- **Allowed-host exfiltration**: data can be encoded in requests to an allowed host. The residual control is allowlist scoping — keep `AllowedHosts` minimal.
- **DoS / resource governance**: no rate limiting, memory caps, or CPU caps on egress traffic.
- **Covert channels**: no restriction on side-channel communication.
