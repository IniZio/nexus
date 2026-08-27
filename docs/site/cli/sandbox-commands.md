---
title: "Lifecycle commands"
description: "Reference for nexus3 lifecycle verbs: create, ps, rm, start, stop, pause, resume"
---

# Lifecycle commands

> Create, inspect, and manage the lifecycle of microVM sandboxes.

A sandbox is a Cloud Hypervisor microVM identified by `project/name`. Every lifecycle operation goes through `internal/core/service`; the CLI verbs here are thin wrappers.

::: tip Both spellings work <Badge type="tip" text="built" />
The lifecycle verbs are spelled flat — `nexus3 create`, `nexus3 ps`, and so on. The grouped spelling is kept as an equivalent alias for existing scripts, the MCP tools and the herdr plugin:

| Flat (preferred) | Grouped (equivalent) |
|---|---|
| `nexus3 create` | `nexus3 sandbox create` |
| `nexus3 ps` (or `nexus3 ls`) | `nexus3 sandbox list` |
| `nexus3 rm` | `nexus3 sandbox rm` |
| `nexus3 start` | `nexus3 sandbox start` |
| `nexus3 stop` | `nexus3 sandbox stop` |
| `nexus3 pause` | `nexus3 sandbox pause` |
| `nexus3 resume` | `nexus3 sandbox resume` |

They are not two implementations: each flat verb delegates to the same code path, so flags, exit codes and JSON envelopes are identical either way.
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
| `--egress <mode>` | string | — | Egress policy (`open` or `github-only`) |
| `--allow-host <host>` | string | — | Add a host to the egress allowlist; repeatable |
| `--repo <owner/repo>` | string | — | Add a GitHub repo to the MITM allowlist; repeatable |
| `--mount <host-path>:<guest-path>[:ro]` | string | — | Live virtiofs mount of a host directory into the guest; repeatable. The host path must exist and be a directory; it is resolved to an absolute path. Add `:ro` for read-only. Guest paths containing `.git` components are **allowed** (unlike `--mount-named`). See [Live host mounts](#live-host-mounts). |
| `--mount-named <name>:<guest-path>[:<options>]` | string | — | Attach a named volume at `<guest-path>`; repeatable. Options: `kind=dir\|disk` (default `disk`), `size=<N>g` (default `10g`; kind=disk only), `ro`. Volume is created automatically when it does not exist. Guest paths whose components include `.git` are rejected. See [Named volumes](#named-volumes). |
| `--service 'name:cmd[:probe]'` | string | — | <Badge type="danger" text="not built" /> Per-sandbox addition or override of a same-named service declared in the image's `.nexus/services.yaml`. `create` blocks until all probes pass (30-second cap). See [Docker in a sandbox](/recipes/docker-in-sandbox). |

Auto-resize is unconditional: hotplug hardware is configured at create time and the dynamic governor activates in the supervisor process. `--memory-max`, `--vcpus-max`, and `--disk-max` set the ceiling; the governor expands within it automatically.

### Named volumes

`--mount-named <name>:<guest-path>[:<options>]` attaches a named volume into the guest. Named volumes are user-owned and persist independently of any sandbox — `nexus3 rm` detaches them but never deletes their backing files.

```
nexus3 create myproject/dev-1 \
  --image myapp-base:latest \
  --mount-named myapp-node_modules:/workspace/myapp/node_modules:kind=disk,size=10g \
  --mount-named myapp-docker:/var/lib/docker:kind=disk,size=20g \
  --memory 8192
```

| Option | Values | Default | Effect |
|--------|--------|---------|--------|
| `kind` | `dir`, `disk` | `disk` | `dir` = virtiofs host directory; `disk` = ext4 block image |
| `size` | e.g. `10g`, `20g` | `10g` | Backing disk size (kind=disk only) |
| `ro` | (flag) | rw | Mount read-only inside the guest |

Prefer `kind=disk` for dependency stores and build caches — block I/O is measurably faster than virtiofs for metadata-heavy operations. Use `kind=dir` when the host directory path must stay accessible outside the sandbox.

**Hard refusals:** any guest path whose components include `.git` (terminal or non-terminal) is rejected at parse time.

**Attach rules:**
- `kind=disk`: one read-write attacher at a time; multiple read-only attachers are allowed simultaneously.
- `kind=dir`: no attach-count restriction.

Use `nexus3 volume rm <name>` to delete a volume explicitly, or `nexus3 volume prune` to reclaim detached volumes. See [Volume commands](/cli/volume-commands) for the full lifecycle.

For the agent skill that generates `--mount-named` fragments from project manifests (package.json, Cargo.toml, go.mod, etc.), see [AI agents](/ai-agents).

### Live host mounts

`--mount <host-path>:<guest-path>[:ro]` mounts a host directory into the guest as a live virtiofs share. Edits inside the sandbox appear on the host immediately; no sync step is needed.

```
nexus3 create myproject/dev-1 \
  --image nexus3-base:20260807 \
  --mount /data/repos/myrepo:/workspace/myrepo \
  --memory 8192
```

Key differences from `--mount-named`:

| | `--mount` (live host mount) | `--mount-named` (named volume) |
|---|---|---|
| Backing | Host directory via virtiofs | User-owned volume (ext4 disk or virtiofs dir) |
| Persistence | Host filesystem — survives `rm` naturally | Volume store — explicit `volume rm` to delete |
| `.git` guest path | **Allowed** (D-PD-99) — mounting a real worktree is the primary use-case | **Hard refused** at parse time |
| Fork / snapshot | **Refused** with an explicit error | Refused (TBR-PD-15, deferred) |
| Use case | Live worktree editing; read-only config injection | Dependency stores, build caches |

The host path must exist and be a directory; it is resolved to an absolute path. Repeatable:

```
nexus3 create myproject/dev-1 \
  --mount /data/repos/myrepo:/workspace/myrepo \
  --mount /data/shared/secrets:/run/secrets:ro \
  --memory 8192
```

See [Mounts and worktrees](/recipes/mounts-and-worktrees) for the full worktree workflow.

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
