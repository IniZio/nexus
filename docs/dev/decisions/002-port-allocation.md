# ADR-002: Port Allocation

**Status:** Accepted

## Context

Each workspace runs multiple services (web, api, database) that need accessible ports on the host. Port conflicts occur when two workspaces try to use the same port.

## Decision

Use dynamic port allocation with predictable mapping patterns and conflict detection.

## Details

### Dynamic Allocation
- Each workspace gets a random SSH port on first creation
- Services map to sequential ports starting from base (3000, 5000, 5432)

### Port Mapping Example
```
feature-auth (SSH: 32777):
  web:    3000 → 32778
  api:    5000 → 32779
  postgres: 5432 → 32780

feature-payment (SSH: 32781):
  web:    3000 → 32782
  api:    5000 → 32783
  postgres: 5432 → 32784
```

### Commands
```bash
# Check ports for a workspace
nexus workspace ports feature-auth

# List all workspaces with ports
nexus workspace list
```

### Conflict Resolution
1. Try primary port
2. If in use, try next available
3. Increment until free port found
4. Cache allocation to avoid reuse

## Consequences

### Benefits
- No manual port management
- Workspaces never conflict
- Easy to see which ports are in use

### Trade-offs
- Ports change between restarts (if SSH port changes)
- Need to check which ports are active

## Implementation

```go
// internal/docker/provider.go
func (p *Provider) AllocatePort(service string) (int, error) {
    basePorts := map[string]int{
        "web":       3000,
        "api":       5000,
        "postgres":  5432,
    }

    base := basePorts[service]
    for port := base; port < 65535; port++ {
        if !p.IsPortInUse(port) {
            return port, nil
        }
    }
    return 0, errors.New("no available ports")
}
```

## Related
- [ADR-001: Worktree Isolation](001-worktree-isolation.md)
- [How-To: Debug Ports](../how-to/debug-ports.md)
