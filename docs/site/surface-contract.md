# nexus3 surface contract

**Purpose:** normative rules about what the canonical API is, which layer is authoritative, how
the parity invariant is enforced, and which design decisions are closed vs. open. This is not a
command reference — see [Surface reference](surface.md) for that.

---

## 1. The canonical API decision (D-PD-16)

**Decision D-PD-16 (2026-08-15, CLOSED):** The canonical API is `internal/core/service`
(`*service.Service` and its package-level helper functions). This layer is the single source of
truth for all sandbox operations.

**Why not the `SandboxService` interface (TBR-PD-13, CLOSED-REJECTED):** An earlier proposal
(TBR-PD-13) suggested narrowing the canonical surface to an 8-method `SandboxService` interface
(Create, List, Start, Stop, Pause, Resume, Remove, and one exec method). That interface was
rejected because it omits operations that are fully implemented and exposed through the CLI:

| Omitted by `SandboxService` | Present in `*service.Service` | CLI verb |
|---|---|---|
| `Exec` | `agent_ops.go:29` | `exec`, `shell`, `attach`, `run` |
| `Fork` | `service.go:877` | `fork` |
| `Snapshot`, `SnapshotList`, `SnapshotRemove` | `service.go:814,962,975` | `snapshot` |
| `RestoreFromSnapshot` | `service.go:1025` | `restore` |
| `HarvestMotive` | `harvest.go:47` | `harvest` |
| `DialGuestPortForward` | `forward_ops.go:19` | `forward` |
| ~~`BatchExec`~~ | retracted with `exec --label` (D-PD-30, 2026-08-15); `batch_exec.go` deleted | — |

Using `SandboxService` as the canonical surface would mean these operations have no declared
canonical backing, which would break the N-AC4 parity invariant. TBR-PD-13 is permanently closed.

**What "thin adapter" means in practice:** The CLI and MCP server are adapters over
`internal/core/service`. They parse flags/JSON, call the service, and format output. No business
logic lives in the adapter layer — `internal/cli/` and `internal/mcp/` are wiring only.

---

## 2. Canonical API surface inventory

### 2.1 `*service.Service` — lifecycle and execution operations

Verified against source as of 2026-08-15. The table covers user-facing operations; builder and
configuration methods (`WithArtifacts`, `WithBroker`, `WithSSHSeeder`, `WithCASeeder`,
`WithDiskDir`) are omitted as internal plumbing.

| Method | Source | CLI verb | MCP tool |
|---|---|---|---|
| `Create(ctx, project, name, CreateOptions)` | `service.go:175` | `sandbox create` | `sandbox_create` |
| `List(ctx)` | `service.go:227` | `sandbox list` | `sandbox_list` |
| `GetByLabels(ctx, labels)` | `service.go:324` | (label selector) | `sandbox_list` |
| `GetByMotive(ctx, motiveID)` | `service.go:331` | (motive selector) | — |
| `Start(ctx, ref)` | `service.go:368` | `sandbox start` | `sandbox_start` |
| `Stop(ctx, ref)` | `service.go:446` | `sandbox stop` | `sandbox_stop` |
| `Pause(ctx, ref)` | `service.go:485` | `sandbox pause` | `sandbox_pause` |
| `Resume(ctx, ref)` | `service.go:525` | `sandbox resume` | `sandbox_resume` |
| `Remove(ctx, ref)` | `service.go:583` | `sandbox remove` | `sandbox_remove` |
| `Exec(ctx, ref, agent.ExecOptions)` | `agent_ops.go:29` | `exec`, `shell`, `attach`, `run` | — |
| ~~`BatchExec`~~ | retracted (D-PD-30) | — | — |
| `Fork(ctx, ref, count)` | `service.go:877` | `fork` | — |
| `Snapshot(ctx, ref)` | `service.go:814` | `snapshot create` | — |
| `SnapshotList()` | `service.go:962` | `snapshot list` | — |
| `SnapshotRemove(ctx, id)` | `service.go:975` | `snapshot remove` | — |
| `RestoreFromSnapshot(ctx, snapID, count)` | `service.go:1025` | `restore` | — |
| `HarvestMotive(ctx, motiveID, src, dst)` | `harvest.go:47` | `harvest` | — |
| `DialGuestPortForward(ctx, ref, guestPort)` | `forward_ops.go:19` | `forward` | — |

> **Code/spec discrepancy (documented):** Spec 17 listed this method as `Forward`. The actual
> method name is `DialGuestPortForward` (`forward_ops.go:19`). The site documents the code.

### 2.2 `service.CreateAndBoot` — create-and-boot helper

```
func CreateAndBoot(ctx, svc *Service, project, name string, opts CreateAndBootOptions) (domain.Sandbox, error)
```

Source: `create.go:300`. This package-level function creates a sandbox record and immediately
boots it, returning the booted sandbox. Used by `sandbox create --boot`, `__herdr-plugin launch`,
`orca create`, and `MCP sandbox_create`. It is not a method on `*Service` — it calls `Create` then
`Start` with additional boot logic in between.

### 2.3 `service.RunEphemeral` — ephemeral one-call exec

```
func RunEphemeral(ctx context.Context, svc *Service, project string, opts RunEphemeralOptions) (int32, error)
```

Source: `ephemeral.go:21`. Creates, boots, execs a command, and removes the sandbox. The Remove
call is deferred with a background context so cleanup runs even under SIGINT or exec error
(decision D-I1-02). Used by `nexus3 run`.

---

## 3. Surface stability expectations

| Tier | What it covers | Stability |
|---|---|---|
| **Stable** | `*service.Service` lifecycle ops (Create/List/Start/Stop/Pause/Resume/Remove) | Breaking changes require a version bump |
| **Stable** | `CreateAndBoot`, `RunEphemeral` | Breaking changes require a version bump |
| **Internal** | Snapshot/Fork/Restore, HarvestMotive, DialGuestPortForward | May change as design matures; callers are adapters only |
| **CLI adapter** | `internal/cli/` flag parsing | Not API — CLI flags are user-visible but not a library API |
| **MCP adapter** | `internal/mcp/` tool definitions | MCP tool names are stable; response shapes may be versioned |

The CLI and MCP layers are **thin adapters**: no business logic, no database access, no driver
calls. All of that lives in `internal/core/service` or below (`internal/core/driver`,
`internal/core/agent`).

---

## 4. The parity invariant (N-AC4)

**Rail N-AC4:** No behaviour reachable from a CLI command may be unreachable from the canonical
API. No MCP tool may return an unenveloped response.

The parity invariant is enforced at test time by `TestSurfaceParity` in
`internal/cli/surface_parity_test.go`. The test:

1. Calls `cli.All()` to enumerate every registered CLI verb (determined at link time by `init()`
   functions).
2. Calls `mcp.KnownTools()` to enumerate every registered MCP tool.
3. Compares both sets against `surfaceMap` — an explicit table in
   `internal/cli/surface_parity_test.go` that maps each CLI verb to its canonical API entry points
   and its MCP tool equivalents.
4. Fails if any CLI verb or MCP tool has no entry in the table (N-AC4 violation: behaviour exists
   with no declared canonical backing).
5. Warns (not fails) if a table entry names a verb or tool that is not registered (forward-compat
   for planned but not-yet-built operations).

The table is the assertion. Adding a new CLI verb without adding a `surfaceMap` entry is a CI
failure.

### Drift known at time of I1 slice (reported, not yet fixed)

These are preflight-bypass drifts, not surface-parity drifts — the parity test enumerates verbs
and tools, not internal call paths:

| Drift | Detail | Owner |
|---|---|---|
| MCP `sandbox_create` bypasses `resolveKernelPath()` | B6 slice patching | B6 |
| herdr `launch` bypasses `resolveKernelPath()` | B6 slice patching | B6 |
| orca `orcaCreate` bypasses `resolveKernelPath()` | B6 slice patching | B6 |

---

## 5. MCP and CLI response envelopes

The envelope shapes and the decision to keep them separate (D-I1-04) are documented in the
[Surface reference](surface.md#4-response-envelopes) (section 4). That page is the reader-facing
reference; this section captures the normative rule.

**MCP envelope rule (D-I1-01):** Every MCP tool response must be wrapped in the MCP envelope.
No tool may return a bare string or bare JSON object. The wrapper is:

```json
{
  "sandboxes": [...] | "output": "...",
  "truncated": true | false | null,
  "bytes_omitted": 1234,
  "total_bytes": 5678
}
```

**Known gap:** `truncated` is only wired for `sandbox_list` today. All other tools return
`truncated: null`. This is tracked in the [known gaps](surface.md#6-known-gaps-and-open-questions)
register.

**CLI envelope rule:** `--json` mode wraps all output in `{"ok": true|false, "data": ...,
"error": "..."}`. This is a separate shape from the MCP envelope (decision D-I1-04).

---

## 6. Decision record

| ID | Decision | Status |
|---|---|---|
| D-PD-16 | Canonical API is `internal/core/service` (`*service.Service`) | CLOSED |
| TBR-PD-13 | `SandboxService` interface as canonical surface | CLOSED-REJECTED |
| D-I1-01 | MCP envelope wraps all tool responses | CLOSED |
| D-I1-02 | `nexus3 run` uses deferred Remove with background context | CLOSED |
| D-I1-03 | Parity check uses explicit table, not reflection | CLOSED |
| D-I1-04 | CLI `--json` and MCP envelopes remain separate shapes | CLOSED |

---

*Cross-link: [Surface reference](surface.md) — the complete command reference with syntax,
flags, MCP tool names, and the departures table.*
