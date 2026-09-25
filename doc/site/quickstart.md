---
title: "Quickstart"
description: "From nothing to a running sandbox in minutes"
---

# Quickstart

> First sandbox in minutes, then a full create → exec → forward → stop round trip.

nexus is a microVM sandbox manager. Each sandbox is an isolated Cloud Hypervisor VM with its own kernel, disk, and network. The CLI and MCP server expose the same primitives.

## Install <Badge type="tip" text="built" />

Installing the herdr plugin is the install path. One command provisions nexus
itself on Linux x86-64:

```sh
herdr plugin install IniZio/nexus/plugins/herdr
```

The plugin's build hook downloads the pinned `nexus-linux-amd64` binary from
GitHub Releases, verifies the SHA256 checksum, installs it to
`~/.local/bin/nexus`, and runs `nexus herdr install-default-shell`.

**One manual step.** Paste the printed line into `~/.config/herdr/config.toml`:

```toml
[terminal]
default_shell = ~/.local/bin/nexus-guest-shell
```

herdr's config is user-owned and is not written automatically.

**Platform matrix:**

| Platform | Install method |
|---|---|
| Linux x86-64 | `herdr plugin install IniZio/nexus/plugins/herdr` (binary bundled) |
| macOS (any arch) | Build from source, then wire manually (see below) |
| Linux arm64 | Build from source, then wire manually (see below) |

**macOS / Linux arm64 — build from source:**

```sh
git clone https://github.com/IniZio/nexus
cd nexus && go build -o ~/.local/bin/nexus ./cmd/nexus
nexus herdr install-default-shell
# paste the printed line into ~/.config/herdr/config.toml
NEXUS_LOCAL=1 herdr plugin install /path/to/nexus/plugins/herdr
```

**Without herdr — build from source directly:**

```sh
git clone https://github.com/IniZio/nexus
cd nexus
go build -o nexus ./cmd/nexus
```

### Claude Code plugin

With `nexus` on your `PATH`, add the plugin so Claude Code gets the `nexus`
skill, the `/nexus:nexus-init` and `/nexus:nexus-delegate` commands, and the
MCP server:

```sh
claude plugin marketplace add IniZio/nexus
claude plugin install nexus@nexus
```

See [Claude Code plugin](./recipes/claude-plugin.md) for what it provides.

---

::: info Prerequisites
- **Linux x86-64** with KVM enabled — `/dev/kvm` must be readable by your user
- **`cloud-hypervisor`** on `$PATH` — verify with `cloud-hypervisor --version`
- **`nexus`** binary on `$PATH` — build from source above

If `/dev/kvm` is not accessible: `sudo usermod -aG kvm $USER` then re-login.
:::

Verify your setup:

```
nexus doctor
```

---

## Your first sandbox

**Option A — run a stock OCI image directly (no build needed):**

```
nexus run alpine:3.20 -- sh -c 'echo hello; cat /etc/os-release | head -1'
```

Standard Docker Hub / OCI images are pulled on demand and cached by ref. Second run hits the cache.

**Option B — build a custom image first:**

```
nexus image build --workspace . --ref nexus-base:20260807
```

`--workspace` is the directory containing `.nexus/Containerfile`. See [Building images](/recipes/building-images) for Containerfile details. The examples below use `nexus-base:20260807`.

### 1. Create and boot

```
nexus create myproject/hello --image nexus-base:20260807 --memory 2048
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

`<project>/<name>` is the sandbox reference used in every subsequent command. The command blocks until the in-guest agent is reachable.

### 2. Run a command

```
nexus exec myproject/hello -- uname -r
```

stdout and stderr stream back over vsock.

### 3. Open an interactive shell

```
nexus exec myproject/hello
```

With no trailing command and stdin on a terminal, `exec` opens an interactive PTY session. <Badge type="danger" text="not built" /> — today `exec` is non-interactive by default; pass `--pty` to allocate a PTY. Type `exit` to leave; the sandbox keeps running.

### 4. Forward a port

```
nexus forward myproject/hello 8080:8080
```

Then open `http://localhost:8080` on the host.

### 5. Stop and remove

```
nexus stop myproject/hello
nexus rm myproject/hello
```

`rm` deletes the record and all disk resources. A running sandbox must be stopped first.

---

## Ephemeral one-shot with `run` <Badge type="tip" text="built" />

`run` creates, boots, runs, and removes the sandbox unconditionally on exit — including on Ctrl-C or a crash. The sandbox is gone when `run` returns.

Run a stock OCI image directly — no pre-build required:

```
nexus run alpine:3.20 -- sh -c 'echo hello; uname -r'
```

Or run against a custom-built image:

```
nexus run --memory 2048 nexus-base:20260807 -- go test ./...
```

---

## Labeling sandboxes

Labels are arbitrary key-value pairs stamped at creation and used to select sandboxes in bulk:

```
nexus create myproject/worker-1 --image nexus-base:20260807 \
  --label team=payments --label env=ci
```

`ps --label KEY=VALUE` filters by label, AND-matched when repeated. Labels carry no reserved semantics — they are plain metadata for your own queries and tooling.

See [Labels and selectors](/cli/#labels-and-selectors).

---

## Named volumes

Attach persistent named volumes to a sandbox so write-heavy directories (dependency stores, build caches, Docker data) survive across sandbox removes and are shared across concurrent sandboxes:

```
nexus volume create myapp-node_modules --kind disk --size 10737418240
nexus create myproject/dev-1 \
  --image myapp-base:latest \
  --mount-named myapp-node_modules:/workspace/myapp/node_modules \
  --memory 8192
```

`nexus volume create` is optional — `--mount-named` auto-creates the volume on first attach (kind=disk, 10 GiB default). The volume persists after `nexus rm`; delete it explicitly with `nexus volume rm myapp-node_modules`.

See [Volume commands](/cli/volume-commands) and [CLI — Named volumes](/cli/sandbox-commands#named-volumes).

## Live virtiofs worktree mount

`--mount <host-path>:<guest-path>[:ro]` mounts a host directory into the sandbox as a live virtiofs share — the sandbox sees dirty files, untracked files, and unpushed commits without a capture step (D-PD-53).

```
nexus create myproject/dev-1 \
  --image nexus-base:20260807 \
  --mount /path/to/myrepo:/workspace/myrepo \
  --memory 8192
```

See [Mounts and worktrees](/recipes/mounts-and-worktrees) for the full workflow.

---

## What's next

- [AI agents](/ai-agents) — MCP server and per-task orchestration
- [Parallel dev flow](/recipes/parallel-dev-flow) — N sandboxes in parallel
- [Building images](/recipes/building-images) — custom guest images from a Containerfile
- [Docker in a sandbox](/recipes/docker-in-sandbox) — Compose stacks inside nexus
- [CLI reference](/cli/) — complete command surface
