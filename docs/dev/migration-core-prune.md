# Migration: Core Prune

Nexus has been hard-pruned to a workspace-core scope.

## Removed surfaces

- Enforcer/Boulder packages and workflows
- IDE/plugin package surfaces outside workspace core
- Non-core docs referencing removed modules
- Firecracker-LXC bridge (replaced with native Firecracker)

## Current supported surfaces

- `packages/nexus`
- `packages/sdk/js`

## What to update in downstream usage

- Stop referencing removed package paths in scripts/automation.
- Use workspace daemon + sdk references in documentation and CI.
- For project configuration, use `.nexus/workspace.json` and workspace reference docs.

## Firecracker Native Cutover Migration

The Firecracker runtime has been migrated from LXC-based execution to native Firecracker + vsock agent.

### Breaking changes

The following environment variables have been **removed** and will cause validation errors at daemon startup or when running `nexus doctor`:

| Removed Variable | Migration Action |
|------------------|------------------|
| `NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE` | Remove. Native Firecracker uses direct API communication. |
| `NEXUS_DOCTOR_FIRECRACKER_INSTANCE` | Remove. Instance management is now internal to the daemon. |
| `NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE` | Remove. Docker mode is no longer supported in Firecracker backend. |

### New required configuration

When using the `firecracker` runtime backend, you must now provide:

| Variable | Description | Example |
|----------|-------------|---------|
| `NEXUS_FIRECRACKER_KERNEL` | Path to Firecracker kernel binary | `/var/lib/nexus/vmlinux.bin` |
| `NEXUS_FIRECRACKER_ROOTFS` | Path to Firecracker rootfs image | `/var/lib/nexus/rootfs.ext4` |

### Replacing vmctl-firecracker scripts

Scripts that previously invoked `vmctl-firecracker` or `limactl shell nexus-firecracker` should be updated:

- **Old**: `vmctl-firecracker create --name $WS`
- **New**: Daemon manages VMs directly via Firecracker API; no CLI equivalent needed

The daemon now handles VM lifecycle internally. Remove any script logic that manually manages Firecracker instances through `vmctl-firecracker` commands.

### Guest agent verification

The guest agent binary (`nexus-firecracker-agent`) must be present in the rootfs image at `/usr/local/bin/nexus-firecracker-agent`. Verify with:

```bash
# Inside a running workspace, or by mounting the rootfs
ls -la /usr/local/bin/nexus-firecracker-agent
# Expected: executable binary present
```

### Migration checklist

- [ ] Remove all `NEXUS_DOCTOR_FIRECRACKER_*` environment variables from CI workflows
- [ ] Add `NEXUS_FIRECRACKER_KERNEL` and `NEXUS_FIRECRACKER_ROOTFS` to environment
- [ ] Ensure Firecracker binary is available in PATH
- [ ] Verify guest agent binary is embedded in rootfs image at `/usr/local/bin/nexus-firecracker-agent`
- [ ] Update any scripts referencing `vmctl-firecracker` or `limactl` for Firecracker operations
