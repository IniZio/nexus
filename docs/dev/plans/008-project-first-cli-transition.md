# Plan 008: Project-First CLI Transition (Staged, Internal)

**Status:** Draft

## Intent

Define a staged migration from the current workspace-first CLI to a project-first interface model without making user-facing promises ahead of implementation.

Current truth: workspace remains implemented CLI.

## Canonical Direction

- Canonical model: `Org -> Project -> Branch -> Version -> Environment`.
- Future command tree target: `nexus org`, `nexus project`, `nexus branch`, `nexus version`, `nexus env`, `nexus deploy`.
- This plan is internal design guidance; it is not a release commitment.

## Non-goals For This Stage

- non-goal: immediate package or type renames from workspace terms to project/branch terms.
- non-goal: immediate introduction of `nexus project` or `nexus branch` commands in code.
- non-goal: compatibility aliases promised in user docs before implementation exists.

This stage does not require a broad code rename and does not require command-surface changes to ship immediately.

## Migration Principles

- Keep user docs and command references truthful while workspace commands are the implemented interface.
- Introduce the future command tree in internal planning first, then code, then user docs.
- Preserve behavior and operational safety while changing interface nouns.
- Enforce one active session per branch in future branch/session surfaces.
- Parallel development is based on forked branches.

## Staged Phases

### Phase 1: Internal command tree design

- Define noun boundaries, shared flags, and context resolution strategy.
- Specify how future command tree maps to current workspace operations.
- Keep all user-facing references workspace-first until code exists.

### Phase 2: Shared context and flag groundwork

- Add internal context resolution for org/project/branch/version/env identifiers.
- Avoid breaking existing `workspace` command behavior.
- Prepare command wiring in `root.go` without activating unimplemented groups.

### Phase 3: Internal data model shims

- Add adapter layer translating branch/session semantics to current workspace structs/state.
- Keep persisted state compatibility during transition.
- Add telemetry dual-tagging if needed (workspace + canonical noun labels).

### Phase 4: Command introduction sequence

- Introduce command groups behind staged rollout controls.
- Implement branch dev-session flows before de-emphasizing `workspace` commands.
- Update user docs only when commands are shipping and test-covered.

### Phase 5: Workspace concept deprecation

- Remove `workspace` as a user-facing concept after migration parity.
- Keep compatibility behavior only if explicitly implemented and tested.
- Finalize canonical command tree as default interface.

## Likely Migration Touchpoints

- `packages/nexusd/internal/cli/root.go` (future command tree wiring)
- `packages/nexusd/internal/cli/workspace.go` (legacy command bridge/deprecation path)
- `packages/nexusd/internal/config/config.go` (context defaults and migration-safe keys)
- `packages/nexusd/internal/types/types.go` (canonical noun adapters and future type surfaces)
- `packages/nexusd/internal/state/store.go` (state compatibility and staged naming evolution)
- `packages/nexusd/pkg/server/server.go` (API payloads and route semantics)

## Risks and Controls

- Risk: docs or CLI claim project-first behavior before it ships.
  - Control: internal-doc-only planning language; user docs stay implementation-truthful.
- Risk: state/API incompatibility during naming transition.
  - Control: staged adapters, compatibility reads, explicit migration tests.
- Risk: parallel-session semantics drift.
  - Control: enforce one active session per branch and direct parallelism to forked branches.
