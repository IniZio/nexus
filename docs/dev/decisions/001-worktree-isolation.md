# ADR-001: Git worktree isolation

**Status:** Accepted

## Context

Multiple workspaces on the same branch can conflict when editing the same files.

## Decision

Isolate work per workspace with git worktrees: branch `nexus/<workspace-name>`, worktree under `.nexus/worktrees/<workspace-name>/`, separate runtime from other workspaces.

## Consequences

**Pros:** No cross-workspace branch/file conflicts; branch names document intent.

**Cons:** Extra disk per worktree; requires git with worktree support (2.5+).

## Related

- [ADR-002: Port allocation](002-port-allocation.md)
