# Roadmap

Focus: remote workspace core quality and usability.

**Verify:** `packages/nexus` — `go test ./...`; `packages/sdk/js` — `pnpm exec tsc --noEmit` and `pnpm exec jest --runInBand`.

---

## How to use this file

- Update when an item changes phase, priority, links, or exit criteria.
- Keep details in linked issue/plan docs; keep this file concise.
- Treat this as the source of prioritization truth for engineering execution.

### Status legend

- `proposed`: idea captured, not yet accepted
- `planned`: accepted and queued
- `in_progress`: active implementation
- `blocked`: active but waiting on dependency/decision
- `done`: completed and verified
- `dropped`: intentionally de-scoped

---

## Current iteration

Iteration: `2026-Q2`  
Priority rule: `P0 reliability > P1 UX consistency > P2 surface simplification`


| Item                                    | Pri | Status   | Links    | Exit criteria                                                             |
| --------------------------------------- | --- | -------- | -------- | ------------------------------------------------------------------------- |
| Live port detection                     | P0  | planned  | plan TBD | New listening ports appear in `tunnel.list` without manual `tunnel.add()` |
| Workspace-configured default port rules | P1  | planned  | plan TBD | `.nexus/workspace.json` rules are auto-applied at workspace startup       |
| Resumable `nexus run`                   | P1  | planned  | plan TBD | Disconnect/reconnect does not lose ephemeral run command state            |
| Roadmap/plan convergence tooling        | P2  | proposed | plan TBD | CI warns on stale roadmap links/status drift                              |


---

## Near-term queue


| Item                                   | Pri | Status   | Notes                                                  |
| -------------------------------------- | --- | -------- | ------------------------------------------------------ |
| Runtime-backed tunnel event stream     | P0  | proposed | foundation for live port detection                     |
| Workspace policy schema evolution      | P1  | proposed | needed for default port rules + future policy features |
| Ephemeral session persistence strategy | P1  | proposed | needed for resumable `nexus run`                       |


---

## Long-term: Multi-User Architecture

Vision: Enable Nexus to operate as a **hybrid architecture** supporting:
1. **Personal Daemon** (`mode: "personal"`): Single-user daemon on laptop/PC (current)
2. **Pool Daemon** (`mode: "pool"`): Multi-user daemon serving many users (future)
3. **Federation**: Cross-daemon workspace sharing (future)

| Phase | Description | Status | Links |
| ----- | ----------- | ------ | ----- |
| Phase 1: Data Model Prep | Auth infrastructure, self-managed tokens, workspace ownership | **done** | [Design](../docs/superpowers/specs/2026-04-12-multi-user-multi-tenant-architecture-design.md), [Plan](../docs/superpowers/plans/2026-04-12-multi-user-prep-implementation.md), [PR #33](https://github.com/IniZio/nexus/pull/33) |
| Phase 2: Pool Mode + OIDC | Multi-user daemon with OIDC auth (Device Code flow) | planned | — |
| Phase 3: Federation | Cross-daemon workspace sharing | proposed | — |
| Phase 4: Multi-Tenancy | Tenant isolation, RBAC, audit logging | proposed | — |

---

## Completed recently


| Item                                             | Pri | Status | Completed in | Links                                                                   |
| ------------------------------------------------ | --- | ------ | ------------ | ----------------------------------------------------------------------- |
| Typed RPC registry and SDK request schema typing | P0  | done   | 2026-Q2      | `docs/superpowers/plans/2026-04-12-typed-rpc-registry.md`               |
| Cobra CLI migration                              | P1  | done   | 2026-Q2      | `docs/superpowers/plans/2026-04-12-cobra-cli-migration.md`              |
| Folded `capabilities.list` into `node.info`      | P2  | done   | 2026-Q2      | `docs/superpowers/plans/2026-04-12-fold-capabilities-into-node-info.md` |
| **Multi-User Architecture: Phase 1**           | P1  | done   | 2026-Q2      | [Design](../docs/superpowers/specs/2026-04-12-multi-user-multi-tenant-architecture-design.md), [PR #33](https://github.com/IniZio/nexus/pull/33) |


---

## Stable baseline

- Workspace lifecycle: create, start, stop, remove, pause, resume, restore, fork
- `nexus run`: ephemeral workspace create → exec → remove
- `nexus shell` / `nexus exec`: interactive and non-interactive PTY sessions
- Host credential forwarding auto-bundled on `create` / `run`
- Tunnel manual operations (`add` / `stop` / `list`) and compose port detection (`nexus tunnel`)
- SDK single `WorkspaceClient` with auto-transport and `start()` returning a handle
