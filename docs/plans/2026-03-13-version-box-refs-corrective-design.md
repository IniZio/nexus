# Version-Box Refs Corrective Design

## Problem

The prior interface migration treated `environment` as a direct rename of `workspace`, creating an incorrect ownership model.

Correct model:

- Every **Version** owns exactly one **Box** (runtime + filesystem state container).
- **Branch** and **Environment** are refs that point to a `version_id`.
- Branch/Environment may appear to "have" a box only because they resolve through their version.

## Corrected Conceptual Model

### Entities

- **Version** (owner): immutable identity and lifecycle state for box-backed content.
- **Box** (implementation substrate): current workspace/container mechanics used by the runtime.
- **BranchRef**: mutable pointer to a `version_id` for development flow.
- **EnvironmentRef**: mutable pointer to a `version_id` for runtime/deploy context.

### Ownership and refs

`Version -> Box` is ownership.

`BranchRef -> Version` and `EnvironmentRef -> Version` are pointer relationships only.

## Version Lifecycle

### 1) scratch (writable)

- Active writing state for developers/agents.
- Backed by a writable box.
- Exactly where file edits happen.

### 2) draft (sealed snapshot)

- Captured from scratch at snapshot/checkpoint time.
- Durable and addressable.
- Not modified in place.

### 3) immutable (published)

- Promoted from draft.
- Reproducible and read-only by policy.
- Branch/environment refs can point here.

Edits after draft/immutable must create/restore a new scratch flow, not mutate in place.

## Snapshot / Restore Semantics

- **snapshot**: create new version record from current scratch box state.
- **restore**: materialize selected version into a writable scratch box (new or reset), then move selected ref (`branch` or `environment`) to that version.
- No in-place mutation of immutable versions.

## Scope Decision

To simplify delivery, defer user-facing trace/version-history feature for now.

- Remove user-facing trace surfaces from CLI/docs.
- Keep telemetry internals as implementation detail for future work.

## Migration Strategy

### Phase 1: Truth reset

- Restore docs and CLI help truthfully to shipped behavior where needed.
- Remove misleading environment/workspace one-to-one conceptual claims.

### Phase 2: Metadata layer

- Add lightweight `version` and ref metadata over existing box/workspace substrate.
- Keep daemon/store/proto internals stable.

### Phase 3: Lifecycle behavior

- Implement scratch -> draft -> immutable rules.
- Implement snapshot/restore semantics based on version ownership.

### Phase 4: CLI promotion

- Expose stable user-facing commands for refs and version lifecycle.
- Promote docs/examples only when behavior is actually implemented.

## Verification Gates

- CLI help reflects only implemented commands.
- Snapshot/restore round-trip tests prove writable scratch and immutable safety.
- Ref movement tests prove branch/environment point to version IDs, not boxes directly.
- User-facing docs avoid unimplemented command claims.

## Open Questions

1. Should Environment refs default to immutable versions only, or allow draft pointers?
2. On snapshot, should Branch remain on current scratch or move to a new scratch derived from snapshot?
3. Should restore always allocate a new writable box, or allow reset-in-place for scratch only?
4. What minimal naming scheme for `version_id` is preferred (generated vs user-specified alias)?
