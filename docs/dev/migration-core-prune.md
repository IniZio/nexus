# Migration: Core Prune

Nexus is scoped to workspace core only. Removed: enforcer/Boulder and other non-core packages, IDE/plugin surfaces outside core, old Firecracker-LXC bridge (native Firecracker now).

**Supported:** `packages/nexus`, `packages/sdk/js`. Downstream: drop old package paths; document daemon + SDK; use `.nexus/workspace.json` per reference docs.

## Firecracker native (was LXC bridge)

**Removed env vars** (will error at startup / `nexus doctor`): `NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE`, `NEXUS_DOCTOR_FIRECRACKER_INSTANCE`, `NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE`.

**Required for `firecracker` backend:**

| Variable | Role |
|----------|------|
| `NEXUS_FIRECRACKER_KERNEL` | Kernel image path |
| `NEXUS_FIRECRACKER_ROOTFS` | Rootfs path |

**Scripts:** replace `vmctl-firecracker` / manual `limactl` VM management with daemon-managed lifecycle (no CLI equivalent for raw FC create).

**Guest agent:** `nexus-firecracker-agent` at `/usr/local/bin/nexus-firecracker-agent` in the rootfs (`ls -la` inside workspace or mounted image).

**Checklist:** strip `NEXUS_DOCTOR_FIRECRACKER_*` from CI; set kernel/rootfs paths; FC binary on `PATH`; verify guest agent in rootfs; remove obsolete vmctl/limactl automation.
