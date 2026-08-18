---
title: "Lifecycle commands"
description: "Reference for nexus3 lifecycle verbs: create, ps, rm, start, stop, pause, resume"
---

# Lifecycle commands

> Create, inspect, and manage the lifecycle of microVM sandboxes.

A sandbox is a Cloud Hypervisor microVM identified by `project/name`. Every lifecycle operation goes through `internal/core/service`; the CLI verbs here are thin wrappers.

::: warning Implementation spelling <Badge type="warning" text="partial" />
Today's implementation spells the lifecycle verbs as `nexus3 sandbox <verb>`. The target spells them flat:

| Target | Implementation today |
|---|---|
| `nexus3 create` | `nexus3 sandbox create` |
| `nexus3 ps` | `nexus3 sandbox list` |
| `nexus3 rm` | `nexus3 sandbox rm` |
| `nexus3 start` | `nexus3 sandbox start` |
| `nexus3 stop` | `nexus3 sandbox stop` |
| `nexus3 pause` | `nexus3 sandbox pause` |
| `nexus3 resume` | `nexus3 sandbox resume` |

The rest of this page uses the target spelling throughout.
:::

## nexus3 create

Create a sandbox and boot it.

```
nexus3 create <project>/<name> [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--image <ref>` | string | — | Guest image reference to boot |
| `--rootfs <path>` | string | — | Path to a rootfs directory or tarball |
| `--context <dir>` | string | — | <Badge type="warning" text="partial" /> Dockerfile build context; builds an image in-VM (see [Configuration](/cli/configuration)). Built today as `--file`. |
| `--dockerfile <path>` | string | `Dockerfile` | Dockerfile path relative to `--context` |
| `--rm` | bool | false | Remove the sandbox automatically on exit |
| `--memory <MiB>` | int | — | Initial memory allocation in MiB |
| `--vcpus <n>` | int | — | Initial vCPU count |
| `--memory-max <MiB>` | int | — | Maximum memory for auto-resize hotplug |
| `--vcpus-max <n>` | int | — | Maximum vCPU count for auto-resize hotplug |
| `--disk-max <GiB>` | int | — | Maximum root disk size for auto-resize |
| `--label KEY=VALUE` | string | — | Metadata label; repeatable |
| `--nested` | bool | false | Enable nested KVM inside the guest |
| `--secret ENV@host[,host...]` | string | — | Inject a host environment variable as a secret; repeatable. See [Named secrets](/cli/auth-mcp-reap#named-secrets) to manage secrets by name. |
| `--no-builtin-gh` | bool | false | Disable the built-in GitHub secret injection |
| `--egress <mode>` | string | — | Egress policy (`open` or `github-only`) |
| `--allow-host <host>` | string | — | Add a host to the egress allowlist; repeatable |
| `--repo <owner/repo>` | string | — | Add a GitHub repo to the MITM allowlist; repeatable |
| `-v, --volume <host>:<guest>[:ro]` | string | — | <Badge type="danger" text="not built" /> Attach a host directory as a virtio-blk disk at `<guest>`. `:ro` marks it read-only |
| `--shadow <guest-path>` | string | — | <Badge type="danger" text="not built" /> Back a write-heavy path inside a mount with a per-sandbox shadow disk |
| `--service 'name:cmd[:probe]'` | string | — | <Badge type="danger" text="not built" /> Per-sandbox addition or override of a same-named service declared in the image's `.nexus/services.yaml`. `create` blocks until all probes pass (30-second cap). See [Docker in a sandbox](/recipes/docker-in-sandbox). |

Auto-resize is unconditional: hotplug hardware is configured at create time and the dynamic governor activates in the supervisor process. `--memory-max`, `--vcpus-max`, and `--disk-max` set the ceiling; the governor expands within it automatically.

### Mounts and shadow disks <Badge type="danger" text="not built" />

`-v` / `--volume` attaches a host directory into the guest as a virtio-blk ext4 disk:

```
nexus3 create myproject/dev-1 \
  --context /data/repos/myrepo \
  --volume /data/repos/myrepo:/workspace/myrepo \
  --memory 8192
```

`--shadow` declares a write-heavy path inside a mounted directory that should be backed by a per-sandbox shadow disk. Common build-artifact directories (`node_modules`, `target`, `.next`, `dist`) are shadowed automatically when `--shadow` is omitted.

### Startup services <Badge type="danger" text="not built" />

`--service 'name:cmd[:readyprobe]'` declares a process that must be running before `create` returns. The create command blocks until all probes pass (30-second cap); if any probe has not passed, create fails and the sandbox is removed.

```
nexus3 create myproject/dev-1 \
  --image nexus3-base:20260807 \
  --service 'dockerd:dockerd:docker info' \
  --memory 8192
```

## nexus3 ps

List sandboxes, optionally filtered by label.

```
nexus3 ps [--label KEY=VALUE] [--wide]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--label KEY=VALUE` | string | — | Filter by label (AND-matched when repeated) |
| `--wide` | bool | false | Include per-sandbox disk allocation, uptime, and fleet-level leaked-resource count (requires `--label`) |

`--wide` requires `--label`; without it the flag is rejected. Example: `nexus3 ps --label task-id=42 --wide`. Extra columns per sandbox: **ID**, **state**, **uptime** (formatted duration), **allocated disk** (sparse blocks × 512 bytes), **error**. Fleet totals: **total allocated** and **leaked resources**. (`--json` additionally carries `handle` and `uptime_seconds` fields in the machine-readable payload.)

## nexus3 rm

Remove a sandbox (must be stopped first).

```
nexus3 rm <id|prefix|project/name>
```

## nexus3 start

Boot a stopped sandbox.

```
nexus3 start <id|prefix|project/name>
```

## nexus3 stop

Shut down a running sandbox.

```
nexus3 stop <id|prefix|project/name>
```

## nexus3 pause

Pause a running sandbox (freeze in memory).

```
nexus3 pause <id|prefix|project/name>
```

## nexus3 resume

Resume a paused sandbox.

```
nexus3 resume <id|prefix|project/name>
```

## nexus3 run

Create a sandbox, run a command, and remove the sandbox on exit. Cleanup is guaranteed even on SIGINT or exec error.

```
nexus3 run [flags] <image-ref> -- <command> [args...]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--memory <MiB>` | int | — | Memory allocation in MiB |
| `--vcpus <n>` | int | — | vCPU count |
| `--name <name>` | string | — | Sandbox name (auto-generated if omitted) |
| `--project <project>` | string | — | Project to create the sandbox under |

## Labels and selectors

Labels are arbitrary key-value metadata stamped at creation. `create` accepts `--label KEY=VALUE`, repeatable:

```
nexus3 create myproject/w1 --image nexus3-base:latest \
  --label task-id=42 --label role=worker
```

`ps --label KEY=VALUE` filters by label, AND-matched when repeated:

```
for sb in $(nexus3 --json ps --label task-id=42 | jq -r '.data.sandboxes[].handle'); do
  nexus3 exec "$sb" -- git status
done
```

Two limits apply:

- **Label mutation** <Badge type="danger" text="not built" /> — the target is a verb that adds and removes labels on an existing sandbox. Today labels are set at create time and cannot be changed.
- **Fleet lifecycle selectors** <Badge type="danger" text="not built" /> — the target is `stop --label` and `rm --label` so a label selects a set for lifecycle operations, not just for listing. Today fan-out is the host-side loop shown above. Fleet `exec` is deliberately excluded from the target.
