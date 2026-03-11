# ADR-004: Product Interface Canonical Nouns

**Status:** Accepted

## Context

Nexus currently ships a workspace-first CLI (`nexus workspace ...`) and workspace-first server/state internals. Product direction has approved a canonical user model that is clearer for long-term collaboration and deployment semantics.

This ADR records internal product-interface decisions only. It does not change what is currently implemented in code.

## Decision

The canonical model is:

`Org -> Project -> Branch -> Version -> Environment`

With deployment semantics:

- `nexus deploy` binds a specific version to a specific environment.
- Versions are immutable and deploy-only.

Canonical top-level command groups (future-facing, not yet implemented):

- `nexus org`
- `nexus project`
- `nexus branch`
- `nexus version`
- `nexus env`
- `nexus deploy`

## Invariants

- Box is internal and must never be documented as a user-facing primitive.
- All development sessions are remote host runtime sessions, even when the host is local hardware.
- One active dev session per branch.
- Parallel development follows fork-first branch strategy; no multiple active sessions on one branch.
- Versions are immutable and deploy-only artifacts.

## Scope and Truthfulness

- Current implemented CLI remains workspace-first.
- This ADR defines canonical nouns and constraints for migration planning.
- This ADR does not promise immediate user-facing command availability.

## Consequences

- Internal design and migration docs should map workspace-era internals onto the canonical model.
- User-facing docs stay truthful to implemented workspace commands until the new command tree is shipped.
- Migration work should preserve behavior while changing terminology and boundaries in staged increments.

## Related

- `docs/plans/2026-03-11-product-interface-design.md`
- `docs/dev/design/product-interface-mapping.md`
- `docs/dev/plans/008-project-first-cli-transition.md`
