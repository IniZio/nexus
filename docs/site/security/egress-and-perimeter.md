---
title: "Egress and Perimeter"
description: "Default-deny egress, credential delivery, the git SSH relay, and the GitHub request allowlist"
---

# Egress and Perimeter

> Per-sandbox default-deny: sandboxes reach only the hosts you name; credentials never cross the guest boundary as plaintext secrets.

Every sandbox starts with no external network access. Outbound connections are evaluated host-side against a per-sandbox hostname allowlist before they reach the wire.

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
| **Curated** | `AllowedHosts` non-empty | Yes — swaps placeholder → real bearer for GitHub/MCP hosts | Yes — `SeedGuest` mints one placeholder per non-Claude allowed host |
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

## Claude Code credential delivery

The `claude-code` agent profile uses a **live read-write virtiofs mount** of the host's `~/.claude` directory at `/root/.claude` inside the guest. There is no placeholder, no broker swap, and no synthetic expiry — the guest holds the real `~/.claude/.credentials.json` and refreshes its own token exactly as the host does.

### What the guest can see

The entire host `~/.claude` is mounted read-write. This includes:

- `.credentials.json` — the real OAuth credential, refreshed in place
- `settings.json` — the host's Claude Code settings, including hooks, plugins, and permission mode
- `history.jsonl`, `projects/`, `todos`, `debug/` — full session history and transcripts

Guest Claude inherits the host's permission mode (`auto` by default; see [AI agents](/ai-agents)) and runs with the host's tool configuration. Hook binaries that are also live-mounted (`~/.local/bin`, mise tools) resolve normally; hooks that reference host-only paths produce tool errors, not agent failures.

### Host CredGuardian

The supervisor arms a `CredGuardian` (`internal/core/perimeter/cred/guardian.go`) against the host `~/.claude/.credentials.json`. It polls once per minute and proactively refreshes the token 30 minutes before expiry — the same threshold Claude Code itself uses. Concurrent guardians (one per running sandbox) serialise refreshes via an advisory `flock(2)` on a sidecar lock file; the loser re-reads the file after acquiring the lock and skips a redundant refresh.

> **Risk (R-2 — Host state exposure):** the mount hands every guest the host's full `~/.claude` read-write. A compromised guest can read all session history and transcripts, and can mutate host settings and hooks (which execute on the host's next session). This risk is accepted by design. See [Accepted risks](/security/accepted-risks).

### Recreate rule (R-7)

Virtiofs mounts and the live-mount manifest are create-time state. Running sandboxes continue with whatever credential model was in effect at their creation. **Existing sandboxes must be recreated to receive the live-mount credential model.** `supervisor-upgrade --force` reloads the host supervisor binary but cannot add a vhost-user-fs device to an already-running VM.

## Other credential kinds (placeholder model)

For all non-Claude hosts — GitHub, MCP OAuth servers, custom API endpoints — the normative model remains **TLS-MITM placeholder substitution**: no real credential exists in the guest.

1. At sandbox start, `SeedGuest` mints one high-entropy placeholder per allowed host and writes it into the guest:

   ```
   NEXUS3_CRED_API_ANTHROPIC_COM_TOKEN=<64-hex placeholder>
   NEXUS3_CRED_API_ANTHROPIC_COM_EXPIRES_AT=2099-12-31T23:59:59Z
   ```

2. For **allowed hosts only**, the MITM proxy swaps `Authorization: Bearer <placeholder>` for the real current bearer on the wire. A placeholder sent to a non-allowed host is never swapped — it is useless off the allowlist.

3. **All dynamism is host-side.** The host broker keeps the real token fresh. The synthetic far-future `expiresAt` (`2099-12-31`) stops agents using this path from self-refreshing.

### Credential kinds (non-Claude)

| Kind | Guest env var | Use case |
|---|---|---|
| Direct API | `ANTHROPIC_AUTH_TOKEN=<placeholder>` | API-key rail; guest sends `Authorization: Bearer <placeholder>`, proxy swaps |
| GitHub | `GH_TOKEN=<placeholder>` | `gh` CLI and GitHub API; brokered via `--repo` or `--secret GH_TOKEN@...` |
| MCP OAuth | per-MCP placeholder | Host-refresh guardian, same broker machinery |

## Secret rotation

Rotating a credential used via `--secret` takes effect for any sandbox created after the rotation — new sandboxes receive a fresh placeholder minted from the updated host credential. Running sandboxes hold the placeholder issued at start; stop and restart them to pick up the rotated value. The named secret store (`nexus3 secret set`) <Badge type="danger" text="not built" /> will provide the same guarantee once built: rotate once, the new value applies to all subsequently created sandboxes.

## GitHub and the request allowlist

`github.com` enters a sandbox envelope only via `--repo` or `--allow-host`. The guest holds a 64-hex placeholder for `GH_TOKEN`; the host broker holds the real `gh auth token`. The MITM swaps the placeholder on every `api.github.com` and `uploads.github.com` request, so REST `gh api` calls work from inside the guest.

On `github.com` itself, git smart-HTTP (`info/refs`, `git-upload-pack`, `git-receive-pack`) is permitted only for the bound repo. Public artifact downloads — `GET`/`HEAD` on `/<owner>/<repo>/archive/*` and `/<owner>/<repo>/releases/download/*` — are permitted for any repository, the same trust level as `codeload.github.com` they redirect to. Those requests carry no credential upstream: the MITM strips the `GH_TOKEN` placeholder instead of swapping it, so `curl -L https://github.com/<org>/<repo>/archive/<tag>.zip` works from inside the guest without exposing the host token to a foreign repository.

A host is either open or policy-gated, never both. `.nexus/config.yaml` may not list the same host under `egress.allow` (open passthrough) and under `egress.policy` or `egress.secrets[].hosts` (default-deny path allowlist, credential brokered). The policy layer takes precedence, so such an `allow` entry would be inert while the file claims open access; the config loader rejects it at parse time, case-insensitively, with `nexus3 config: host "<host>" is listed under egress.allow and egress.policy; a host can be open (allow) or policy-gated (policy/secrets), not both — remove it from egress.allow`. Because public archive and release downloads are already permitted on policy-gated `github.com`, no `allow` entry is needed for release tarballs; `codeload.github.com` may still be listed under `allow` since it is never policy-gated.

:::warning `gh pr create` is refused
`gh pr create` uses GitHub's GraphQL API, which the perimeter denies (GraphQL default-deny). Create PRs with the REST form:

```sh
gh api -X POST /repos/<owner>/<repo>/pulls \
  -f title="..." -f head="<branch>" -f base="main" -f body="..."
```
:::

**git over SSH** to GitHub remotes uses a separate path that does not go through the MITM (see below). The MITM and GH_TOKEN broker are retained for all HTTPS API traffic.

## git SSH relay

From inside a claude-code sandbox, git SSH remotes (`git@github.com:owner/repo.git`) work natively through a host-side relay. No private key material is placed in the guest; the host's `SSH_AUTH_SOCK` (ssh-agent) handles authentication.

### How it works

1. The `GIT_SSH_COMMAND` environment variable in the guest points at a `nexus3-agent` shim binary.
2. When the guest runs `git push git@github.com:owner/repo`, the shim sends the SSH command over a vsock channel to the host relay.
3. The host relay (`internal/supervisor/gitssh_relay.go`) verifies the request against the repo's `.nexus/config.yaml` egress policy, then execs the real `ssh` with the host's `SSH_AUTH_SOCK`.

### Policy guard

The relay enforces two rules before forwarding:

**Host + repo allowlist** — derived from `.nexus/config.yaml` `egress.policy`. A push to a repo not in the policy is refused immediately. The error written to git's stderr is:

```
nexus3: refused by egress policy: <host> <owner/repo> not in .nexus/config.yaml egress.policy
```

**Branch allowlist** — on `git-receive-pack` (push) only, the relay parses the pkt-line ref negotiation and checks each ref against the sandbox's `AllowedBranches` (default: `refs/heads/nexus3/**`). A push to an out-of-allowlist ref is refused:

```
nexus3: refused: ref <refname> not in allowed branches
```

Only `git-upload-pack` (fetch/clone) and `git-receive-pack` (push) are forwarded. Any other SSH command — including interactive shells, `sftp`, and flag-injection attempts like `-oProxyCommand=…` — is refused.

### `gh` / HTTPS API

`gh api` and any HTTPS GitHub traffic continue to route through the MITM + brokered `GH_TOKEN` path. The SSH relay does not affect HTTPS. Note that `gh pr create` is refused because it uses GitHub's GraphQL API, which the MITM denies by default; use `gh api -X POST /repos/<owner>/<repo>/pulls` for PR creation from inside the sandbox.

## SSH identity (Orca workspace)

The Orca workspace `proxyCommand` path (`cmd_orca.go:129`, `buildOrcaConnectionJSON`) wires SSH over vsock so that a workspace opened via direct SSH can reach the guest. This is a separate concern from the git SSH relay above.

## v1 scope

The guarantee is: **allowlist + audit**. Default-deny per-sandbox hostname allowlists are enforced host-side, and the MITM path provides audit logging.

Gaps accepted for v1 (see [Known Risks](accepted-risks.md)):

- **Allowed-host exfiltration**: data can be encoded in requests to an allowed host. The residual control is allowlist scoping — keep `AllowedHosts` minimal.
- **DoS / resource governance**: no rate limiting, memory caps, or CPU caps on egress traffic.
- **Covert channels**: no restriction on side-channel communication.
