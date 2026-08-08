# 04 — Agent Runtime

*Purpose: the thin PID-1 guest agent, the `nexus3.agent.v1` protocol, its control and data planes, and session reattach across snapshot. Homegrown, not offloaded.*

## Why homegrown

No production OSS library provides exec/PTY/streaming composable with an external microVM substrate (Daytona OSS archived, E2B cloud-only; ticket 05). So the agent is **homegrown Go**, a **thin PID-1** (ticket 08): it does **exec / PTY / signals only**. Mounts and port-forwarding stay host-side (virtio-fs / vsock); the agent does not manage them.

- One static binary, `CGO_ENABLED=0`, `linux/amd64` + `arm64`. Baked as the final rootfs layer (`init=/sbin/nexus3-agent`), so an agent bump rebuilds only that layer (doc 07).
- Its own protocol `nexus3.agent.v1`, **not vendored** from vminitd (which assumes OCI containers and in-guest mounts, both designed away).
- The Go choice is **reversible** behind the proto seam, with a recorded trigger: rewrite in Rust **iff** measured agent RSS binds fan-out density (ticket 09).

## The `nexus3.agent.v1` protocol (~10 RPCs)

The protocol has **two planes**, both riding vsock connections that `driver.DialGuest` opens (host dials guest, doc 03).

### Control plane — gRPC over vsock (fixed control port)

Small set of request/response RPCs:

- **`Exec` / `Spawn`** — start a process, optionally with a PTY. Returns a host-minted `session_id`.
- **`Signal`** — deliver a signal to a running process.
- **`SessionStatus` / `ListSessions`** — read-only status for display (the only surviving *probe*; not used for reattach).
- **`Copy`** — the file-transfer capability class (ticket 22, ratified live): a control RPC in apple-containerization's **split-plane** shape — metadata on the control channel, raw bytes over the data-plane side-channel, and **the agent archives directories itself** (no guest `tar`, unlike fragile `kubectl cp`). Backs `nexus3 cp` (doc 09) over agent/vsock, **never SSH**.

### Data plane — one multiplexed, clawk-framed vsock connection per session

Stdio is **one multiplexed connection**, not three separate stdio ports (ticket 36 retires ticket 16's three-port assumption). Frames follow clawk's typed shape: **`handshake` / `data` / `winsize` / `exit`**, with a stream-tag splitting stdout/stderr and a 64 KiB frame cap. `session_id` demuxes multiple sessions over one fixed guest data port.

## Session reattach = the data-connection handshake

There is **no `Reattach` RPC and no probe** (ticket 36 retires ticket 08's monolithic `Reattach`). Reattach *is* the data-connection handshake:

```
host → guest:  { session_id, resume_from_offset }
guest → host:  { alive } | { exited(code) }   then stream-from-offset
```

- Resize is an **in-band `winsize` frame**, not an RPC.
- Session key is **`(instance_id, session_id)`** on the host (kata's `(container_id, exec_id)` on nexus3's nouns). The **guest is oblivious to `instance_id`** — which satisfies ticket 16 rule 5 (identical guest pids across N fork children are separated by the host key) by construction. E2B's `{pid}` keying was rejected.
- The host dials **one fixed guest data port**; `session_id` demuxes. New-ports-on-restore (Firecracker's shape) is **rejected** because guest listeners survive restore (doc 03).

## Guest-authoritative output ring

Each session's scrollback lives in a **guest-RAM output ring held by the agent** (the agent's PTY table). This ring:

- **rides the snapshot**, so a fork child carries correct history and ancestor post-snapshot output is physically absent from children (ticket 16 forbidden #1, verified ticket 37);
- gives physical isolation between siblings;
- survives a **core restart** (the buffer is in the guest, not the host).

Ticket 16's host-side replay buffer is **demoted** to a transient per-client repaint/fan-out cache — it is no longer the authority for fan-out history.

## Reattach is not novel; the hazards are pre-solved

The reattach primitive is published three times (E2B's re-bindable `Connect`/`StreamInput`/`Update`; firecracker-containerd's `State`-then-`Attach`; kata's `(container_id, exec_id)`). What is novel is an agent-held PTY **surviving VM snapshot/restore** — but the honest reading is that *preserving the connection is impossible for everyone, preserving the session is free for everyone, and nobody wired it up.* The two hazards nobody else solved (N children with identical pids; scrollback coherence across N clients) are exactly ticket 16's rule 5 and its host-side replay buffer. Proven end-to-end on CH v53 with N=3 independently-attachable clones (ticket 37).

## macOS data path (backlogged)

The framing is identical on the backlogged macOS path (the data path stays off the keystroke path there too); the VZ-specific mechanism is in **doc 12**.

---

*Sources: tickets 08 (thin PID-1, exec/PTY/signals, own proto), 05 (no OSS library), 09 (Go reversible), 36 (one multiplexed connection, handshake reattach, guest ring, retires 3-port/monolithic-Reattach), 16 (host-dials-guest, keying, rule 5), 22 (Copy capability class), 37 (proven on CH v53), 33 (macOS fd-passing). Map amendments: 08←16, 08←22, 08←36.*
