# Sandbox lifecycle

nexus creates and manages isolated microVMs. Each sandbox has its own network, filesystem, and process namespace.

## Commands

```sh
nexus create [--file <Dockerfile>] [--mount <host-path>:<guest-path>[:ro]] [--mount-named <name>:<guest-path>[:<options>]] ...
nexus ps
nexus start <sandbox-ref>
nexus stop <sandbox-ref>
nexus pause <sandbox-ref>
nexus hibernate <sandbox-ref>
nexus resume <sandbox-ref> [--restore-mode copy|ondemand] [--no-cold-fallback]
nexus rm <sandbox-ref>
nexus shell <sandbox-ref>
nexus forward <sandbox-ref> <host-port>:<guest-port>
```

## Pause vs hibernate (Proxmox-style)

- `pause` freezes vCPUs in memory (like Proxmox "pause"); RAM stays allocated on the host. `resume` thaws instantly (`resumed_from: memory`).
- `hibernate` snapshots VM RAM + device state to disk and stops the VMM (like Proxmox "hibernate"/suspend-to-disk): zero host RAM and no supervisor while hibernated. `resume` (or `start`) restores the same sandbox id from the snapshot; if restore fails it cold-starts on the same disks (exit 0, stderr `warning: snapshot restore failed (<reason>); cold-started`, JSON `resumed_from:"cold"` + `fallback_reason`). `--no-cold-fallback` makes that an error (exit 1).
- Idempotent: `hibernate` on Hibernated and `resume` on Running exit 0 with `already:true`.
- JSON (`--json`): hibernate gives `id, state, already, pause_ms, snapshot_ms, total_ms, snapshot_bytes, snapshot_bytes_on_disk, snapshot_dir`. `snapshot_bytes` is apparent (logical) size and can approach guest RAM, incl. the sparse hotplug region; `snapshot_bytes_on_disk` is allocated blocks, so use it for disk budgeting (`ls` shows it). Resume gives `id, state, already, resumed_from (memory|snapshot|cold), restore_mode, restore_ms, agent_ready_ms, total_ms, fallback_reason?, clock_skew_ms?`.
- Error codes: `hibernate_unsupported`, `hibernate_refused` (reason in message), `illegal_transition`, `snapshot_failed`.
- Limits: cloud-hypervisor driver only; refused with live `--mount` or named volumes (and builder sandboxes); snapshot size is about the guest RAM size on disk; open TCP/vsock connections reset on resume. `--restore-mode ondemand` lazily faults memory in (needs host userfaultfd support).

## Live host mounts (`--mount`)

`--mount <host-path>:<guest-path>[:ro]` mounts a host directory into the guest as a live virtiofs share. Edits inside the sandbox appear on the host immediately — no archive or sync step. Repeatable.

Primary use-case: mounting a git worktree so an in-guest agent can edit source files and commit directly to the host branch.

**Key rules:**
- The host path must exist and be a directory; it is resolved to an absolute path.
- `.git` guest paths are **allowed** (D-PD-99) — this is the deliberate divergence from `--mount-named`. Mounting a real worktree's `.git` is the primary use-case.
- `fork` and `snapshot create` are **refused** on a sandbox with live mounts; the error names the offending host→guest pairs.

**Contrast with `--mount-named`:**

| | `--mount` | `--mount-named` |
|---|---|---|
| Backing | Host directory via virtiofs | User-owned volume (ext4 disk or virtiofs dir) |
| Persistence | Host filesystem | Volume store (`nexus volume rm` to delete) |
| `.git` guest path | Allowed | **Hard refused** |
| Fork / snapshot | Refused | Refused (TBR-PD-15, deferred) |
| Use case | Live worktree editing | Dependency stores, build caches |

Do **not** use `--mount` to mount dependency directories (node_modules, target, etc.) — use `--mount-named kind=disk` for those; block I/O is measurably faster for metadata-heavy operations.

For named volumes (`--mount-named`) see `volumes.md`.
