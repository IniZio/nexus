---
title: "Mounts and worktrees"
description: "Mount a git worktree per sandbox so edits inside the VM appear on the host tree in real time"
---

# Mounts and worktrees <Badge type="danger" text="not built" />

> Mount a host git worktree as the live workspace — edits inside the sandbox appear on the host immediately, and work flows back through normal git push.

Live virtiofs mounts replace workspace capture. There is no archive step and no extraction step.

**1. Create a dedicated git worktree**

```sh
git -C /data/repos/myrepo worktree add /data/repos/myrepo-dev1 feat/my-branch
```

**2. Boot the sandbox with the worktree attached** <Badge type="danger" text="not built" />

```sh
nexus3 create myproject/dev-1 \
  --context /data/repos/myrepo-dev1 \
  -v /data/repos/myrepo-dev1:/workspace/myrepo \
  --memory 8192
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

<Badge type="warning" text="partial" /> — current implementation uses `--file`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

`--context` locates `.nexus/Containerfile` for the rootfs build. `-v host-path:guest-path`
(repeatable; add `:ro` for read-only) attaches the host directory as a live virtiofs volume at
the guest path.

## Git identity in mounted worktrees

When a host git worktree is mounted into the sandbox, git operations inside the guest use the **repo-local git config** from `.git/config` in that repository — not the host's global `~/.gitconfig`. This means:

- `user.name` and `user.email` set via `git config --local` in the worktree apply in-guest automatically.
- The host's `~/.gitconfig` does **not** reach the guest unless you mount it explicitly:

  ```
  nexus3 create myproject/dev-1 \
    -v /data/repos/myrepo:/workspace/myrepo \
    -v ~/.gitconfig:/root/.gitconfig:ro \
    --image nexus3-base:20260807
  ```

  This is optional — if the worktree's repo-local config already has the identity you want, no extra mount is needed.

**3. Run work inside the sandbox**

```sh
nexus3 exec myproject/dev-1 -- go test ./...
nexus3 exec myproject/dev-1 -- git add -A
nexus3 exec myproject/dev-1 -- git commit -m "feat: implement foo"
```

Every write inside the guest appears in the host worktree immediately — no sync step.

**4. Push from the host**

Credentials stay on the host, not in the guest:

```sh
git -C /data/repos/myrepo-dev1 push origin feat/my-branch
```

Multiple mounts are also supported — for example, a read-only shared config alongside a writable
source tree:

```sh
nexus3 create myproject/dev-1 \
  --context /data/repos/myrepo \
  -v /data/repos/myrepo:/workspace/myrepo \
  -v /data/shared/secrets:/run/secrets:ro \
  --memory 8192
```

---

## Shadow disks <Badge type="danger" text="not built" />

Write-heavy paths (package managers, build caches, compiler outputs) perform poorly on a virtiofs
volume because every metadata operation crosses a virtio channel. Shadow disks back the named guest
path with a **per-sandbox sparse ext4 virtio-blk image** that lives entirely inside the guest's
disk space.

### Default shadow paths <Badge type="danger" text="not built" />

When `--shadow` is not specified, these paths under any `-v` target are automatically backed
by shadow disks:

| Guest path (relative to mount root) | Typical owner |
|---|---|
| `node_modules` | npm / pnpm / yarn |
| `.next` | Next.js build cache |
| `target` | Rust / Maven / Gradle |
| `dist` | bundlers |

The guest sees these as ordinary directories; the shadow binding is transparent to all tooling.

### Declaring additional shadow paths <Badge type="danger" text="not built" />

Use `--shadow` to add paths beyond the defaults. The path must be a subdirectory of a declared
`-v` target.

```sh
nexus3 create myproject/dev-1 \
  --context /data/repos/monorepo \
  -v /data/repos/monorepo:/workspace/monorepo \
  --shadow /workspace/monorepo/packages/web/node_modules \
  --shadow /workspace/monorepo/packages/api/node_modules \
  --memory 16384
```

### Shadow disk lifecycle

| Event | Workspace disk | Shadow disks |
|---|---|---|
| `create` | created from host dir | created as empty sparse images |
| `stop` | persisted | persisted |
| `rm` | deleted | deleted |

Shadow disks do **not** survive `rm`. Build artifacts and package trees are ephemeral
per sandbox. Durable work flows back via git push, not file copy.

---

## Fork and snapshot restrictions <Badge type="danger" text="not built" />

`fork` and `snapshot create` are **refused** on a live-mounted sandbox and return an
explicit error.

- **Fork refused**: two VMs would share one worktree and one `.git/index.lock`, producing
  concurrent writes to the same tree with no coordination.
- **Snapshot refused**: the mounted tree lives on the host and is not captured inside the snapshot;
  restoring would resume memory state against files that have changed underneath it.

For N-way parallel work, use **independent creates** — each sandbox gets its own `git worktree`
directory. See [Parallel development flow](parallel-dev-flow.md).

---

## See also

- [Parallel development flow](parallel-dev-flow.md)
- [Building images](building-images.md)
- [Surface reference — `create`](/cli/#commands)
