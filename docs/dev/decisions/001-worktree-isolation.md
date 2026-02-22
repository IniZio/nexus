# ADR-001: Git Worktree Isolation

**Status:** Accepted

## Context

Without proper git isolation, multiple workspaces share the same branch and can conflict when editing the same files. This makes it impossible to work on multiple features simultaneously.

## Decision

Use git worktrees to create isolated branches for each workspace, mounted to separate containers.

## Details

### Workspace = Git Branch
- Each workspace creates a branch `nexus/<workspace-name>`
- Worktree mounted at `.worktree/<workspace-name>/`
- No code conflicts between workspaces

### Commands
```bash
# Create workspace with branch
nexus workspace create feature-auth
# → Creates branch nexus/feature-auth
# → Creates worktree at .worktree/feature-auth/

# Switch between workspaces
git checkout nexus/feature-auth
git checkout nexus/feature-payment
```

### Isolation Guarantees
- Different git branches → No branch conflicts
- Different directories → No file conflicts
- Different containers → No process conflicts
- Different ports → No service conflicts

## Consequences

### Benefits
- Workspaces are truly isolated
- Can work on multiple features simultaneously
- Branch names self-document workspace purpose

### Trade-offs
- More disk space (one worktree per workspace)
- Requires git 2.5+ for worktree support

## Implementation

```go
// internal/workspace/manager.go
func (m *Manager) Create(name, template string) error {
    // 1. Create git worktree
    worktreePath := filepath.Join(".worktree", name)
    if err := m.git.Worktree(name, worktreePath); err != nil {
        return err
    }

    // 2. Create container with mounted worktree
    if err := m.docker.Create(name, worktreePath); err != nil {
        return err
    }

    return nil
}
```

## Related
- [ADR-002: Port Allocation](002-port-allocation.md)
- [ADR-003: Telemetry Design](003-telemetry-design.md)
