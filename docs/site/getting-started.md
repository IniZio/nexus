# Getting started with nexus3

nexus3 is a microVM sandbox manager. Each sandbox is an isolated Cloud Hypervisor VM with its own
disk, network namespace, and an in-guest agent you talk to over vsock. The CLI and an MCP tool
server expose the same primitives.

This guide takes you from nothing to a running sandbox in ten minutes, then to a full round-trip
of create → exec → inspect → remove.

---

## Prerequisites

| Requirement | Notes |
|---|---|
| Linux x86-64 host | KVM must be enabled (`/dev/kvm` readable by your user) |
| Cloud Hypervisor | `ch-remote` and `cloud-hypervisor` on `$PATH` |
| `nexus3` binary | On `$PATH`; built via `go build ./cmd/nexus3` |
| A guest image | See [Building images](guides/building-images.md) or use a pre-built ext4 image |

Verify your setup:

```
nexus3 doctor
```

If `doctor` reports `/dev/kvm` not accessible, add your user to the `kvm` group and re-login:

```
sudo usermod -aG kvm $USER
```

---

## Your first sandbox

### 1. Create and boot

```
nexus3 sandbox create myproject/hello --image nexus3-base:20260807 --memory 2048
```

`<project>/<name>` is the sandbox reference used in every subsequent command. The `--image` flag
triggers create-and-boot: the command blocks until the in-guest agent is reachable.

Without `--image` the record is created in state `created` but no VM starts — useful for
pre-allocating records before disk preflight checks.

### 2. Run a command

```
nexus3 exec myproject/hello -- uname -r
```

`exec` dispatches to the in-guest agent over vsock. Stdout and stderr are streamed back.

### 3. Open an interactive shell

```
nexus3 shell myproject/hello
```

This opens a login shell. Type `exit` to leave; the sandbox keeps running.

### 4. Forward a port

If your in-guest process listens on port 8080:

```
nexus3 forward myproject/hello 8080:8080
```

Then open `http://localhost:8080` on the host.

### 5. Stop and remove

```
nexus3 sandbox stop myproject/hello
nexus3 sandbox rm myproject/hello
```

`rm` deletes the record and all disk resources. A running sandbox must be stopped first.

---

## Ephemeral one-shot with `run`

If you want to run a single command without managing the lifecycle yourself:

```
nexus3 run --memory 2048 nexus3-base:20260807 -- go test ./...
```

`run` creates a sandbox, boots it, runs the command, and removes the sandbox unconditionally on
exit — including on Ctrl-C or a crash. Cleanup is a deferred service call, not a signal handler.
The sandbox is gone when `run` returns.

---

## Labeling sandboxes

Labels are key-value pairs stamped at creation and used later to select sandboxes in bulk:

```
nexus3 sandbox create myproject/worker-1 --image nexus3-base:20260807 \
  --label motive=pr-42 --label env=ci
```

Multiple `--label` flags are AND-matched when selecting. See [Surface reference](surface.md#3-labels-and-selectors) for the full selector contract.

Batch exec `exec --label` was retracted (2026-08-15); select with `sandbox list --label` and
loop `exec <ref>` host-side. See [Surface reference](surface.md#3-labels-and-selectors).

---

## What's next

- [Workspace capture](guides/workspace-capture.md) — seed a sandbox with your live working tree (dirty files, untracked files, unpushed commits included)
- [Parallel dev flow](guides/parallel-dev-flow.md) — fan N sandboxes out, run tasks in parallel, and harvest results back
- [Building images](guides/building-images.md) — build custom guest images from a Containerfile
- [Docker in a sandbox](guides/docker-in-sandbox.md) — run Docker Compose inside a nexus3 sandbox
- [Surface reference](surface.md) — every command and flag, verified against the source
