# Branch Dev Session Lifecycle (Target Design)

## Purpose

Define the target lifecycle and operating rules for branch-owned development sessions in the project-first model.

This is internal design guidance only; workspace remains implemented CLI.

## State Machine

Target lifecycle:

`idle -> active -> suspended -> active -> archived`

## State Definitions

- `idle`: branch exists, no running dev session.
- `active`: one live remote-host dev session attached to the branch.
- `suspended`: branch session paused/stopped but resumable.
- `archived`: branch lifecycle is complete; session cannot resume.

## Runtime Semantics

- Remote host runtime is mandatory for all dev sessions, including local-hardware hosts used as remote endpoints.
- Branch ownership rule: one active session per branch.
- Parallel work path: forked branches only; no multi-session concurrency on a single branch.

## Lifecycle Transitions

- `idle -> active`: start dev session for branch.
- `active -> suspended`: pause/sleep/stop session while preserving resumable context.
- `suspended -> active`: resume session.
- `active -> archived` or `suspended -> archived`: finalize branch session lifecycle.

## Interaction With Version and Environment

- Version cut points happen from branch state, not from mutable environment state.
- Version outputs are immutable and deploy-only.
- Environment/deploy actions consume versions and remain outside branch session mutability.

## Non-goals For This Stage

- non-goal: immediate rename of existing workspace packages/types.
- non-goal: immediate shipping of project-first command groups.
- non-goal: compatibility aliases in user docs before implementation exists.

This stage does not require command removals or direct changes to current `nexus workspace` behavior.

## Likely Future Integration Surfaces

- CLI orchestration: `packages/nexusd/internal/cli/root.go`, `packages/nexusd/internal/cli/workspace.go`
- Runtime/API behavior: `packages/nexusd/pkg/server/server.go`
- Persisted lifecycle state: `packages/nexusd/internal/state/store.go`
- Type and config boundaries: `packages/nexusd/internal/types/types.go`, `packages/nexusd/internal/config/config.go`
