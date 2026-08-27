---
name: nexus3
description: Reference skill for the nexus3 microVM sandbox tool — CLI commands, sandbox lifecycle, and volume configuration for write-heavy project directories.
---

# nexus3

nexus3 creates and manages isolated Firecracker microVMs (sandboxes). Each sandbox has its own network, filesystem, and process namespace.

## Sandbox lifecycle

```sh
nexus3 create [--file <Dockerfile>] [--mount <host-path>:<guest-path>[:ro]] [--mount-named <name>:<guest-path>[:<options>]] ...
nexus3 ps
nexus3 start <sandbox-ref>
nexus3 stop <sandbox-ref>
nexus3 pause <sandbox-ref>
nexus3 resume <sandbox-ref>
nexus3 rm <sandbox-ref>
nexus3 shell <sandbox-ref>
nexus3 forward <sandbox-ref> <host-port>:<guest-port>
```

---

## Live host mounts (`--mount`)

`--mount <host-path>:<guest-path>[:ro]` mounts a host directory into the guest as a live virtiofs share. Edits inside the sandbox appear on the host immediately — no archive or sync step. Repeatable.

**When to use `--mount`**: mounting a git worktree so an in-guest agent can edit source files and commit directly to the host branch.

**Key rules:**
- The host path must exist and be a directory; it is resolved to an absolute path.
- `.git` guest paths are **allowed** (D-PD-99) — this is the deliberate divergence from `--mount-named`. Mounting a real worktree's `.git` is the primary use-case.
- `fork` and `snapshot create` are **refused** on a sandbox with live mounts; the error names the offending host→guest pairs.

**Contrast with `--mount-named`:**

| | `--mount` | `--mount-named` |
|---|---|---|
| Backing | Host directory via virtiofs | User-owned volume (ext4 disk or virtiofs dir) |
| Persistence | Host filesystem | Volume store (`nexus3 volume rm` to delete) |
| `.git` guest path | Allowed | **Hard refused** |
| Fork / snapshot | Refused | Refused (TBR-PD-15, deferred) |
| Use case | Live worktree editing | Dependency stores, build caches |

Do **not** use `--mount` to mount dependency directories (node_modules, target, etc.) — use `--mount-named kind=disk` for those; block I/O is measurably faster for metadata-heavy operations.

---

## Volume configuration for write-heavy directories

Build caches and dependency directories inside a sandbox are ephemeral by default — they vanish when the sandbox is removed. Named volumes persist across sandbox lifecycles and are user-owned (the reaper never removes them automatically). Mounting write-heavy directories as named volumes avoids repeated cold downloads and rebuilds.

### Flag grammar

```
--mount-named <name>:<guest-path>[:<options>]
```

`--mount-named` is repeatable. Options are comma-separated:

| Option | Values | Default | Meaning |
|--------|--------|---------|---------|
| `kind` | `dir`, `disk` | `disk` | `dir` = virtiofs host directory; `disk` = ext4 block image |
| `size` | e.g. `10g`, `20g` | `10g` | Backing disk size (kind=disk only) |
| `ro` | (flag) | rw | Mount read-only in guest |

**Prefer `kind=disk`** for dependency stores and build caches — block I/O is ~20× faster than virtiofs for metadata-heavy operations. Use `kind=dir` only when `rm -rf` semantics without EBUSY errors are more important than write throughput.

### Volume naming

Agent-generated volume names follow the pattern `<projectslug>-<dirname>` where `<projectslug>` is a short stable identifier (e.g. the project directory basename). Re-runs reuse existing volumes; `--mount-named` with create-on-mount is idempotent.

### Hard rule — never emit .git paths

**Skip any candidate guest path that contains `.git` as any path component — not just as a final segment.** Both of these are refused:

- `/workspace/proj/.git`
- `/workspace/proj/.git/objects`

Under the live virtiofs mount model, git commits inside the guest must reach the host working tree. A volume over any `.git` path intercepts writes and breaks that contract. The primitive enforces this as a hard refusal at the flag layer regardless of source; the skill must not emit such paths in the first place.

---

### Ecosystem manifest-probe rules

Examine the project root for the manifests below. Emit the corresponding `--mount-named` flags. Multiple ecosystems may apply to the same project.

#### npm / node_modules

| What to detect | What to emit |
|----------------|-------------|
| `package.json` present, no `workspaces` field, no PnP marker | One `<slug>-node_modules` kind=disk volume at `/workspace/<project>/node_modules` |
| `package.json` with `workspaces` field | One `<slug>-<pkgname>-node_modules` kind=disk volume per workspace package at each package's `node_modules` |

Do **not** emit a `node_modules` volume when Yarn PnP is active (see Yarn PnP section below).

#### pnpm

| What to detect | What to emit |
|----------------|-------------|
| `pnpm-lock.yaml` present (single package) | One `<slug>-node_modules` kind=disk volume at `/workspace/<project>/node_modules` |
| `pnpm-workspace.yaml` present | Per-package `<slug>-<pkgname>-node_modules` kind=disk volumes |

**Store co-location requirement:** pnpm hardlinks store entries into `node_modules`. The pnpm content-addressable store and the `node_modules` tree must reside on the **same filesystem** for hardlinking to work. By default pnpm places the store at `node_modules/.pnpm` (inside the mounted disk volume), so no separate store volume is needed. If the project sets a custom `store-dir` outside the project tree (check `.npmrc` or `pnpm-workspace.yaml`), emit a second kind=disk volume for that store path and confirm both volumes are kind=disk — hardlinking across a virtiofs mount and a disk volume does not work.

#### Yarn PnP

| What to detect | What to emit |
|----------------|-------------|
| `.pnp.cjs` present, OR `.yarnrc.yml` contains `nodeLinker: pnp` | `<slug>-yarn-cache` kind=disk at `/workspace/<project>/.yarn/cache` AND `<slug>-yarn-unplugged` kind=disk at `/workspace/<project>/.yarn/unplugged` |

In PnP mode there is no `node_modules` tree — do **not** emit a node_modules volume.

#### Rust

| What to detect | What to emit |
|----------------|-------------|
| `Cargo.toml` with `[workspace]` section | One `<slug>-target` kind=disk volume at the **repo root** `/workspace/<project>/target` |
| `Cargo.toml` (single crate, no workspace) | One `<slug>-target` kind=disk volume at the crate root |

Cargo compiles all crates in a workspace into a single root `target/` directory. Mount at the root only — do not emit per-crate target volumes.

#### Go

| What to detect | What to emit |
|----------------|-------------|
| `go.mod` present | `<slug>-go-build` kind=disk at guest `/root/.cache/go-build` AND `<slug>-go-mod` kind=disk at guest `/root/go/pkg/mod` |

Go caches are guest-global (not inside the project tree). Both volumes are kind=disk.

#### Docker

| What to detect | What to emit |
|----------------|-------------|
| `Dockerfile`, `docker-compose.yml`, or `compose.yml` present | `<slug>-docker` kind=disk at guest `/var/lib/docker`, **size=20g** |

`/var/lib/docker` must be kind=disk. The dockerd overlay2 storage driver requires a real block device; virtiofs does not support the ioctl set it needs.

#### Docker Compose bind-mounts

If the project uses Docker Compose with host bind-mounts in `volumes:` entries (e.g. `./data:/app/data`), those paths resolve to locations inside the sandbox workspace — not on the host. They do not require `--mount-named` flags. Inform the operator: compose bind-mounts are already inside the sandbox workspace volume and are discarded with the sandbox on `nexus3 rm`.

#### No recognized manifest

Emit nothing. `nexus3 sandbox create` proceeds with no `--mount-named` flags; the sandbox uses its plain workspace disk.

---

### What to emit

A shell fragment for operator review. The operator adjusts sizes and volume names before running.

```sh
# Generated by nexus3 skill (volume-config) for project: myapp
nexus3 sandbox create \
  --mount-named myapp-node_modules:/workspace/myapp/node_modules:kind=disk,size=10g \
  --mount-named myapp-target:/workspace/myapp/target:kind=disk,size=15g \
  --mount-named myapp-docker:/var/lib/docker:kind=disk,size=20g \
  ...
```

**The skill does not invoke the command.** It emits a reviewable fragment. The operator sees exactly which volumes will be created and can edit the list before running.

Run `nexus3 volume ls` to inspect existing volumes. Run `nexus3 volume rm <name>` before recreating a volume with a different size (size changes require recreate).

---

## Dev-environment egress policy

An agent sandbox (`--agent <name>`) boots with **default-deny egress**. Only the
agent profile's own hosts are reachable — for `claude-code` that is
`api.anthropic.com` and `platform.claude.com`. Nothing else resolves, so every
package manager fails at connect time until its hosts are named explicitly.

Add dev-toolchain hosts with `--allow-host <host>` (repeatable):

```sh
nexus3 create <project>/<name> --image <digest> --agent claude-code \
  --mount /path/to/checkout:/work \
  --allow-host proxy.golang.org \
  --allow-host sum.golang.org \
  --allow-host storage.googleapis.com
```

### Do not combine `--allow-host` with `--egress closed` on an agent sandbox

The flag's own help text says `--allow-host` applies "when `--egress closed`".
For an agent sandbox that combination is **structurally impossible**:
`--egress closed` requires `--repo`, which binds a GitHub secret, which
`service.ValidateSecrets` refuses with `ErrAgentGitHubSecret`.

`--allow-host` works on an agent sandbox **without** `--egress closed` —
`resolveAgentPosture` unions the list onto the profile's hosts unconditionally.
Pass `--allow-host` alone.

### Per-ecosystem host sets

| Toolchain | Hosts required |
|-----------|----------------|
| Go | `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com` |

`storage.googleapis.com` is **not optional** for Go: `proxy.golang.org` serves
module zips as redirects to signed `storage.googleapis.com` URLs. Omitting it
produces a build that downloads part of the graph and then fails with
`connection refused` — a confusing partial failure, not a clean one.

Note that `storage.googleapis.com` is a broad host (every GCS bucket). Treat
adding it as a deliberate widening, not a formality.

Host sets for other ecosystems are **not yet verified**. Determine them by
running the toolchain and reading the refused hostnames out of the failure —
do not guess, and add the verified set here.

---

## Guest setup a dev sandbox needs

Three things are not done for you. Each produces a confusing failure rather
than a clear one.

### 1. Install the MITM CA for non-Node clients

Allowlisted hosts are **TLS-intercepted** by the per-sandbox MITM proxy. The
`claude-code` profile exports `NODE_EXTRA_CA_CERTS`, which only Node reads — so
`claude` works while every other TLS client fails certificate validation. The CA
is seeded to disk but never installed into the system trust store:

```sh
update-ca-certificates
```

Run this once per boot before any `go`, `curl`, `pip`, or `cargo` call.
Allowlisting a host is necessary but **not sufficient** without it.

### 2. Clear git's ownership check on a virtiofs mount

A `--mount`ed repo is owned by a host uid the guest does not recognise, so every
git command fails with `detected dubious ownership`:

```sh
git config --global --add safe.directory /work
```

### 3. Put the Go toolchain on PATH

Go is installed at `/usr/local/go/bin` but is absent from the login-shell PATH:

```sh
export PATH=$PATH:/usr/local/go/bin
```

---

## Trap: `go test` exits 0 in-guest without running anything

Five packages detect that they are running inside a nexus3 guest — via
`/proc/1/comm` == `nexus3-agent` — and call `os.Exit(0)` **before** `m.Run()`, so
a nested test run cannot pollute the operator's real state directory:

- `internal/cli`
- `internal/core/service`
- `internal/core/recovery`
- `internal/core/perimeter/netstack`
- `internal/mcp`

The consequence is a green exit code that asserts nothing:

```sh
go test -count=1 ./internal/cli/...   # ok ... 0.015s — ZERO test bodies ran
```

Two tells. Each package prints a line to **stderr** before exiting:

```
cli: skipping tests — running inside nexus3 guest VM (host-side package)
```

and the timing stays a suspiciously-fast `0.0Xs` regardless of which `-run`
filter is applied. `go test` prints `ok` either way, so the stderr line is the
reliable signal — check for it before believing an in-guest pass.

To get a real signal in-guest, give the test binary its own PID namespace so
`/proc/1/comm` no longer reads `nexus3-agent`:

```sh
unshare --pid --mount-proc --fork -- env TMPDIR=/tmp go test -count=1 ./internal/cli/...
```

Use this only for pure-filesystem packages. Packages that spawn real
hypervisor or supervisor machinery (`internal/core/driver/cloudhypervisor`,
`internal/core/service`) are skipped deliberately — forcing them to run
in-guest surfaces unrelated environment failures.

**When verification must be trustworthy, run it on the host**, where `TestMain`
does not skip. An in-guest green is not evidence unless it was produced one of
these two ways.

---

## Projecting the host agent setup into a sandbox

An in-guest agent starts with **none** of the operator's configuration — no
`CLAUDE.md`, no skills, no commands. It works, but blind to every operating rule
the host agent follows. Project the config in explicitly.

### Never mount `~/.claude` wholesale

`~/.claude` contains `.credentials.json`. Mounting the directory carries real
credential material into the guest and breaks the zero-cred-in-guest invariant
(AC-7). The guest is supposed to hold only the placeholder token that the
host-side MITM proxy swaps per request.

Project a **curated subset** instead:

```sh
nexus3 create <project>/<name> --image <digest> --agent claude-code \
  --mount /path/to/checkout:/work \
  --mount ~/.claude/skills:/root/.claude/skills:ro \
  --mount ~/.claude/commands:/root/.claude/commands:ro \
  ...

# --mount is directory-only, so single files go via cp:
nexus3 cp <project>/<name> ~/.claude/CLAUDE.md guest:/root/.claude/CLAUDE.md
```

Mount these **read-only**. The guest has no reason to write to the operator's
configuration, and `:ro` makes that structural rather than a convention.

| Path | Project? | Why |
|------|----------|-----|
| `CLAUDE.md` | yes (~4K) | the operating rules the agent should follow |
| `skills/` | yes (~724K) | skills the agent needs |
| `commands/` | yes (~12K) | small, harmless |
| `plugins/` | **no** (~424M) | far too large; ships binaries |
| `.credentials.json` | **never** | real credential material |
| `sessions/`, `projects/` | no | host history, no value in-guest |

After creating a sandbox this way, confirm the boundary held:

```sh
nexus3 exec <ref> -- /usr/bin/bash -lc 'ls /root/.claude/.credentials.json'
# MUST report: No such file or directory
```

Treat that check as mandatory, not optional. It is the one assertion that
distinguishes a projected config from a leaked credential.

### Workspace trust

A fresh guest has no `~/.claude.json`, so `claude` blocks on the workspace-trust
prompt before doing any work. Seed it:

```json
{
  "projects": { "/work": { "hasTrustDialogAccepted": true } },
  "hasCompletedOnboarding": true
}
```

Running `claude --dangerously-skip-permissions` as root (the guest default user)
additionally requires `IS_SANDBOX=1`, and still shows a one-time interactive
consent prompt that must be accepted before the agent starts.

---

## Known limitation: new tabs in a nexus3 herdr space open a HOST shell

Opening a new tab in a nexus3-created herdr space launches a shell on the
**host**, not in the sandbox — confusing, since the workspace represents a
sandbox.

This cannot be fixed in nexus3. The herdr plugin ABI has no per-workspace
default entrypoint: `entrypoint` exists only on `PluginPaneOpenParams` (a
per-open argument), while `WorkspaceCreateParams` and `TabCreateParams` carry
`{cwd, env, focus, label}` only. Closing it needs an upstream herdr feature.

To get another **guest** shell, open a plugin pane rather than a plain tab:

```sh
nexus3 herdr space-open-pane <sandbox-ref>
```

## Starting an agent inside a sandbox

One command does the whole thing:

```
nexus3 herdr agent [--autonomous] <sandbox-ref> "<brief>"
```

It starts the sandbox, creates or reuses its herdr space, opens the guest
pane, launches claude, waits for the prompt, and types the brief. It is also a
herdr action (**nexus3: launch Claude agent in sandbox**), which prompts for
the ref, the brief, and whether to run autonomously.

The sandbox must have source mounted. `herdr agent` refuses one that does not,
because an agent with nothing to work on looks identical to a healthy agent.

`--autonomous` adds `--dangerously-skip-permissions` so the agent acts without
asking approval per tool call. It is off by default and always asked, never
assumed: the flag's safety argument is entirely that the blast radius is a
disposable microVM, so it is only sound when the operator knows what they
mounted. A sandbox with a real working tree mounted read-write is not a
disposable blast radius.

### Driving an agent by hand

`herdr agent start` **cannot** drive an in-guest agent. It validates the
*host-side* pane foreground process, which for a guest pane is the
`nexus3 exec --pty` wrapper, so it refuses with `agent_pane_busy`. This is a
boundary, not a bug. Use the pane API: `send-text`, `send-keys`, `read`,
`wait-output`.

Three traps, each of which produces something that looks like a working agent:

- **`herdr pane run` is wrong for a TUI.** It sends the text and Enter in one
  call. Against a shell that is fine; against claude the text lands in the
  input box and *sits there unsubmitted*. Send the text, pause, then send
  `Enter` separately.
- **There is no single "agent is ready" token.** The footer differs by
  permission mode — `? for shortcuts` in the default mode,
  `shift+tab to cycle` under `--dangerously-skip-permissions`. Match the one
  for the mode you launched. Do **not** match the prompt glyph `❯`: it is also
  every wizard's selector glyph, so it reports ready mid-dialog.
- **`send-keys` key names**: `ctrl+c` and `C-c` work; `ctrl-c` and `^C` are
  rejected as invalid.

### First-run wizards

A guest claude walks up to **four** wizards before reaching its prompt. nexus3
seeds past all of them, but if you build a guest by hand, these are the keys:

| wizard | file | key |
|---|---|---|
| theme picker | `~/.claude.json` | `theme` |
| login method | `~/.claude.json` | `hasCompletedOnboarding` |
| folder trust | `~/.claude.json` | `projects[<dir>].hasTrustDialogAccepted` |
| bypass-permissions consent | `~/.claude/settings.json` | `skipDangerousModePermissionPrompt` |

The fourth lives in a **different file** and appears only with
`--dangerously-skip-permissions`.

The login wizard is the deceptive one: it appears when onboarding is
incomplete *even though the credential is present and correct*. Reaching
"Select login method" is not evidence of a credential problem. Check
`bash -lc 'env | grep CLAUDE_CODE_OAUTH_TOKEN'` in the guest before concluding
anything about credentials.

### What the guest image has

`node` is present (claude is a node program). `python3`, `python` and `jq` are
**absent** — write guest-side JSON manipulation in node.

---

## Creating a GitHub pull request from inside a sandbox

The MITM perimeter is the only egress boundary for in-guest GitHub traffic.
It allowlists REST endpoints and **denies GraphQL** fail-closed (HTTP 403).

**`gh pr create` uses a GraphQL mutation. Do not use it — it returns 403.**

Other `gh` subcommands that use GraphQL are also blocked. Notably, `gh repo view --json` returns 403; to read repo context over REST use `gh api repos/{owner}/{repo}` instead (e.g. `gh api repos/{owner}/{repo} --jq .default_branch` to read the default branch, or `--jq .owner.login` / `--jq .name` for owner and repo name).

### Push first

The branch must exist on the remote before opening the PR. Branch names must
match the perimeter's push allowlist (typically `nexus3/<project>/<name>` or
the pattern the operator configured); the perimeter denies any push to a
disallowed ref pattern.

```sh
git push origin HEAD
```

### Create the PR over REST

```sh
gh api -X POST repos/{owner}/{repo}/pulls \
  -f title="<title>" \
  -f head="<branch>" \
  -f base="<base-branch>" \
  -f body="<body>"
```

`gh api` expands `{owner}` and `{repo}` from the local git remote automatically.
A successful call returns HTTP 201 with an `html_url` field pointing to the new PR.

**Draft PRs**: add `-F draft=true` only if the target repo supports drafts.
Some private repos do not — a hardcoded `draft=true` returns HTTP 422 there.
Omit it for a regular PR.

### Stacked PRs

`gh stack submit` works if the `gh-stack` extension is present — it creates
PRs over REST, not GraphQL, so the perimeter allows it.

### Attaching a GitHub credential

Pass `--repo owner/name` together with `--secret GH_TOKEN@github.com,api.github.com,uploads.github.com` at create time. There is no longer a built-in GitHub token — sandboxes default to no GitHub credential (fail-closed). The `--no-builtin-gh` flag was removed (D-PDE-02); one mechanism: `--secret`.

```sh
nexus3 create myproject/sandbox \
  --repo myorg/myrepo \
  --secret GH_TOKEN@github.com,api.github.com,uploads.github.com \
  --image nexus3-agent-base
```

## Sharing your host agent setup into sandboxes (user mounts)

nexus3 has **no built-in default mounts** — it ships no hardcoded tool list. All host-to-guest sharing is driven entirely by user config. Pass `--no-user-mounts` on a `create` call to suppress all user-global mounts for that sandbox.

### User-global config: `~/.config/nexus3/config.yaml`

The file lives at `$XDG_CONFIG_HOME/nexus3/config.yaml` (falls back to `~/.config/nexus3/config.yaml` when `$XDG_CONFIG_HOME` is unset). It uses `version: 1` and the `sandbox.mounts` key.

Both **short form** (`host:guest[:ro]`) and **long form** (`{source, target, read_only}`) are accepted — same syntax as Docker Compose bind-mounts. `~` and `$HOME` expand against the operator's home directory.

```yaml
version: 1

sandbox:
  mounts:
    # Short form: host:guest:ro
    - ~/.claude/plugins:/root/.claude/plugins:ro
    - ~/.local/bin:/root/.local/bin:ro
    - ~/.local/share/mise:/root/.local/share/mise:ro
    - ~/.local/share/uv:/root/.local/share/uv:ro
    - ~/.bun:/root/.bun:ro
    - ~/.vscode-server/extensions:/root/.vscode-server/extensions:ro
    - ~/.local/share/groundwork:/root/.local/share/groundwork:ro
    # Long form (exercises both YAML shapes)
    - source: ~/.codegraph
      target: /root/.codegraph
      read_only: true
```

A parse error in the config file is logged and the sandbox starts without user mounts — `sandbox create` never fails due to a bad config.

Host and guest are both **Linux x86_64**, so mounted ELF binaries and shared libraries run without translation.

### Diagnosis: my tool / plugin / MCP isn't working

Run these two commands against a running sandbox:

```sh
nexus3 exec <ref> -- sh -lc 'claude mcp list'
nexus3 exec <ref> -- sh -lc 'claude plugin list'
```

Interpret the output:

| Symptom | Cause | Fix |
|---|---|---|
| `ENOENT` / not found in `$PATH` | Tool binary or shim dir not in guest | Add a `sandbox.mounts` entry for the tool's install dir |
| Plugin `cache-miss` | Marketplace source dir absent in guest | Add a `sandbox.mounts` entry for the source dir |
| MCP server crashes / exits immediately | Server binary resolves but runtime env missing | Add a `sandbox.mounts` entry for the runtime (e.g. the `uv` or `mise` tree the server's wrapper invokes) |

### Security boundary

Never add credential directories to `sandbox.mounts`. All virtiofs mounts are host-read-only inside the guest, but read-only is not the same as invisible — an in-guest agent can read anything mounted. The following must never appear in user config:

- `~/.ssh`
- `~/.config/gh`
- `~/.claude.json` / `~/.claude/.credentials.json`
- `~/.aws`, `~/.config/gcloud`, or any provider credential store

The security model rests on "tool payloads, never credential stores."

---

## GitHub/VCS egress for worktree sandboxes

Worktree sandboxes are created automatically by the herdr plugin when an agent opens a pane. They inherit egress rules from the **operator-controlled** `nexus3.yaml` at the repo root — not from the worktree's checked-out branch. This section teaches a fresh agent how to declare the right egress entry so the operator can ratify it.

> **Real token never enters the guest.** The MITM proxy swaps a 64-hex placeholder for the real bearer on the wire (PDF-R-020). The env var the guest sees is always the placeholder, not the real credential.

### Step 1 — Discover your provider, host, and repo identity

Run this inside your checkout:

```sh
git remote get-url origin
```

Normalize the URL to `host` + `owner/name`:

| Remote URL form | Host | Owner/name |
|---|---|---|
| `https://github.com/acme/myrepo.git` | `github.com` | `acme/myrepo` |
| `git@github.com:acme/myrepo.git` | `github.com` | `acme/myrepo` |
| `https://gitlab.com/acme/myrepo.git` | `gitlab.com` | `acme/myrepo` |
| `git@gitlab.example.com:acme/myrepo.git` | `gitlab.example.com` | `acme/myrepo` |
| `https://bitbucket.org/acme/myrepo.git` | `bitbucket.org` | `acme/myrepo` |

If the project has no git remote or uses a non-git VCS, determine the host explicitly and declare it in `hosts:`.

### Step 2 — Author the `egress.policy` and `egress.secrets` entries in `nexus3.yaml`

`egress.policy` and `egress.secrets` live in `nexus3.yaml` at the repo root (same file as `sandbox.image`, `egress.allow`, etc.). Add or extend the `egress` key; do not create a separate file.

#### GitHub

GitHub hosts **require** a policy entry. Without one, `sandbox create` is refused with a hard error. List the specific API paths your workflow needs under `paths:`. The MITM proxy enforces default-deny — only listed paths are forwarded; cross-repo API calls and GraphQL are denied by omission.

```yaml
version: 1
egress:
  policy:
    - host: github.com
      paths: ["/acme/myrepo/**"]
    - host: api.github.com
      paths: ["/repos/acme/myrepo/**", "/repos/acme/myrepo", "/user"]
    - host: uploads.github.com
      paths: ["/**"]
  secrets:
    - env: GH_TOKEN
      hosts: [github.com, api.github.com, uploads.github.com]
```

Cross-repo API calls and `gh api graphql` (GraphQL) are denied (403) by default because they are not listed in the path allowlist — not in the configuration entries.

> **SECURITY WARNING — GitHub glob tightness is the author's responsibility.**
> The `egress.policy` layer is generic default-deny globs; the system cannot automatically narrow what you write. When authoring GitHub entries you MUST:
>
> - **Scope every `api.github.com` path to `/repos/<owner>/<repo>/**`** (plus specific endpoints like `/repos/<owner>/<repo>` and `/user`). Do **not** write `/**` or `/` at the root — that would let a compromised guest reach other repos and perform any API action with the operator's full-scope token.
> - **Do not list `/graphql`** under `api.github.com`. GraphQL is a parallel write channel that bypasses path-allowlist semantics; listing it reopens the sole-bound risk documented in the security notes (`nexus3-github-token-sole-bound`). The unconditional `/graphql` backstop only fires on the legacy CLI `GitHubPolicy` path — it does **not** apply to config-authored globs.
> - **Do not list `/**` under `github.com`** — that exposes all repository HTML and raw download paths across the entire platform.
>
> An unscoped or `/graphql`-listing GitHub glob **reopens the sole-bound risk**: the operator's unrotated full-scope token becomes the only protection. Treat the path list as the security perimeter, not as a convenience filter.
>
> A stricter parse-time graphql lint (automatic floor for config-authored globs) is available as an optional follow-up (TBD) if the operator wants the hard block back.

Short form (equivalent for secrets, no path policy support — **not valid for github hosts** because policy entries are mandatory):

```yaml
egress:
  secrets:
    - GH_TOKEN@github.com,api.github.com,uploads.github.com   # REJECTED: missing policy entries
```

Do not use the short form for GitHub. Use the long form with policy entries and secrets.

#### GitLab (cloud or self-hosted)

Non-GitHub hosts have no mandatory path policy. A project access token scoped to the specific project is strongly preferred over a full personal access token.

```yaml
version: 1
egress:
  secrets:
    - env: GITLAB_TOKEN
      hosts: [gitlab.com]
```

For a self-hosted instance:

```yaml
version: 1
egress:
  secrets:
    - env: GITLAB_TOKEN
      hosts: [git.example.com]   # adjust to your instance hostname
```

Short form also accepted for non-GitHub hosts:

```yaml
egress:
  secrets:
    - GITLAB_TOKEN@gitlab.com
```

#### Generic — path-restricted API token

Use `paths:` in a policy entry to restrict which URL paths the token is forwarded to:

```yaml
version: 1
egress:
  policy:
    - host: api.example.com
      paths: ["/v4/projects/123/**"]
  secrets:
    - env: API_TOKEN
      hosts: [api.example.com]
```

Paths are anchored globs. An optional `METHOD ` prefix restricts to one HTTP verb (e.g. `"GET /v4/projects/123/**"`).

### Step 3 — Trust anchor: propose → merge → ratify

**Critical.** The worktree sandbox launch reads `nexus3.yaml` from `refs/remotes/origin/HEAD` (the operator's default branch), never from the agent's checked-out branch.

Workflow:

1. Agent authors `nexus3.yaml` on its feature branch and opens a PR.
2. The PR branch config grants **nothing** — the sandbox launches without the declared egress.
3. Operator reviews and merges to the default branch.
4. Thereafter, every new worktree sandbox inherits the egress rule from the merged config.

A config on a PR branch only is **not a security gap** — it is the intended trust boundary. The operator's merge is the ratification act.

### Step 4 — Verify the egress is working (after the operator merges)

Open a shell in a worktree sandbox and run the provider-appropriate check:

**GitHub:**

```sh
# Own-repo REST call → 200 expected
gh api repos/acme/myrepo --jq .full_name

# Confirm placeholder — must be 64 hex chars, NOT the real token
echo $GH_TOKEN | grep -E '^[0-9a-f]{64}$' && echo "placeholder OK" || echo "FAIL: real token exposed"

# Cross-repo call → 403 expected (path policy enforced)
gh api repos/some-other-org/other-repo --jq .id && echo "UNEXPECTED 200 — check path policy" || echo "403 OK"

# GraphQL → 403 expected (GraphQL is denied fail-closed)
gh api graphql -f query='{ viewer { login } }' && echo "UNEXPECTED 200" || echo "403 OK"
```

**GitLab:**

```sh
# Verify token reaches the instance and is accepted
curl -sf -H "Authorization: Bearer $GITLAB_TOKEN" \
  https://gitlab.com/api/v4/projects/acme%2Fmyrepo | jq .id

# Confirm placeholder
echo $GITLAB_TOKEN | grep -E '^[0-9a-f]{64}$' && echo "placeholder OK" || echo "FAIL"
```

**Generic API token:**

```sh
# Declared host → token forwarded, request should succeed
curl -sf -H "Authorization: Bearer $API_TOKEN" https://api.example.com/v4/projects/123/

# Non-declared host → token NOT forwarded (placeholder sent, rejected)
curl -sf -H "Authorization: Bearer $API_TOKEN" https://other.example.com/ || echo "rejected OK"
```

### Manual sandbox create (non-worktree) with a VCS secret

Outside the herdr worktree flow, pass `--secret` explicitly. GitHub additionally requires `--repo`:

```sh
# GitHub
nexus3 sandbox create myproject/dev-1 \
  --image nexus3-agent-base \
  --secret GH_TOKEN@github.com,api.github.com,uploads.github.com \
  --repo acme/myrepo

# GitLab
nexus3 sandbox create myproject/dev-1 \
  --image nexus3-agent-base \
  --secret GITLAB_TOKEN@gitlab.com
```

There is no longer a built-in GitHub token; `--repo` alone without `--secret` does not inject a credential (D-PDE-02). Pass both, or declare both in `nexus3.yaml` for automatic wiring via the worktree flow.
