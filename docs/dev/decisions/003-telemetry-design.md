# ADR-003: Telemetry Design

**Status:** Accepted

## Context

Need to collect usage analytics to improve the system while respecting user privacy.

## Decision

Local-first telemetry with optional external sync. All data stays on user's machine unless explicitly exported.

## Details

### Privacy-First Principles
1. **No Cloud by Default** - Everything stays on user's machine
2. **Explicit Sync** - User must run `nexus sync` to send data
3. **Anonymized IDs** - Workspace/task names hashed
4. **No Code Content** - Never collect source code or file contents
5. **User Controls** - Can purge, export, or disable anytime

### Data Model

**Events Table:**
```sql
CREATE TABLE events (
    id INTEGER PRIMARY KEY,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    session_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    workspace_hash TEXT,
    task_hash TEXT,
    command TEXT,
    duration_ms INTEGER,
    success BOOLEAN,
    error_category TEXT,
    template_used TEXT,
    services_count INTEGER,
    ports_used INTEGER
);
```

**Sessions Table:**
```sql
CREATE TABLE sessions (
    id INTEGER PRIMARY KEY,
    session_id TEXT UNIQUE NOT NULL,
    started_at DATETIME,
    ended_at DATETIME,
    duration_ms INTEGER,
    commands_executed INTEGER,
    workspaces_created INTEGER,
    tasks_completed INTEGER,
    errors_encountered INTEGER
);
```

### CLI Commands

```bash
# Telemetry control
nexus telemetry status
nexus telemetry on
nexus telemetry off
nexus telemetry purge

# Local analytics
nexus stats
nexus stats --week
nexus stats --month

# Insights
nexus insights
nexus insights --slow
nexus insights --errors
```

### Dashboard

```
╔══════════════════════════════════════════════════════════╗
║  Nexus Usage Analytics (Last 30 Days)                   ║
╠══════════════════════════════════════════════════════════╣
║  Workspaces Created:    12                               ║
║  Tasks Completed:       47                              ║
║  Avg Session Length:    2h 15m                           ║
║  Success Rate:          94%                              ║
╚══════════════════════════════════════════════════════════╝
```

## Consequences

### Benefits
- Privacy-respecting by design
- Helps improve system based on real usage
- Users own their data

### Trade-offs
- Less data for developers
- Requires user action to share

## Implementation

```go
// pkg/telemetry/collector.go
type Collector struct {
    db *sql.DB
}

func (c *Collector) Record(event Event) error {
    _, err := c.db.Exec(`
        INSERT INTO events (session_id, event_type, command, duration_ms, success)
        VALUES (?, ?, ?, ?, ?)
    `, event.SessionID, event.Type, event.Command, event.Duration, event.Success)
    return err
}
```

## Related
- [ADR-001: Worktree Isolation](001-worktree-isolation.md)
- [ADR-002: Port Allocation](002-port-allocation.md)
