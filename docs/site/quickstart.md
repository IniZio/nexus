---
title: "Quickstart"
description: "From nothing to a running sandbox in minutes"
---

# Quickstart

> First sandbox in minutes, then a full create → exec → forward → stop round trip.

nexus3 is a microVM sandbox manager. Each sandbox is an isolated Cloud Hypervisor VM with its own kernel, disk, and network. The CLI and MCP server expose the same primitives.

## Install <Badge type="danger" text="not built" />

The target provides a one-line installer:

```
curl -fsSL https://raw.githubusercontent.com/inizio/nexus3/main/scripts/install.sh | sh
```

**What works today — build from source:**

```
git clone https://github.com/inizio/nexus3
cd nexus3
go build -o nexus3 ./cmd/nexus3
```

---

::: info Prerequisites
- **Linux x86-64** with KVM enabled — `/dev/kvm` must be readable by your user
- **`cloud-hypervisor`** on `$PATH` — verify with `cloud-hypervisor --version`
- **`nexus3`** binary on `$PATH` — build from source above

If `/dev/kvm` is not accessible: `sudo usermod -aG kvm $USER` then re-login.
:::

Verify your setup:

```
nexus3 doctor
```

---

## Your first sandbox

The examples below use `nexus3-base:20260807`. Build it first if you don't have it:

```
nexus3 image build --workspace . --ref nexus3-base:20260807
```

`--workspace` is the directory containing `.nexus/Containerfile`. See [Building images](/recipes/building-images) for Containerfile details.

### 1. Create and boot

```
nexus3 create myproject/hello --image nexus3-base:20260807 --memory 2048
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

`<project>/<name>` is the sandbox reference used in every subsequent command. The command blocks until the in-guest agent is reachable.

### 2. Run a command

```
nexus3 exec myproject/hello -- uname -r
```

stdout and stderr stream back over vsock.

### 3. Open an interactive shell

```
nexus3 exec myproject/hello
```

With no trailing command and stdin on a terminal, `exec` opens an interactive PTY session. <Badge type="danger" text="not built" /> — today `exec` is non-interactive by default; pass `--pty` to allocate a PTY. Type `exit` to leave; the sandbox keeps running.

### 4. Forward a port

```
nexus3 forward myproject/hello 8080:8080
```

Then open `http://localhost:8080` on the host.

### 5. Stop and remove

```
nexus3 stop myproject/hello
nexus3 rm myproject/hello
```

`rm` deletes the record and all disk resources. A running sandbox must be stopped first.

---

## Ephemeral one-shot with `run`

```
nexus3 run --memory 2048 nexus3-base:20260807 -- go test ./...
```

`run` creates, boots, runs, and removes the sandbox unconditionally on exit — including on Ctrl-C or a crash. The sandbox is gone when `run` returns.

---

## Labeling sandboxes

Labels are arbitrary key-value pairs stamped at creation and used to select sandboxes in bulk:

```
nexus3 create myproject/worker-1 --image nexus3-base:20260807 \
  --label team=payments --label env=ci
```

`ps --label KEY=VALUE` filters by label, AND-matched when repeated. Labels carry no reserved semantics — they are plain metadata for your own queries and tooling.

See [Labels and selectors](/cli/#labels-and-selectors).

---

## Mounting a working tree <Badge type="danger" text="not built" />

The target surface lets you mount a host directory into the guest as a named disk, so the sandbox sees your live working tree — dirty files, untracked files, and unpushed commits included — without a capture step:

```
nexus3 create myproject/dev-1 \
  --context /data/repos/myrepo \
  -v /data/repos/myrepo:/workspace/myrepo \
  --memory 8192
```

<Badge type="warning" text="partial" /> — `--context` current implementation uses `--file`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

`-v host-path:guest-path` (repeatable) attaches the host directory as a virtio-blk ext4 disk. Build-artifact directories (`node_modules`, `target`, `.next`, `dist`) are automatically backed by per-sandbox shadow disks so writes do not flow back to the host.

If the image declares startup services — or you pass `--service` at create time — the create command blocks until all readiness probes pass:

```
nexus3 create myproject/dev-1 \
  --context /data/repos/myrepo \
  -v /data/repos/myrepo:/workspace/myrepo \
  --service 'dockerd:dockerd:docker info' \
  --memory 8192

nexus3 exec myproject/dev-1 -- docker ps
```

See [Mounts and worktrees](/recipes/mounts-and-worktrees), [CLI — Mounts and shadow disks](/cli/#mounts-and-shadow-disks), [CLI — Startup services](/cli/#startup-services).

---

## What's next

- [AI agents](/ai-agents) — MCP server and per-task orchestration
- [Parallel dev flow](/recipes/parallel-dev-flow) — N sandboxes in parallel
- [Building images](/recipes/building-images) — custom guest images from a Containerfile
- [Docker in a sandbox](/recipes/docker-in-sandbox) — Compose stacks inside nexus3
- [CLI reference](/cli/) — complete command surface
