# Guest agent

Every sandbox image contains `nexus3-agent`, a thin Go binary that runs as **PID 1** inside the guest. It is the sole interface between the host and the workload running in the VM.

The agent is **homegrown** — not built on any container runtime, not an adapted open-source init. This was a deliberate choice: OSS process supervisors (e.g. tini, s6) do not provide the session-reattach-across-snapshot guarantee that nexus3 needs. The protocol is small (around 10 RPCs) and owned entirely by the project.

## Startup sequence (PID 1 mode)

When the kernel hands control to `nexus3-agent` as PID 1:

1. Mount standard pseudo-filesystems: `devtmpfs`, `proc`, `sysfs`.
2. Set a default `PATH` (`/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`). The kernel provides no PATH to PID 1.
3. Mount `/tmp` as a RAM-backed `tmpfs`. Seed size: 32 MiB. The governor resizes it at runtime to `max(1 GiB, min(50% MemTotal, 2 GiB))`.
4. Configure the virtio-net interface with static IP `192.168.127.2/24`.
5. Start `sshd` (if present in the image).
6. Process kernel cmdline args (workspace disk mounts, `--mem-ceiling`, builder role flag).
7. Open the vsock control port and begin serving the `nexus3.agent.v1` gRPC service.

The agent build tag (`YYYYMMDD-<git-short-sha>`) is printed at startup so operators can detect image staleness.

## The `nexus3.agent.v1` protocol

Verified against `internal/core/agent/agentpb/agent_grpc.pb.go`.

### Control plane — gRPC over vsock (fixed port)

All RPCs are unary request/response. The host dials the guest; the guest never dials back.

| RPC | Description |
|-----|-------------|
| `Exec` | Start a process (optionally with a PTY). Returns a session ID. Does not stream stdio — stdio flows on the data plane. |
| `Signal` | Deliver a POSIX signal to a running session's process. |
| `SessionStatus` | Read-only status for a single session. |
| `ListSessions` | Read-only status for all known sessions. |
| `Copy` | Transfer files between host and guest. |

### Data plane — clawk-framed vsock connection per session

Each active session gets one multiplexed vsock connection, framed with the clawk protocol (handshake / data / winsize / exit frames). Keystrokes and output flow here, not over the gRPC control channel.

The data-plane connection carries:
- `Handshake` frame — identifies session and replay cursor.
- `Data` frames — stdin/stdout/stderr bytes.
- `Winsize` frames — terminal resize events.
- `Exit` frame — process exit code.

### Session reattach

The agent maintains a **guest-authoritative output ring** per session. When a host reconnects (after a network hiccup, a snapshot restore, or a CLI restart), it sends a `Handshake` frame with its last-seen sequence number. The agent replays any buffered frames the client missed and resumes live streaming.

This is how the data plane survives snapshot: after `fork` produces a child VM, the child's agent holds the output ring from the moment the snapshot was taken. A new host connection can reattach and see the output from before the fork.

Reattach is not novel: the hazards (cursor consistency, ring overflow, concurrent reattach races) are pre-solved in the protocol design. The `Attach` call in `internal/core/agent/attach.go` opens a fresh data-plane connection, sends the reattach handshake, and drives it until the session exits.

## Builder role

The same `nexus3-agent` binary serves a second role: the **builder VM** (used during image builds). When passed `--builder-role` on the kernel cmdline, the agent runs the BuildKit lifecycle and exits — it does not start the gRPC control server. This keeps the image pipeline from needing a second binary.

See [Images](images.md) for the full build pipeline.

## Workload agnosticism

The agent is workload-agnostic. It does not know or care what the user runs inside the sandbox. In particular, any container runtime (Docker, containerd) in the guest is a user-supplied component of the image — the agent does not manage it.

> **Code note (2026-08-15):** `agent.StartDockerIfPresent` was removed from `cmd/nexus3-agent/main.go`. The agent no longer auto-starts Docker in the workload path. (buildkitd is still started when invoked with `--builder-role` — that is nexus3's own build plumbing, not a user workload; see [Surface reference § DEPARTURE 3](../surface.md#5-departures-table-and-analysis).) If your workload requires dockerd, start it explicitly after the sandbox boots. See [Docker in a sandbox](../guides/docker-in-sandbox.md).

## macOS (backlogged)

On macOS, the vsock data path uses fd-passing (`nexus3-vzd`). The control protocol semantics are identical; only the transport differs. This path is backlogged. See [Execution substrate](execution-substrate.md).
