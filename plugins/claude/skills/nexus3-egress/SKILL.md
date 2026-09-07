---
name: nexus3-egress
description: >
  Load when authoring or debugging nexus3 egress policy: egress.policy allowlist
  structure, egress.secrets brokering model, per-provider patterns (GitHub, GitLab,
  generic tokens), and the four verification probes. Also load when a question
  involves GH_TOKEN in the guest, cross-repo 403, GraphQL 403, or "why does the
  sandbox have open egress."
---

# nexus3 egress policy — reference

## CRITICAL: OpenEgress posture of worktree sandboxes (TBD-2 SETTLED)

**The `egress.policy.allow` host allowlist is inert for worktree sandboxes.**

Every worktree sandbox created by the herdr plugin is created with `OpenEgress: true`.
Source: `internal/cli/cmd_herdr_plugin.go:3764` — the build-args function appends
`--egress open` to every `nexus3 sandbox create` call:

```go
args = append(args, "--agent", herdrPrimaryAgent(), "--egress", "open", handle)
```

Result: `internal/core/service/service.go:959` sets `allowAll = sb.Envelope.OpenEgress`,
which is `true`, so `al.AllowAllFor(72 * time.Hour)` is called and the netfilter ACL
passes everything. The `egress.policy.allow` list from `nexus3.yaml` is stored in the
envelope's `AllowedHosts` and is never consulted as a gate.

**What IS enforced for worktree sandboxes:**

| Layer | Enforced? | Evidence |
|---|---|---|
| `egress.secrets` credential brokering (placeholder swap) | YES | MITM proxy always runs when `SecretHosts` or `AgentName` is non-empty (`service.go:1132-1135`) |
| `egress.policy.paths` for SecretHosts (e.g. GitHub REST path ACL, cross-repo deny) | YES | MITM enforces `PathPolicies` regardless of `AllowAll` mode (`service.go:998-999`) |
| `egress.policy.paths` for non-secret hosts | NO | Path policies are only checked at the MITM layer for MITM-intercepted hosts |
| `egress.policy.allow` host allowlist (general ACL) | NO | Bypassed by `AllowAll` from `OpenEgress: true` |

**Live-verified (example-app/EX-929, 2026-09-07):**
```
$ cat ~/.local/state/nexus3/sandboxes/sb-06G7NG0K49VPNB8PHVX237H368/record.json | python3 -m json.tool | grep open_egress
    "open_egress": true,
```
Egress log: all decisions logged as "open egress bypass", zero enforced denies — matching
the MAP.md observation of 215 and 698 "open egress bypass" decisions.

**Stale comments in the source:**
- `cmd_sandbox.go:2209`: "Agent sandboxes (orca, herdr) must NOT set OpenEgress=true" —
  contradicted by `cmd_herdr_plugin.go:3764` which does set it. The comment describes the
  intended policy; the code does the opposite.
- `cmd_herdr_plugin.go:1163`: "OpenEgress left false — agent sandboxes never do" — this
  describes only the `buildLaunchBootOpts` function (used for pane-open launch), not the
  worktree-sandbox create path.

**Consequence for skill consumers:** Document `egress.policy.allow` as a best-effort
declaration of intent, not a security boundary. The binding security properties are:
secret brokering and the MITM path policy for SecretHosts.

---

## Brokering model (file:line citations)

The guest **never holds the real credential**. The model has three components:

### 1. Guest-side placeholder

The supervisor mints a 64-hex placeholder and writes it to `/run/nexus3/cred.env` inside
the guest (`internal/cli/cmd_herdr_plugin.go:1193-1210`, `internal/supervisor/supervisor.go`
`SeedGuestAgent`). The agent launch wrapper sources this file before exec'ing the real agent
command (`launchCredSourcedArgv`, `cmd_herdr_plugin.go:1193`), so the agent process sees the
placeholder as `GH_TOKEN` (or the appropriate env var).

The file is on a tmpfs (`/run`), so it does not survive a reboot. On supervisor re-adopt the
file is re-seeded from the same broker instance, keeping placeholder and proxy in sync.

### 2. MITM proxy substitution

`internal/core/service/service.go:982-1033` creates a `mitm.Proxy` whenever
`SandboxHasMITMProxy` returns true. For worktree sandboxes with secrets this is always true
(`service.go:1132-1135`). The proxy is configured with:
- `SecretHosts` — hosts whose outbound requests are intercepted for credential swap
- `SecretHostSuffixes` — dot-anchored DNS suffixes (covers sharded endpoints)
- `Broker` — the credential broker that knows the real secret
- `PathPolicies` — per-host path ACL for `SecretHosts`

On each outbound HTTPS request to a `SecretHost`, the proxy intercepts (MITM), replaces the
`Authorization: Bearer <placeholder>` header with `Authorization: Bearer <real-secret>`,
enforces the path ACL, and forwards. If the path is not in the allowlist the proxy returns
403 before forwarding.

### 3. Which hosts a secret is forwarded to

`nexus3.yaml` `egress.secrets` entries bind an env-var name to a list of hostnames
(`internal/core/config/config.go:44-51`, `EgressSecret`). This list becomes the
`SecretHosts` in the envelope. The MITM proxy intercepts only these hosts — traffic to
other hosts flows through unmodified (no credential swap, no path ACL).

---

## nexus3.yaml egress section

Place `nexus3.yaml` at the **repository root on the base branch** (`origin/main` or
`origin/master`). New worktree sandboxes read it via `git show refs/remotes/origin/HEAD:nexus3.yaml`.
A PR branch grants nothing — the policy must be merged before it takes effect.

```yaml
version: 1
egress:
  policy:
    # Per-host path allowlist for secret hosts.
    # Required for GitHub hosts (sandbox create refuses without it).
    # Not enforced as a general ACL for non-secret hosts.
    - host: api.github.com
      paths:
        - "/repos/owner/myrepo"
        - "/repos/owner/myrepo/**"
        - "/user"
    - host: github.com
      paths:
        - "/owner/myrepo/**"
        - "/owner/myrepo.git/**"
  secrets:
    # Bind host env var → target hosts (swap on these hosts only).
    - env: GH_TOKEN
      hosts: [github.com, api.github.com]
  allow:
    # Host allowlist — stored in envelope but NOT enforced as an ACL
    # for worktree sandboxes (open_egress: true bypasses the netfilter).
    # Declare it anyway as documented intent; it becomes the ACL if
    # OpenEgress is ever changed to false for this create path.
    - registry-1.docker.io
    - auth.docker.io
    - pypi.org
```

**Hard rule:** never add `/graphql` under `api.github.com` paths. The GitHub GraphQL endpoint
is a parallel write channel; the `nexus3-github-token-sole-bound` MEMORY note documents why
this matters. The MITM returns 403 for GraphQL even if listed (`service.go:971-977` backstop
fires for unbound GitHub secrets, and the graphql backstop in the MITM layer is a separate
guard).

---

## Provider patterns

### GitHub (github.com / api.github.com)

GitHub hosts require a path policy entry when listed in `egress.secrets`. Without one,
`StartPerimeterOnly` / `Start` returns `ErrUnboundGitHubSecret` (`service.go:971-977`).

```yaml
egress:
  policy:
    - host: api.github.com
      paths:
        - "/repos/{owner}/{repo}"
        - "/repos/{owner}/{repo}/**"
        - "/user"
    - host: github.com
      paths:
        - "/{owner}/{repo}/**"
        - "/{owner}/{repo}.git/**"
  secrets:
    - env: GH_TOKEN
      hosts: [github.com, api.github.com]
```

Replace `{owner}` and `{repo}` with the actual values. Keep paths scoped to the repo —
never `/repos/**` or `/**` at root. If the workflow needs release uploads, add
`uploads.github.com` to the `secrets.hosts` list and a matching path entry.

### GitLab (gitlab.com or self-hosted)

GitLab hosts are not checked by `isGitHubHost` (`service.go:972`), so no mandatory path
policy. The path-policy feature for non-GitHub hosts is available but optional.

```yaml
egress:
  secrets:
    - env: GL_TOKEN
      hosts: [gitlab.com]
    # Self-hosted example:
    # - env: GL_PRIVATE_TOKEN
    #   hosts: [git.example.com]
```

If a GitLab instance uses a shared runner or package registry at a different subdomain,
add those hostnames to the same or a separate secrets entry.

### Generic API token (non-VCS)

For any service that needs a bearer token brokered (package registries, internal APIs):

```yaml
egress:
  secrets:
    - env: NPM_TOKEN
      hosts: [registry.npmjs.org]
    - env: PYPI_TOKEN
      hosts: [upload.pypi.org]
```

Short form (equivalent to the long mapping):
```yaml
egress:
  secrets:
    - "NPM_TOKEN@registry.npmjs.org"
```

No path policy requirement for non-GitHub hosts. The broker swaps the token on all requests
to the listed hosts regardless of path.

### Shared shape across providers

All providers share the same model:
1. Add the env var name and target hostnames to `egress.secrets`.
2. For GitHub hosts: also add `egress.policy` entries with specific paths.
3. For non-GitHub hosts: path policy is optional.
4. The MITM proxy handles the swap; the guest always sees a placeholder.
5. List the hosts in `egress.allow` as well (inert today, documents intent).

---

## Verification probes

Run inside the sandbox shell after sourcing credentials:

```bash
source /run/nexus3/cred.env
```

(The agent process sources this automatically at launch via `launchCredSourcedArgv`. A bare
`nexus3 exec` shell does NOT source it — run the `source` command above before probing.)

### Probe 1 — Own-repo REST → 200 expected

```bash
curl -s -o /dev/null -w "HTTP %{http_code}\n" \
  -H "Authorization: Bearer $GH_TOKEN" \
  https://api.github.com/repos/{owner}/{repo}
# Expected: HTTP 200
```

### Probe 2 — Cross-repo REST → 403 expected

```bash
curl -s -o /dev/null -w "HTTP %{http_code}\n" \
  -H "Authorization: Bearer $GH_TOKEN" \
  https://api.github.com/repos/some-other-org/other-repo
# Expected: HTTP 403  (MITM path policy deny)
```

### Probe 3 — GraphQL → 403 expected

```bash
curl -s -o /dev/null -w "HTTP %{http_code}\n" \
  -X POST \
  -H "Authorization: Bearer $GH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"{ viewer { login } }"}' \
  https://api.github.com/graphql
# Expected: HTTP 403  (GraphQL denied fail-closed)
```

### Probe 4 — Placeholder check → 64-hex expected

```bash
echo "$GH_TOKEN" | grep -E '^[0-9a-f]{64}$' \
  && echo "PASS: placeholder" \
  || echo "FAIL: real token exposed"
```

---

## Evidence — live run (2026-09-07, example-app/EX-929)

```
=== Probe 4: placeholder ===
ae0d7a81926cda8b80764d513c4240d2cba9e5fc2be5d021013e0bd11fac9c24
PASS: 64-hex placeholder

=== Probe 1: own-repo REST ===
HTTP 200

=== Probe 2: cross-repo REST ===
HTTP 403

=== Probe 3: GraphQL ===
HTTP 403
```

### AC-3: Failure demonstration

Both arms recorded against example-app/EX-929:

**Wrong configuration — real-looking token, not a placeholder** (fails probe 4):
```
export GH_TOKEN=ghp_xK3mQvN8pL2rJ7wT1cF0yB9nE4uH6oI5sA
echo "$GH_TOKEN" | grep -E '^[0-9a-f]{64}$' && echo PASS || echo 'FAIL: real token exposed'
→  FAIL: real token exposed
```

**Correct configuration — placeholder from cred.env** (passes probe 4):
```
source /run/nexus3/cred.env
echo "$GH_TOKEN" | grep -E '^[0-9a-f]{64}$' && echo PASS || echo 'FAIL: real token exposed'
→  ae0d7a81926cda8b80764d513c4240d2cba9e5fc2be5d021013e0bd11fac9c24
→  PASS: placeholder
```

---

## Existing prose that is wrong or misleading

- `skills/nexus3/SKILL.md:172` — "An agent sandbox boots with default-deny egress." This is
  true for manually created sandboxes (`nexus3 sandbox create` without `--egress open`) but
  NOT for worktree sandboxes created by the herdr plugin. Worktree sandboxes get
  `open_egress: true` and bypass the netfilter ACL.

- `skills/nexus3/SKILL.md:606` — "The MITM proxy enforces default-deny — only listed paths
  are forwarded." Correct for SecretHost path policies. Incorrect as a statement about general
  host ACL — non-secret hosts are not path-filtered at all.

- `skills/nexus3/SKILL.md:606` — "Without one, `sandbox create` is refused with a hard error."
  Correctly describes `ErrUnboundGitHubSecret` (`service.go:971-977`), but only applies to
  GitHub hosts in `egress.secrets`. Non-GitHub hosts can be listed in secrets without a policy.
