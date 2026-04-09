# SQLite Node Store Cutover Design

Date: 2026-04-09
Status: Approved for planning
Owner: workspace-daemon

## Context

The daemon currently has a partially completed persistence migration:

- `pkg/store` has sqlite wiring and goose migrations.
- Workspace manager persistence is partially integrated.
- Spotlight persistence still uses JSON snapshot save/load behavior.

This design completes a single-phase (big-bang) cutover to sqlite as the only node-level persistence backend.

## Decisions

1. Cutover strategy: big-bang in one PR.
2. Legacy JSON migration: none.
3. Behavior on empty sqlite after upgrade: silent empty state.
4. Legacy JSON codepaths: remove now.

## Goals

1. Make sqlite the only persistence backend for daemon node-level state.
2. Remove JSON persistence reads/writes for workspace and spotlight state.
3. Preserve existing RPC semantics while changing storage internals.
4. Keep node-level DB path defaults aligned with `~/.nexus` when XDG vars are unset.
5. Prove restart persistence in package tests and live runtime verification.

## Non-Goals

1. Backfill/import data from legacy JSON persistence files.
2. Temporary dual-read or compatibility fallback to JSON.
3. Additional user-facing migration UX beyond existing API behavior.

## Architecture

### Persistence boundary

`pkg/store` is the single persistence boundary. Other packages do not perform direct persistence file I/O for node state.

- `pkg/workspacemgr` uses store APIs for create/update/delete/list/get operations.
- `pkg/spotlight` and `pkg/server` use store APIs for forward persistence and hydration.

### Startup/shutdown lifecycle

- Startup:
  - Open sqlite DB at node-level path.
  - Run goose migrations.
  - Hydrate runtime state from sqlite rows.
- Shutdown:
  - No JSON snapshot write step.
  - Runtime mutations are persisted during normal operations through store writes.

### Behavior guarantees

- Empty DB is valid and non-error.
- No warnings for missing legacy JSON data.
- Legacy JSON files are ignored and never written.

## Data Model

The schema remains migration-managed under `pkg/store/migrations`.

### Workspace persistence

- Table keyed by `workspace_id`.
- Stores all data required for manager hydration and RPC-visible behavior.
- Uses deterministic upsert semantics for updates.

### Spotlight persistence

- Table keyed by (`workspace_id`, `forward_id`).
- Stores forward metadata needed for restart restoration.
- Supports idempotent upsert/remove operations.

## Runtime Data Flow

1. Workspace mutation path:
   - handler -> workspacemgr -> store upsert/delete.
2. Spotlight mutation path:
   - handler -> spotlight manager/server -> store upsert/delete.
3. Read/list path:
   - runtime serves from hydrated state and/or store lookups depending on current package boundary.
4. Restart:
   - daemon restarts with same workspace dir.
   - state is restored from sqlite only.

## Error Handling

1. DB open or migration failure is startup-fatal.
2. Store CRUD errors propagate through existing RPC error pathways.
3. Empty sqlite state returns existing empty/not-found semantics.

## Code Changes

1. Remove JSON persistence helpers and file-path dependencies in:
   - `pkg/workspacemgr`
   - `pkg/spotlight`
   - `pkg/server`
2. Ensure all persistence entry points call store APIs.
3. Remove tests asserting JSON snapshot behavior.
4. Add/adjust tests to assert sqlite-backed persistence behavior.

## Testing and Verification

### Package tests

Run:

```bash
go test ./pkg/store ./pkg/workspacemgr ./pkg/spotlight ./pkg/server ./pkg/handlers ./pkg/config ./cmd/nexus -count=1
```

Expected:

- All tests pass.
- No new type/lint failures in touched scope.

### Restart persistence proof

Run a live daemon proof on a non-`:8080` port (for example `8101`) with isolated workspace dir.

Evidence to capture:

1. `spotlight.list` before apply is empty.
2. `spotlight.applyComposePorts` creates forwards.
3. `spotlight.list` after apply shows forwards.
4. Daemon restart with same workspace dir.
5. `spotlight.list` after restart shows equivalent forwards restored from sqlite.

## Documentation Updates

Update `docs/dev/internal/testing/2026-04-09-workspace-robustness-persistence-and-versioning.md` with:

1. sqlite-only persistence behavior.
2. explicit "no JSON migration" and "silent empty state" decision notes.
3. test command output and runtime proof results.

## Risks and Mitigations

1. Risk: hidden JSON coupling remains in call sites.
   - Mitigation: grep for JSON persistence files/functions and remove all references.
2. Risk: cross-test contamination through shared node DB path.
   - Mitigation: preserve temp-root-local DB path behavior in tests.
3. Risk: restart rehydration mismatch with runtime in-memory structures.
   - Mitigation: targeted restart tests plus live daemon proof.

## Acceptance Criteria

1. No runtime reads/writes of legacy workspace/spotlight JSON persistence files.
2. Workspace and spotlight state persist and restore via sqlite across daemon restart.
3. Behavior with empty sqlite is silent empty responses (no migration, no warning requirement).
4. Touched package test suites pass.
5. Internal testing doc updated with concrete evidence.
