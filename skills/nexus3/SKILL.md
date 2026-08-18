---
name: nexus3
description: Reference skill for the nexus3 microVM sandbox tool — CLI commands, sandbox lifecycle, and volume configuration for write-heavy project directories.
---

# nexus3

nexus3 creates and manages isolated Firecracker microVMs (sandboxes). Each sandbox has its own network, filesystem, and process namespace.

## Sandbox lifecycle

```sh
nexus3 create [--file <Dockerfile>] [--mount-named <name>:<guest-path>[:<options>]] ...
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
