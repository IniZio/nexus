# SDK/CLI Ergonomics Phased Plan (Nexus)

Date: 2026-04-07  
Status: Approved execution plan (starting Phase 1)

Related research:

- `docs/dev/internal/research/2026-04-07-sdk-cli-ergonomics-benchmark.md`

## Goal

Deliver an SDK/CLI surface that is semantically coherent and highly ergonomic for both:

- Opencode Nexus plugin (automation, repeatability, multi-workspace safety)
- Embedded management UI (interactive lifecycle management)

## Phase 1 (Now): Semantics and SDK correctness baseline

### Scope

1. SDK type hygiene and API consistency

- Remove duplicated type declarations in `packages/sdk/js/src/types.ts`.
- Ensure default workspace scoping behavior is explicit and consistent across:
  - `client.fs`
  - `client.exec`
  - `client.spotlight`

2. Documentation clarity

- Add an explicit scoping/capability matrix to `docs/reference/sdk.md`.
- Ensure docs map exactly to real method behavior.

3. Verification

- SDK build and tests must pass.

### Acceptance criteria

- `types.ts` contains one authoritative definition per SDK interface.
- `client.fs` and `client.exec` include default `workspaceId` in emitted requests.
- `client.spotlight` defaults to configured workspace and supports explicit override.
- `workspaceHandle.*` remains explicitly scoped and unchanged semantically.
- `docs/reference/sdk.md` includes a matrix of scope behavior by operation group.
- `npm run build` and `npm test -- --runInBand` pass in `packages/sdk/js`.

## Phase 2: CLI taxonomy and intent grouping

### Scope

- Refactor CLI docs structure in `docs/reference/cli.md` to intent buckets:
  - auth/session
  - workspace lifecycle
  - execution and filesystem
  - forwarding/network
  - diagnostics
- Align command names/verbs with SDK nomenclature where practical.

### Acceptance criteria

- CLI reference sections map to user intent rather than implementation internals.
- Verb set is consistent with SDK lifecycle terms.
- At least one end-to-end flow in docs demonstrates CLI <-> SDK conceptual parity.

## Phase 3: Plugin/UI parity hardening

### Scope

- Verify each embedded UI action has exact SDK-equivalent operation and backend endpoint semantic parity.
- Add a small parity table documenting UI action -> SDK method -> daemon endpoint.

### Acceptance criteria

- No UI-only lifecycle semantics.
- Parity table exists and is verified against code.

## Phase 4: ADR formalization

### Scope

- Convert stabilized decisions to ADR(s):
  - default workspace context + explicit cross-workspace override
  - canonical lifecycle verb set
  - scoped-handle model for multi-workspace programming

### Acceptance criteria

- ADR files created in `docs/dev/decisions/` and linked from dev docs.
