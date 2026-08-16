# nexus3 surface reference

**Purpose of this document:** operator decision support. Read it to see the entire nexus3 command
surface at once and judge whether it is coherent, minimal, and appropriately primitive. It is not
marketing and not an API dump. Every command listed here exists and has been verified against the
parity table (`internal/cli/surface_parity_test.go`) and the CLI source files.

For the normative rules — what the canonical API is, the parity invariant, and the decisions behind
the envelope shapes — see the [Surface contract](surface-contract.md).

---

## 1. What nexus3 is

nexus3 is a microVM sandbox manager that exposes generic lifecycle, execution, and I/O primitives
over both a CLI and an MCP tool server. Each sandbox is an isolated Cloud Hypervisor VM with its
own disk (CoW-from-a-base ext4 image), network namespace, and vsock channel to an in-guest agent.

The governing principle is **D-PD-21: nexus3 core ships primitives, never workflow verbs.** Surface
grouping uses generic labels, not domain nouns. There is no `motive start` or `pr create`; there is
`up --label motive=X` and (not yet built) a host-side push command. Every departure from this rule
is tracked in Section 5.

The MCP surface (`sandbox_create`, `sandbox_list`, etc.) covers only lifecycle. All execution and
I/O operations are CLI-only today.

---

## 2. Command reference

A sandbox **reference** (`<ref>`) is an exact ID (hex), an ID prefix, or a `project/name` handle.

### 2.1 Lifecycle

#### `sandbox` — subcommands for the full sandbox lifecycle

The primary command group. Has MCP equivalents for all subcommands.

```
sandbox create <project>/<name> [flags]     # mint a record (or create-and-boot)
sandbox list                                # enumerate all records
sandbox rm   <ref>                          # delete record + disk resources
sandbox start  <ref>                        # boot a stopped/created sandbox
sandbox stop   <ref>                        # terminate a running sandbox
sandbox pause  <ref>                        # suspend (VM snapshot to disk)
sandbox resume <ref>                        # unsuspend
```

**`sandbox create` flags:**

| Flag | Description |
|---|---|
| `--image <ref>` | OCI/ext4 image reference; triggers create-and-boot |
| `--rootfs <path>` | Host ext4 path; triggers create-and-boot |
| `--file <dir>` | Build from `<dir>/.nexus/Containerfile` via in-VM buildkitd, then boot |
| `--dockerfile / -f <path>` | Override Containerfile path (use with `--file`) |
| `--rm` | Remove sandbox on process exit |
| `--memory <MiB>` | Guest RAM (default: driver default) |
| `--vcpus <N>` | Guest vCPUs (default: driver default) |
| `--memory-max <MiB>` | RAM ceiling for live hotplug |
| `--vcpus-max <N>` | vCPU ceiling for live hotplug |
| `--label KEY=VALUE` | Repeatable; AND-matched when selecting (see Section 3) |
| `--nested` | Expose `/dev/kvm` inside guest (enables nested virtualization; off by default) |
| `--workspace <host-path>` | Capture host working tree (dirty + untracked + unpushed) into guest |
| `--capture-max <size>` | Explicit workspace capture cap; 0 = auto (derived from free disk space) |

Without `--image`, `--rootfs`, or `--file`: record is created in state `created`, no VM is started.
With any image flag: VM is booted and agent reachability is probed before returning.

**Example — store-only create:**
```
nexus3 sandbox create myproject/worker-1
```

**Example — create and boot from image:**
```
nexus3 sandbox create myproject/worker-1 --image nexus3-base:20260807 \
  --memory 4096 --vcpus 2 --label motive=my-motive
```

**Example — build from Containerfile and boot:**
```
nexus3 sandbox create myproject/builder-1 --file /path/to/project \
  --workspace /path/to/project --capture-max 8GiB
```

**MCP equivalents:** `sandbox_create`, `sandbox_list`, `sandbox_start`, `sandbox_stop`,
`sandbox_pause`, `sandbox_resume`, `sandbox_remove`.

---

#### `up` — create N sandboxes in one call with disk-space preflight

```
nexus3 up [--count N] [--project P] [--label KEY=VALUE ...]
```

| Flag | Description |
|---|---|
| `--count <N>` | Number of sandboxes to create (default: 1) |
| `--project <P>` | Project for all sandboxes (default: "ephemeral") |
| `--label KEY=VALUE` | Repeatable; stamped on every created sandbox |

Runs a disk-space preflight before any sandbox is created: estimates `count × per-sandbox-bytes`
(allocated, not apparent size) and rejects with `insufficient_disk` if the host cannot accommodate
the batch. Creates sandboxes sequentially; reports per-sandbox success/failure in the `up.completed`
envelope. Does not boot sandboxes — record-only creates (state `created`).

```
nexus3 up --count 8 --label motive=my-motive --project dev
```

No MCP equivalent.

---

#### `run` — ephemeral one-call exec (create + boot + command + remove)

```
nexus3 run [flags] <image-ref> -- <command> [args...]
```

| Flag | Description |
|---|---|
| `--memory <MiB>` | Guest RAM (0 = driver default) |
| `--vcpus <N>` | vCPUs (0 = driver default) |
| `--name <name>` | Sandbox name suffix (default: generated) |
| `--project <P>` | Sandbox project (default: "ephemeral") |

Creates and boots a sandbox, runs the command via the in-guest agent, and removes the sandbox
unconditionally on exit — even if the command crashes or the host process is killed. Cleanup is
guaranteed via a deferred service call, not a signal handler.

```
nexus3 run --memory 2048 nexus3-base:20260807 -- go test ./...
```

No MCP equivalent.

---

#### `reap` — report and optionally delete orphaned host resources

```
nexus3 reap [--apply]
```

Scans the filesystem directly (not the record store) and classifies each nexus3 resource as
orphaned (no record, no live process) or live. Without `--apply`: prints a report and the
reclaimable bytes. With `--apply`: deletes orphaned resources.

```
nexus3 reap --apply
```

No MCP equivalent.

---

#### `recover` — reconcile persisted records against the live substrate

```
nexus3 recover
```

Reconciles sandbox records against the live VM substrate. Used after unexpected host failures.
No flags documented. No MCP equivalent.

---

### 2.2 Execution

#### `exec` — run a command in a sandbox via the in-guest agent

**Single-sandbox form:**
```
nexus3 exec <ref> [--pty] [--rows N] [--cols N] -- <command> [args...]
```

| Flag | Description |
|---|---|
| `--pty` | Allocate a PTY (single-sandbox only) |
| `--rows <N>` | Terminal rows (requires `--pty`; default: 24) |
| `--cols <N>` | Terminal columns (requires `--pty`; default: 80) |

**Batch form (fan-out across label-matched sandboxes):**
```
nexus3 exec --label KEY=VALUE [--parallel N] -- <command> [args...]
```

| Flag | Description |
|---|---|
| `--label KEY=VALUE` | Select sandboxes matching this label (see Section 3) |
| `--parallel <N>` | Max concurrent sandboxes (default: 2) |

The default of 2 is not arbitrary: at 2 concurrent nexus3 VMs the host measured 84% swap
pressure. Raising `--parallel` beyond 2 risks swap thrashing; measure before changing.
**Current restriction:** `--label` batch exec only supports the `motive` label key. Other keys
return a usage error. This is a known limitation (see Section 6).

Stdout and stderr from each sandbox are buffered separately and printed sequentially after all
sandboxes complete, so output from different sandboxes never interleaves.

```
# Single
nexus3 exec myproject/worker-1 -- python3 run_test.py

# Batch across motive
nexus3 exec --label motive=my-motive --parallel 4 -- git status
```

No MCP equivalent.

---

#### `shell` — interactive login shell in a sandbox

```
nexus3 shell <ref> [-- <cmd> [args...]]
```

Opens a PTY-backed shell. Defaults to `/bin/bash --login` when no trailing command is given.
Terminal size is auto-detected from stdin; non-TTY stdin falls back to 80×24. No additional flags.

```
nexus3 shell myproject/worker-1
nexus3 shell myproject/worker-1 -- /bin/bash -lc 'cd /app && make test'
```

No MCP equivalent.

---

#### `attach` — reattach to an existing guest session

```
nexus3 attach <ref> <session-id>
```

Reattaches to an existing in-guest session by session ID. No PTY flags; reuses the existing
session's PTY. No MCP equivalent.

---

### 2.3 I/O and data movement

#### `cp` — copy files between host and guest

```
nexus3 cp <ref> <src> <dst> [--dir]
```

Exactly one of `src` or `dst` must carry the `guest:` prefix to identify the guest side.

| Flag | Description |
|---|---|
| `--dir` | Treat the guest path as a directory (archive transfer) |

```
# Pull from guest
nexus3 cp myproject/worker-1 guest:/app/output.tar.gz ./output.tar.gz

# Push to guest
nexus3 cp myproject/worker-1 ./config.json guest:/etc/myapp/config.json

# Pull directory
nexus3 cp myproject/worker-1 guest:/app/dist ./dist --dir
```

No MCP equivalent.

> **Transport design note:** `cp` transport is the native agent `Copy` capability over vsock,
> never SSH. The agent archives directories itself; no guest `tar` is required. `cp` is
> host-initiated with no inbound channel — the guest cannot invoke it, so the adversary gains no
> host-mutation vector. The push-in direction is an explicit operator-initiated post-seed
> contribution. `cp` produces plain host-side files, never a nexus3 durable entity.
> Guest-outbound egress (`git push`, `curl`, `npm publish`) has no nexus3 verb — it is ordinary
> agent egress under the perimeter allowlist.

---

#### `forward` — forward a host TCP port to a guest TCP port

```
nexus3 forward <ref> <hostPort>:<guestPort>
```

Binds `127.0.0.1:<hostPort>` on the host, dials the in-guest agent's vsock port-forward
multiplexer, and splices traffic. Runs until Ctrl-C. No additional flags.

```
nexus3 forward myproject/worker-1 8080:3000
```

No MCP equivalent.

---

#### `harvest` — copy a guest path from every sandbox in a motive

```
nexus3 harvest <motive-id> <guest-src-path> <host-dest-dir>
```

Copies `<guest-src-path>` from every sandbox whose `motive` label equals `<motive-id>`. Each
sandbox's output is placed in `<host-dest-dir>/<sandbox-id>/`. No flags beyond the three
positionals. Returns `harvest_partial_failure` if any sandbox fails; always emits per-sandbox
outcomes before reporting the aggregate result.

```
nexus3 harvest my-motive /app/out.bundle ./bundles/
```

Primary use case is git bundle extraction from parallel agent sandboxes (see Section 5 — departures).
No MCP equivalent.

---

### 2.4 Snapshot and fork

#### `snapshot` — manage snapshots

```
nexus3 snapshot create <ref>    # take a snapshot of a running sandbox
nexus3 snapshot list            # list all snapshots
nexus3 snapshot rm <id>         # delete a snapshot by ID
```

Snapshots are retained artifacts; they are not automatically deleted when the sandbox is removed.
No additional flags on any subcommand. No MCP equivalent.

---

#### `fork` — snapshot-fork into N running children

```
nexus3 fork <ref> [--count N]
```

| Flag | Description |
|---|---|
| `--count <N>` | Number of child sandboxes to create (default: 1) |

Takes a CoW snapshot of the parent's disk and boots N children with fresh network namespaces and
vsock identities. The parent sandbox continues running. Children are independent sandboxes.

```
nexus3 fork myproject/base --count 4
```

No MCP equivalent.

---

#### `restore` — fan-out N running children from a retained snapshot

```
nexus3 restore <snapshot-id> [--count N]
```

| Flag | Description |
|---|---|
| `--count <N>` | Number of children to fan-out (default: 1) |

Creates N sandboxes from a snapshot taken with `snapshot create`. Unlike `fork`, the snapshot is
retained and can be restored from multiple times. No MCP equivalent.

---

### 2.5 Image management

#### `image` — manage guest images

```
nexus3 image build [--workspace <dir>] [--ref <tag>] [--base <ref>]
nexus3 image ls
nexus3 image prune
```

| Flag | Description | Applies to |
|---|---|---|
| `--workspace <dir>` | Workspace root containing `.nexus/Containerfile` (default: cwd) | `build` |
| `--ref <tag>` | Human-readable tag, e.g. `nexus3-base:20260807` (optional) | `build` |
| `--base <ref>` | OCI base image reference (default: `debian:bookworm-slim`) | `build` |

`build` uses an in-VM buildkitd (self-contained; no external daemon required). `prune` removes
cached images unused by any sandbox. No MCP equivalent.

---

### 2.6 SSH plumbing

These are plumbing verbs for integrating with external SSH tooling; they do not open a shell
themselves.

> **Design invariant: a sandbox IS an SSH endpoint.** Every sandbox has sshd running after boot,
> reachable over vsock port 22 via `nexus3 ssh --stdio` as a ProxyCommand. The SSH endpoint is a
> first-class access path — any toolchain that can use a ProxyCommand can reach a sandbox. `scp`,
> `rsync`, and `sshfs` work by hand through this path. They are supported but are not nexus3-owned
> verbs (that role belongs to `cp` for structured copy and `harvest` for fleet extraction).

> **Design invariant: agent selection — no inference, ever.** nexus3 never decides which AI
> agent to run in a sandbox. It never bakes in a reference to Claude, GPT, or any other model.
> The orchestrator (herdr, Orca, or a custom caller) selects the agent and passes it to the
> sandbox as a workload. nexus3's responsibility ends at the VM and vsock channel.

#### `ssh` — dial a sandbox's sshd over vsock

```
nexus3 ssh [--stdio] <ref>
```

`--stdio` enables ProxyCommand mode: splices stdin/stdout to the guest's vsock port 22. Suitable
as `ProxyCommand` in an SSH config stanza.

---

#### `config-ssh` — write an SSH config stanza for a sandbox

```
nexus3 config-ssh <ref>
```

Writes a `Host nexus3-<project>-<name>` stanza to `~/.ssh/config` using `nexus3 ssh --stdio` as
the ProxyCommand. Idempotent: if a stanza for this handle already exists, exits 0 without
modifying the file.

```
nexus3 config-ssh myproject/worker-1
# then: ssh nexus3-myproject-worker1
```

---

### 2.7 Utility

| Verb | Description | Flags |
|---|---|---|
| `auth` | OAuth login flow for in-guest Claude authentication | (not documented here) |
| `doctor` | Diagnose host environment (kernel, KVM, dependencies) | none visible |
| `version` | Print binary version | none |
| `mcp` | Start the MCP server on stdio | none |

---

### 2.8 Plugin verbs (not for direct operator use)

| Verb | Description |
|---|---|
| `orca` | VM-recipe lifecycle hooks (`create\|suspend\|resume\|destroy`) for the Orca VM plugin |
| `__herdr-plugin` | Internal herdr orchestrator plugin; hidden subcommand |

These are registered in the CLI surface table (N-AC4) but are not stable operator-facing commands.

---

## 3. Labels and selectors

Labels are arbitrary key-value metadata stamped on sandboxes at creation time.

### Stamping labels

Both `sandbox create` and `up` accept `--label KEY=VALUE`, repeatable:

```
nexus3 sandbox create myproject/w1 --image nexus3-base:latest \
  --label motive=pr-42 --label role=worker

nexus3 up --count 8 --label motive=pr-42 --label tier=ci
```

Labels are stored as a `map[string]string` in the sandbox record. They are visible in `sandbox list`
output and the `sandbox_list` MCP tool response.

### Selecting by label

**`exec --label KEY=VALUE`** selects all sandboxes matching the label and runs the command in each:

```
nexus3 exec --label motive=pr-42 -- git bundle create /tmp/out.bundle HEAD
```

When multiple `--label` flags are given they are AND-matched: a sandbox must carry all specified
labels to be selected.

**Current restriction:** `exec --label` batch mode only supports the `motive` label key. Using any
other key returns a usage error (`exec --label: batch exec currently only supports the motive label
key`). This is a known gap (see Section 6).

**`up --label`** stamps labels on creation but is not a selector (it creates new sandboxes).

There is currently **no** `sandbox stop --label`, `sandbox rm --label`, or other fleet lifecycle
selector. This is a gap vs. microsandbox (see Section 5).

### Migration from MotiveID

An earlier design used a first-class `MotiveID` field on sandbox records. Decision D-PD-21 replaced
this with generic labels. Existing sandbox records with a `MotiveID` load correctly; the field is
treated as `Labels["motive"]`. The `harvest` command and `exec --label motive=X` both use this
migration path.

---

## 4. Response envelopes

nexus3 has two separate envelope shapes, one for CLI `--json` mode and one for the MCP server.
Decision D-I1-04 keeps them separate (see rationale below).

### 4.1 CLI `--json` envelope

Every command that supports `--json` emits newline-delimited JSON objects on stdout. Each object
is either a success or an error envelope.

**Success:**
```json
{
  "schema_version": 1,
  "kind": "sandbox.created",
  "data": { ... }
}
```

**Error:**
```json
{
  "schema_version": 1,
  "kind": "error",
  "error": {
    "code": "sandbox_not_found",
    "message": "sandbox proj/name not found"
  }
}
```

`kind` identifies the event type (e.g. `sandbox.created`, `exec.done`, `harvest.done`,
`exec_batch`, `reap.report`). Error `code` values are stable machine-contract strings versioned at
`schema_version` 1; within a version, changes are additive only.

### 4.2 MCP envelope

All 7 MCP tools use a uniform envelope:

**Success:**
```json
{ "ok": true, "data": { ... }, "truncated": null }
```

**Error:**
```json
{ "ok": false, "error": { "code": "error", "message": "..." } }
```

**Truncated list:**
```json
{
  "ok": true,
  "data": [ ... ],
  "truncated": {
    "bytes_omitted": 12345,
    "total_bytes": 78900
  }
}
```

`truncated` is non-null only when the response was cut. **Currently wired for `sandbox_list` only.**
`sandbox_list` caps responses at 64 KiB; when the serialised array exceeds this cap, the handler
returns the longest fitting prefix of complete sandbox objects and sets `truncated`. All other tools
return single-object payloads that are always bounded; their `truncated` is always null.

### 4.3 Why two shapes (D-I1-04)

The CLI and MCP envelopes serve different consumers:

- **CLI** consumers are shell scripts and CI pipelines. They need stable `kind` strings for
  event routing, `schema_version` for forward compatibility, and sequential newline-delimited
  records (not a single JSON document per invocation).
- **MCP** consumers are LLM tool-call handlers. They need a minimal `ok`/`data`/`error` shape that
  maps directly to tool-call success/failure semantics, with `truncated` to handle large list
  outputs that would exceed model context limits.

Unifying to one shape would require either polluting the MCP envelope with CLI-specific `kind`/
`schema_version` fields that MCP clients ignore, or stripping `kind` from the CLI envelope and
breaking existing shell consumers. Separation is the lower-cost path.

---

## 5. Departures table and analysis

This section is the most important part of the document. The standing operator rule is:
**copy the interface from the references, do not invent surface.** Each departure is a debt that
must be justified or marked OPEN.

### 5.1 Feature comparison table

| Dimension | nexus3 | microsandbox | clawk | OpenShell |
|---|---|---|---|---|
| **Labels (stamp)** | YES — `--label KEY=VALUE` repeatable on `sandbox create`, `up` | YES — `--label KEY=VALUE` on most create/run verbs | NO | NO |
| **Selectors (fleet lifecycle)** | NO — no `sandbox stop --label` or `sandbox rm --label` | YES — `--label` on `start`/`stop`/`restart`/`rm` etc. | NO | NO |
| **Batch exec** | YES — `exec --label` with bounded parallelism (DEPARTURE — see below) | NO — exec is single-sandbox only; explicitly excluded from fleet verbs | NO | UNKNOWN |
| **Single exec** | YES — `exec <ref> -- <cmd>` | YES — `sandbox exec <name> -- <cmd>` | NO (UNKNOWN from source) | UNKNOWN |
| **Ephemeral exec** | YES — `run` (create+boot+exec+rm, guaranteed cleanup) | UNKNOWN | NO | UNKNOWN |
| **Lifecycle verbs** | create/list/rm/start/stop/pause/resume | create/ls/rm/start/stop/restart/pause/resume | start/stop (INFERRED) | create/start/stop/destroy/restart (INFERRED) |
| **Snapshot / fork** | YES — `snapshot create/list/rm`, `fork --count N` (CoW from running VM), `restore` | YES — `snapshot_create/list/inspect/remove` (from stopped sandbox) | NO | NO |
| **Artifact extraction** | YES — `harvest` (any guest path, fleet), `cp` (per-sandbox) | NO | NO (only `pr` workflow verb) | NO |
| **Git bundle / host push** | `harvest` can pull bundles; `nexus3 pr` NOT YET BUILT | NO | `clawk pr` (workflow verb, not generic) | NO |
| **Response envelope (MCP)** | `{ok, data, error, truncated}` | `{ok, data, error}` + truncation metadata (exact fields UNKNOWN) | N/A | UNKNOWN |
| **Response envelope (CLI)** | `{schema_version, kind, data/error}` newline-delimited | N/A (no documented --json) | `{schema}` versioned, additive-only | UNKNOWN |
| **Egress policy** | MITM proxy, currently AllowAll; per-sandbox configurable | Per-sandbox network policy (details UNKNOWN) | Default-deny (UNKNOWN) | Rich YAML security policy per-sandbox |
| **SSH plumbing** | `ssh --stdio` (ProxyCommand), `config-ssh` (stanza writer) | UNKNOWN | NO | `sandbox connect` (SSH interactive — INFERRED) |
| **Fleet selector on exec** | Partial (motive key only) | NO (exec is single only) | NO | UNKNOWN |

Confidence grades on reference data are inherited from the prior-art study. INFERRED = behavior
matches documented patterns but exact verb spelling unconfirmed. UNKNOWN = not found in any
accessible documentation.

### 5.2 DEPARTURES list

Each item is something nexus3 does that no reference does, or omits that they all have. A
unjustified departure is a surface-design debt.

---

**DEPARTURE 1 — batch exec across N sandboxes (`exec --label`)**

No reference ships fleet exec. microsandbox deliberately excluded `exec` from its label-driven
fleet verbs (`ps`, `start`, `stop`, `restart`, `ping`, `touch`, `rm` — no `exec`). clawk and
OpenShell have no batch exec at all.

**Status: OPEN.** Two candidates:
- Retract `exec --label` entirely; document a host-side shell loop as the pattern instead.
- Keep it, justified as a substrate-safety primitive: the `--parallel 2` default encodes
  measured host swap pressure (84% at 2 concurrent VMs). A floor of 2 is host-specific knowledge
  that a wrapper would have to duplicate or ignore.

Current implementation additionally restricts to the `motive` key only, which limits the
departure's scope but also limits its utility.

---

**DEPARTURE 2 — no fleet lifecycle selectors**

microsandbox ships `msb stop --label app=X` and `msb rm --force --label app=X`. nexus3 has no
equivalent on its lifecycle verbs. You cannot `sandbox stop --label motive=X`.

**Status: OPEN.** Could be added to `sandbox stop`, `sandbox rm`. Not justified by any recorded
decision. This is a gap vs. the primary reference.

---

**DEPARTURE 3 — guest boot-services (dockerd/buildkitd autostart)**

No reference has any concept of in-guest service autostart. nexus3 has two instances of this
pattern with different statuses:

- **dockerd autostart — REMOVED (2026-08-15).** `agent.StartDockerIfPresent` was deleted from
  `cmd/nexus3-agent/main.go`. Docker is a user workload choice, not nexus3 plumbing. It now
  requires explicit user setup: install Docker in the guest image and start dockerd after boot.
  See `docs/site/guides/docker-in-sandbox.md`.

- **buildkitd autostart — REMAINS.** buildkitd is started inside the guest when the agent is
  invoked with `--builder-role` (`cmd/nexus3-agent/main.go:107`,
  `internal/core/agent/buildkit_linux.go:87–165`). This flag is set only by the `sandbox create
  --file` build path. buildkitd is nexus3's own build plumbing — the mechanism by which
  `--file` builds guest images — not a user workload, so it was deliberately kept.

**Status: PARTIAL.** dockerd autostart resolved by deletion. buildkitd autostart remains as the
one surviving instance of the boot-services pattern (see OPEN-2).

---

**DEPARTURE 4 — workspace capture (`--workspace`, `--capture-max`)**

No reference captures a host working tree — including dirty tracked files, untracked files, and
unpushed commits — into a sandbox. clawk uses live virtiofs mounts or `git worktree`; microsandbox
and OpenShell have no equivalent.

**Justification:** Replaces git-archive (which drops dirty/untracked files). Required for
iterative in-sandbox development. Considered a core nexus3 primitive; not OPEN.

---

**DEPARTURE 5 — artifact extraction (`harvest`, `cp`)**

No reference ships an artifact-extraction primitive. `harvest` copies a guest path from every
sandbox in a motive. `cp` is per-sandbox bidirectional file copy.

**Justification:** Required for the parallel dev flow (Stage: Extract) and for general agent
workflows where in-guest work products must reach the host. clawk's `pr` is a workflow verb, not
a generic primitive.

**Partial gap:** `harvest` addresses a motive-ID (i.e., `Labels["motive"]`). A generic
`harvest --label KEY=VALUE` equivalent does not exist yet.

---

**DEPARTURE 6 — git bundle / host-side push (`nexus3 pr` — NOT YET BUILT)**

The parallel dev flow calls for `nexus3 pr` or equivalent to apply a git bundle to a host worktree
and push. This verb is described in the [parallel dev flow guide](guides/parallel-dev-flow.md) but
**does not exist** in the current CLI. `nexus3 harvest` can pull the bundle; the host push is
currently manual.

**Status: OPEN (slice P1, not yet built).**

---

**DEPARTURE 7 — fork from running VM (`fork --count N`)**

microsandbox has `snapshot_create` but requires the sandbox to be stopped first. nexus3 `fork`
takes a CoW snapshot of a running VM's disk and boots N children with fresh network namespaces.
No reference ships this.

**Justification:** Fork-from-running is required for the parallel dev flow (seeding N identical
sandboxes from a warm base without re-bootstrapping each). Considered a core primitive.

---

**DEPARTURE 8 — ephemeral one-call exec (`run`)**

No reference has a single verb that creates, boots, runs a command, and removes the sandbox with
a cleanup guarantee. microsandbox has ephemeral sandbox objects but through a different creation
path.

**Justification:** Needed for CI-style one-shot command isolation. The cleanup guarantee (not a
signal handler — a deferred service call) is a safety primitive, not a workflow opinion.

---

**DEPARTURE 9 — nested KVM (`--nested`)**

No reference exposes `/dev/kvm` inside a guest. nexus3 opt-in `--nested` enables nested
virtualization for building container images inside a sandbox without privileged containers.

**Justification:** Required for in-sandbox buildkitd with KVM acceleration. Default-off to avoid
the security surface being the default.

---

**DEPARTURE 10 — SSH plumbing verbs (`ssh --stdio`, `config-ssh`)**

OpenShell has `sandbox connect` (SSH-based interactive, INFERRED). nexus3 splits this into raw
plumbing: `ssh --stdio` is a ProxyCommand adapter; `config-ssh` writes the stanza. No shell is
opened by either verb.

**Justification:** Interoperates with any external SSH client/toolchain rather than owning the
interactive session. `shell` is the interactive verb; `ssh`/`config-ssh` are plumbing.

---

**DEPARTURE 11 — `recover` (record/substrate reconciliation)**

No reference has a verb to reconcile persisted records against the live VM substrate. nexus3 needs
this because the record store (filestore) and the hypervisor state can diverge after host crashes.

**Justification:** Operational necessity given the persistent record store design. Considered
internal plumbing.

---

**DEPARTURE 12 — plugin verbs (`orca`, `__herdr-plugin`)**

No reference bakes orchestrator-plugin hooks into the binary. These exist because nexus3's primary
deployment is as an Orca VM-recipe plugin and as a herdr substrate.

**Status:** Arguably a violation of D-PD-21 (workflow verbs in the surface). The `orca` verb is
specifically lifecycle hooks (`create|suspend|resume|destroy`) that translate Orca events to
nexus3 service calls; it has no generic-primitive justification. This may warrant extraction to a
thin wrapper binary. OPEN.

---

## 6. Known gaps and open questions

**Implementation gaps (features referenced in specs but not yet built):**

1. `nexus3 pr` — host-side git bundle application and PR creation (see [parallel dev flow guide](guides/parallel-dev-flow.md)). Not yet built.
2. `exec --label` batch exec restricted to `motive` key only. Generic label key support not yet wired.
3. `harvest` restricted to motive-ID. No `harvest --label KEY=VALUE` generalization.
4. No fleet lifecycle selectors: `sandbox stop --label` / `sandbox rm --label` do not exist.
5. MCP `truncated` field only wired for `sandbox_list`. Other tools always return `truncated: null`.
6. In-guest per-sandbox credential kind and groundwork glue for N-way parallel agent not yet built.

**Open operator decisions:**

- **OPEN-1:** Retain or retract `exec --label` batch exec? (DEPARTURE 1) — no reference does this;
  microsandbox explicitly excluded it from fleet verbs. Decision needed before surface stabilises.
- **OPEN-2:** Docker autostart resolved by deletion; buildkitd autostart remains, special-cased to
  `--builder-role`. Whether this surviving instance warrants a generic boot-services declaration
  mechanism (rather than staying as special-cased code) is undecided. (DEPARTURE 3)
- **OPEN-3:** Extract `orca` plugin verb to a thin wrapper binary? (DEPARTURE 12)
- **OPEN-4:** Add fleet lifecycle selectors (`sandbox stop --label`, `sandbox rm --label`) to close
  the gap vs. microsandbox? (DEPARTURE 2)
- **OPEN-5:** Should `harvest` accept `--label KEY=VALUE` as a selector, or remain motive-specific?

**Confidence notes inherited from prior-art study:**

- OpenShell CLI reference returned 404 during research. All OpenShell claims marked INFERRED or
  UNKNOWN are from README + quickstart only; the full CLI reference was not accessible.
- clawk label system: NONE confirmed from `docs/commands.md` grep. clawk is early-stage; a label
  system may be planned but is not shipped as of 2026-08-15.
- microsandbox truncation field exact spellings (`bytes_omitted`, `total_bytes`) — the prose says
  "includes truncation metadata when shortened" but does not spell out field names. These are
  UNKNOWN from indexed docs; nexus3's own names are `bytes_omitted` / `total_bytes`.
