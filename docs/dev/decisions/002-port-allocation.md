# ADR-002: Port allocation

**Status:** Accepted

## Context

Workspaces expose several services; two workspaces must not bind the same host ports.

## Decision

Allocate host ports dynamically per workspace with conflict detection; expose services through `nexus tunnel` and compose-driven forwarding (see CLI reference).

## Consequences

**Pros:** No manual port picking; fewer collisions across workspaces.

**Cons:** Host ports may change across restarts; use `nexus list` / tunnel output to see current bindings.

## Related

- [ADR-001: Git worktree isolation](001-worktree-isolation.md)
