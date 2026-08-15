# 17 — Surface Contract

*Produces: normative description of the canonical API, surface stability expectations, and the
uniform MCP response envelope. Part of motive `nexus3-parallel-dev-pr-flow` slice I1.*

*Accuracy rule: every claim names its source. Code citations were verified against HEAD on
2026-08-15. Symbol name is the stable key; line number is a hint that may drift.*

---

## 1 — The canonical surface decision (I1-AC1)

### Decision: D-PD-16 UPHELD — `internal/core/service` is the canonical API

The charter entry D-PD-16 proposed `internal/core/service` as the canonical surface. Open item
TBR-PD-13 challenged this, asking whether the `SandboxService` interface in `internal/mcp` should
instead be treated as canonical — reasoning that it is protocol-neutral and already machine-readable.

**TBR-PD-13 is rejected.** Rationale:

1. **SandboxService is a subset, not a superset.** The interface
   (`internal/mcp/server.go:SandboxService`) exposes only 8 methods: Create, CreateAndBoot, List,
   Start, Stop, Pause, Resume, Remove. It omits Exec, Fork, Snapshot, SnapshotList,
   SnapshotRemove, RestoreFromSnapshot, Harvest, GetByMotive, Forward, CP — operations that all
   surfaces need and that must not be re-derived per surface. A canonical surface that omits half
   the operations is not canonical; it is a projection.

2. **The service layer is already where correctness lives.** Intent journaling (`intent.go`),
   lifecycle guarding (`service.go:machine`), orphan reaping (`reap.go`), and supervisor
   lifecycle (`supervisor.go`) all live in `internal/core/service`. Nominating a narrower
   interface above this layer as canonical means each surface must re-implement these invariants
   independently — which is precisely the problem that caused the B3 defect.

3. **Protocol-neutrality is already satisfied.** `*service.Service` is pure Go; it has no
   CLI-specific or MCP-specific concerns. MCP and CLI are thin adapters that translate surface
   inputs (tool call arguments, CLI flags) into service method calls.

**Normative statement (D-PD-16, ratified here):**

> `internal/core/service` — specifically `*service.Service` for lifecycle operations and
> `service.CreateAndBoot` for the create-and-boot fast path — is the canonical API. Every
> behavior reachable from any nexus3 surface (CLI, MCP, herdr plugin, orca integration) **must**
> be reachable from this layer. No surface may implement business logic — lifecycle rules,
> preflight validation, cleanup invariants, error taxonomy — that is not expressed through or
> derivable from this layer.

### What "thin adapter" means in practice

"Thin adapter" does not mean "zero surface-specific code." Each surface has legitimate
presentation concerns:

| Surface | Legitimate surface-specific code | Forbidden duplication |
|---|---|---|
| CLI | Flag parsing, interactive output, streaming I/O, exit codes | Lifecycle rules, preflight |
| MCP | Tool schema, response serialisation, truncation metadata | Lifecycle rules, preflight |
| herdr plugin | Plugin manifest, JSON handshake protocol | Lifecycle rules, preflight |
| Orca integration | Workspace handshake, direct-SSH fallback | Lifecycle rules, preflight |

### TBR-PD-13 closed

TBR-PD-13 is resolved by this document. The decision is D-PD-16 as ratified above. The
`SandboxService` interface remains as a convenience boundary for the MCP server's dependency
injection — it is a projection of the canonical API, not the canonical API itself.

---

## 2 — Canonical API surface inventory

### 2.1 — `*service.Service` — lifecycle operations (stable internal API)

| Method | Trigger |
|---|---|
| `Create(ctx, project, name, CreateOptions)` | mint record |
| `List(ctx)` | enumerate |
| `Start(ctx, ref)` | boot |
| `Stop(ctx, ref)` | terminate |
| `Pause(ctx, ref)` | suspend |
| `Resume(ctx, ref)` | unsuspend |
| `Remove(ctx, ref)` | delete record + disk resources |
| `Exec(ctx, ref, agent.ExecOptions)` | run command in guest |
| `Fork(ctx, ref, count)` | snapshot-fork |
| `Snapshot(ctx, ref)` | take snapshot |
| `SnapshotList()` | list snapshots |
| `SnapshotRemove(ctx, id)` | delete snapshot |
| `RestoreFromSnapshot(ctx, id, count)` | restore |
| `GetByMotive(ctx, motiveID)` | filter by motive |
| `Forward(ctx, ref, ...)` | port forward |

### 2.2 — `service.CreateAndBoot` — create-and-boot (package-level function)

```go
func CreateAndBoot(
    ctx context.Context,
    svc *Service,
    cache *image.Cache,
    newDriver DriverFactory,
    probe ProbeFunc,
    project, name string,
    opts CreateAndBootOptions,
) (domain.Sandbox, error)
```

This is a free function rather than a method because it takes infrastructure parameters
(`DriverFactory`, `ProbeFunc`, `*image.Cache`) that are wiring concerns owned by each surface's
adapter, not by the service layer itself. Each surface's adapter resolves these from its local
configuration (environment variables, CLI flags, plugin context) and passes them in.

**This wiring separation is correct and intentional.** It does not weaken the canonical API
guarantee: the business logic of create-and-boot (intent journaling, CoW disk copy, probe timeout,
cleanup on failure) lives entirely inside `CreateAndBoot`. Surfaces supply infrastructure; they do
not reimplement logic.

### 2.3 — `service.RunEphemeral` — ephemeral one-call exec (I1-AC3, new in this slice)

```go
func RunEphemeral(
    ctx context.Context,
    svc *Service,
    cache *image.Cache,
    newDriver DriverFactory,
    probe ProbeFunc,
    project, name string,
    opts CreateAndBootOptions,
    argv []string,
    stdin io.Reader,
    stdout, stderr io.Writer,
) (exitCode int32, err error)
```

Composes CreateAndBoot → Exec → Remove in a single call with guaranteed cleanup. Defined in
`internal/core/service/ephemeral.go`. See §5 for the cleanup guarantee and fault-injection proof.

---

## 3 — Surface stability expectations

| Surface | Stability contract | Breaking-change process |
|---|---|---|
| CLI verb names + flag spellings | **User-stable**: not changed without a deprecation notice | Changelog entry + flag alias for one release |
| MCP tool names + schema | **Agent-stable**: changes break automated workflows | Additive-only preferred; schema version bump on breaking |
| `SandboxService` interface | **Internal**: no external versioning, may change across commits | No process — update callers in the same commit |
| `*service.Service` methods | **Internal**: no external versioning | No process — update callers in the same commit |
| `service.CreateAndBoot` signature | **Internal** | No process — update callers in the same commit |

The `SandboxService` interface is deliberately NOT marked stable. If callers outside the `mcp`
package need to depend on `*service.Service`, they should import it directly.

---

## 4 — Uniform MCP response envelope (I1-AC2)

All MCP tool responses use this envelope, defined in `internal/mcp/envelope.go`:

```json
{
  "ok": true,
  "data": { ... },
  "truncated": null
}
```

On error (returned as an MCP error response, not a successful envelope):

```json
{
  "ok": false,
  "error": {
    "code": "sandbox.not_found",
    "message": "sandbox ref 'foo/bar' not found"
  },
  "truncated": null
}
```

When output is capped (e.g., exec stdout, large list):

```json
{
  "ok": true,
  "data": "...first N bytes...",
  "truncated": {
    "bytes_omitted": 12345,
    "total_bytes": 15000
  }
}
```

### 4.1 — `truncated` semantics

`truncated` is `null` when the response is complete. When non-null, it carries:
- `bytes_omitted`: bytes excluded from `data` (total_bytes − len(data))
- `total_bytes`: the full size before truncation

A caller can distinguish a short answer from a cut-off one by checking whether `truncated` is
non-null. This is designed for slice I2 (MCP exec output capture) but the envelope applies to all
tools now so I2 does not introduce a schema discontinuity.

### 4.2 — Current tool wrapping

The 7 sandbox lifecycle tools (`sandbox_create`, `sandbox_list`, `sandbox_start`, `sandbox_stop`,
`sandbox_pause`, `sandbox_resume`, `sandbox_remove`) wrap their JSON payloads in the envelope via
`successResult()` and `errorResult()` in `internal/mcp/envelope.go`.

`sandbox_list` additionally applies a 64 KiB byte cap via `listResultWithCap()` in `server.go`:
when the serialised array exceeds the cap, the handler returns the longest fitting prefix via
`successWithTruncation()` with non-null `truncated`. All other tools return single-object payloads
that are always bounded; their `truncated` field is always null.

---

## 4.3 — CLI envelope vs MCP envelope: design decision (D-I1-04)

The CLI (`internal/cli/output.go`) and the MCP surface (`internal/mcp/envelope.go`) both use an
envelope pattern for machine-readable output. They are **deliberately separate schemas**.

### CLI envelope (`--json` mode)

```json
{ "schema_version": 1, "kind": "sandbox.list", "data": [...] }
{ "schema_version": 1, "kind": "error", "error": {"code": "...", "message": "..."} }
```

Fields: `schema_version`, `kind`, `data` | `error`.

### MCP envelope (all MCP tool responses)

```json
{ "ok": true, "data": {...}, "truncated": null }
{ "ok": false, "error": {"code": "...", "message": "..."} }
```

Fields: `ok`, `data` | `error`, `truncated`.

### Rationale for separation

| Concern | CLI `--json` | MCP |
|---|---|---|
| Primary consumer | Operators, shell scripts, CI/CD pipelines | AI agents (Claude, etc.) |
| Schema stability | `schema_version` for forward compat across CLI releases | Additive-only via MCP tool versioning |
| Type discrimination | `kind` string for multi-command pipelines | `ok` boolean (MCP convention) |
| Truncation metadata | Not needed (output streams to terminal/script) | `truncated` for agent context budgets |
| Breaking-change process | Changelog + flag alias for one release | Version bump or additive field |

**Unifying the two schemas would optimize for neither consumer.** CLI callers know `schema_version`
and `kind`; changing those to `ok` and `truncated` would break existing shell scripts. MCP clients
expect `ok` and the optional `truncated` field; adding `schema_version` and `kind` would add noise
that no agent currently parses.

The separation is not accidental duplication — it is an intentional design boundary between two
different stability contracts serving different parties. Both surfaces share the *principle*
(every machine-readable response is enveloped), but the field schemas are adapted to their
respective consumers.

**Decision (D-I1-04):** CLI `--json` and MCP output use separate envelope types. No code
unification. The canonical source for each is: `internal/cli/output.go` (CLI) and
`internal/mcp/envelope.go` (MCP).

---

## 5 — `nexus3 run` — ephemeral one-call exec (I1-AC3)

### 5.1 — Cleanup guarantee

`service.RunEphemeral` guarantees sandbox removal **even on exec error or context cancellation
(SIGINT/SIGTERM)**:

1. `CreateAndBoot` writes a create-intent file before materialising disk resources
   (`intent.go:writeCreateIntent`). This makes the sandbox visible to the reaper even if the
   process is killed between create and store.Create.
2. `RunEphemeral` defers `svc.Remove(context.WithoutCancel(ctx), id)` immediately after
   CreateAndBoot returns (whether create succeeded or failed — on failure Remove is a no-op
   because no record was written). The background context ensures Remove runs even after the
   caller's context is cancelled.
3. On SIGINT or SIGTERM, `internal/cli/root.go:Run` cancels the command context via
   `signal.NotifyContext`. The exec is interrupted (context cancellation propagates into the
   agent dial); the deferred Remove runs with `context.WithoutCancel(ctx)` and therefore
   succeeds even though the original context is cancelled.

### 5.2 — CLI invocation

```
nexus3 run [--memory MiB] [--vcpus N] [--project P] [--name N] <image-ref> -- <cmd> [args...]
```

Returns stdout, stderr (streamed to terminal), and exit code. With `--json`: returns
`{"exit_code": N}` on stdout on completion.

### 5.3 — Fault-injection proof

`TestRunEphemeral_ZeroLeftoversOnFault` in `internal/core/service/ephemeral_test.go`:
- Uses a fake driver that boots successfully but whose Exec call returns an error
- Calls `RunEphemeral`; expects it to return the exec error
- After the call, calls `ResourceIndex.List()` on the disk directory
- Asserts no resources remain for the sandbox ULID
- `TestResourceIndex_RecordFree` (existing) guards the record-free property of `ResourceIndex`;
  this test does not weaken it.

---

## 6 — Surface parity check (I1-AC4)

`internal/cli/surface_parity_test.go` contains `TestSurfaceParity`, which:

1. Calls `cli.All()` to enumerate registered CLI verbs (determined at link time by `init()`
   functions).
2. Compares the set against `SurfaceContract` — an explicit table defined in
   `internal/cli/surface_parity_test.go` that maps each CLI verb to its canonical API entry
   points.
3. Compares the MCP tool name set (from `mcp.KnownTools()`, a new function in
   `internal/mcp/server.go`) against the same table.
4. Fails with a descriptive message if any CLI verb or MCP tool has no entry in the table, or if
   any entry in the table names a verb or tool that does not exist.

N-AC4 is asserted by rule 4, first clause: a CLI verb absent from the table means behaviour
exists in CLI that is undeclared with respect to the canonical API, and the test fails.

Drift found at time of I1 (reported, not fixed — B6 owns the affected files):

| Drift | Detail | Owner |
|---|---|---|
| MCP `sandbox_create` (boot path) bypasses `resolveKernelPath()` | B6 is patching this | B6 |
| herdr `launch` bypasses `resolveKernelPath()` | B6 is patching this | B6 |
| orca `orcaCreate` bypasses `resolveKernelPath()` | B6 is patching this | B6 |

These are preflight-bypass drifts (business logic not shared), not surface-parity drifts (verb/tool
enumeration). The parity test catches enumeration drift; the preflight fix is B6's.

---

## 7 — Architecture residuals (things this slice does not change)

- `service.CreateAndBoot` remains a free function. Callers supply infrastructure. This is correct.
- The herdr plugin's `launch` path (`cmd_herdr_plugin.go:herdrPluginLaunch`) and `orcaCreate`
  (`cmd_orca.go:orcaCreate`) are NOT refactored to call `service.CreateAndBoot` in this slice.
  That refactoring is a separate migration tracked in the charter. This slice defines the contract;
  B6 adds preflight; the full adapter consolidation is future work.
- The `SandboxService` interface in `internal/mcp/server.go` is NOT promoted or exported. It
  remains an internal adapter seam.

---

## 8 — Decision record

| ID | Decision | Rationale |
|---|---|---|
| D-PD-16 | Canonical API is `internal/core/service` | Only layer with complete operation set; business logic already lives here |
| TBR-PD-13 | CLOSED-REJECTED | `SandboxService` is a subset; cannot be canonical |
| D-I1-01 | MCP envelope wraps all tool responses | Consistent parsing; truncation metadata enables I2 exec output |
| D-I1-02 | `nexus3 run` uses deferred Remove with background context | Guarantees cleanup under SIGINT and exec error |
| D-I1-03 | Parity check uses explicit table, not reflection | Predictable, self-documenting, no SDK dependency |
| D-I1-04 | CLI `--json` and MCP envelopes remain separate | Different consumers, different stability contracts, different field semantics |
