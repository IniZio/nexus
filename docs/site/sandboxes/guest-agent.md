---
title: "Guest agent"
description: "The in-guest PID 1: gRPC control plane, session reattach, workload agnosticism"
---

# Guest agent

> `nexus3-agent` is a thin Go binary that runs as PID 1 inside every sandbox — the sole interface between the host and the workload.

The agent is purpose-built: OSS process supervisors (tini, s6, systemd) do not provide session-reattach-across-snapshot. The protocol is small (~10 RPCs) and owned entirely by the project.

```sh
nexus3 exec my-app -- uname -a          # gRPC Exec → data plane stdio
nexus3 exec my-app                      # gRPC Exec with PTY → data plane (auto-TTY target)
nexus3 attach my-app <session-id>       # reconnect to an existing session
nexus3 cp my-app:/path/to/file ./local  # gRPC Copy
```

## Startup sequence (PID 1 mode)

When the kernel hands control to `nexus3-agent` as PID 1:

1. Mount standard pseudo-filesystems: `devtmpfs`, `proc`, `sysfs`.
2. Set a default `PATH` (`/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`).
3. Mount `/tmp` as a RAM-backed `tmpfs`. Seed size: 32 MiB. The governor resizes it at runtime to `max(1 GiB, min(50% MemTotal, 2 GiB))`.
4. Configure the virtio-net interface with static IP `192.168.127.2/24`.
5. Start `sshd` (if present in the image).
6. Process kernel cmdline args (mount specs, `--mem-ceiling`, builder role flag).
7. Read the services table (`/etc/nexus3/services.yaml` merged with create-time overrides); start declared services in order; poll readiness probes until all pass or the 30s cap expires — create fails if the cap is exceeded. <Badge type="danger" text="not built" />
8. Open the vsock control port and begin serving the `nexus3.agent.v1` gRPC service. The host's `create` call unblocks only after this port is open.

The agent build tag (`YYYYMMDD-<git-short-sha>`) is printed at startup so operators can detect image staleness.

## The `nexus3.agent.v1` protocol

### Control plane — gRPC over vsock (fixed port)

All RPCs are unary request/response. The host dials the guest; the guest never dials back.

| RPC | Description |
|-----|-------------|
| `Exec` | Start a process (optionally with a PTY). Returns a session ID. Stdio flows on the data plane. |
| `Signal` | Deliver a POSIX signal to a running session's process. |
| `SessionStatus` | Read-only status for a single session. |
| `ListSessions` | Read-only status for all known sessions. |
| `Copy` | Transfer files between host and guest. |

### Data plane — clawk-framed vsock connection per session

Each active session gets one multiplexed vsock connection, framed with the clawk protocol. Keystrokes and output flow here, not over the gRPC control channel.

| Frame type | Purpose |
|------------|---------|
| `Handshake` | Identifies session and replay cursor |
| `Data` | stdin/stdout/stderr bytes |
| `Winsize` | Terminal resize events |
| `Exit` | Process exit code |

### Session reattach

The agent maintains a **guest-authoritative output ring** per session. When a host reconnects — after a network hiccup, a snapshot restore, or a CLI restart — it sends a `Handshake` frame with its last-seen sequence number. The agent replays any buffered frames the client missed and resumes live streaming.

This is how the data plane survives snapshot: after `fork` produces a child VM, the child's agent holds the output ring from the moment the snapshot was taken. A new host connection can reattach and see the output from before the fork.

```sh
# Start a long-running session
nexus3 exec my-app -- go test -count=1 ./...

# Later — reconnect and see output from before the disconnect
nexus3 attach my-app <session-id>
```

## Builder role

The same `nexus3-agent` binary serves a second role: the **builder VM** used during image builds. When passed `--builder-role` on the kernel cmdline, the agent runs the BuildKit lifecycle and exits — it does not start the gRPC control server. This keeps the image pipeline from needing a second binary.

See [Images](images.md) for the full build pipeline.

## Workload agnosticism

The agent does not know or care what the user runs inside the sandbox. Any container runtime (Docker, containerd) in the guest is a user-supplied component of the image — the agent does not manage it.

The agent does not auto-start Docker in the workload path. Start dockerd explicitly after the sandbox boots, or declare it as a [startup service](#startup-services) in the image so `create` blocks until Docker is ready.

```sh
nexus3 exec my-app -- dockerd &
nexus3 exec my-app -- docker ps
```

## Startup services <Badge type="danger" text="not built" />

The agent reads a **services table** at startup and starts declared services before opening the vsock control port. This makes `nexus3 create` readiness-gated — the CLI returns only once all declared service probes pass (30s cap; create fails if exceeded). Blocking is the only behaviour; there is no opt-out. <Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

| Site | Where declared | Priority |
|------|---------------|----------|
| Image-level | `/etc/nexus3/services.yaml` baked into the rootfs | Base |
| Create-time | `--service 'name:cmd[:readyprobe]'` flag on `nexus3 create` | Overrides same-named image entry |

```yaml
# /etc/nexus3/services.yaml
services:
  - name: dockerd
    command: [dockerd, --storage-driver=overlay2]
    ready: [docker, info]
```

A service with no `ready` probe is fire-and-forget (same behaviour as `sshd` today). No init system (systemd, runit) is used; the agent drives the table directly as PID 1. See [Images — startup services](images.md#startup-services) for the image-build side.

## macOS <Badge type="info" text="backlogged" />

On macOS, the vsock data path uses fd-passing (`nexus3-vzd`). The control protocol semantics are identical; only the transport differs. This path is backlogged. See [Execution substrate](execution-substrate.md).
