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
nexus3 __herdr-plugin space-open-pane <sandbox-ref>
```
