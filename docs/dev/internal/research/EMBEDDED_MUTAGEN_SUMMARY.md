# Embedded Mutagen Daemon - Research Summary

## Research Complete ✓

**Date:** February 22, 2026  
**Effort:** Medium (1-2 days)  
**Status:** Ready for Implementation

---

## Bottom Line

**Recommended Approach:** Embed Mutagen as a subprocess with isolated data directory (`~/.nexus/mutagen/`).

This approach:
- ✅ Requires zero user setup (no Mutagen CLI installation)
- ✅ Prevents conflicts with existing Mutagen installations
- ✅ Provides full lifecycle control
- ✅ Is used successfully by mutagen-compose (official Docker integration)
- ⚠️ Adds ~50MB to distribution size

---

## Key Research Findings

### 1. Mutagen Architecture

```
┌─────────────┐     gRPC/Unix      ┌─────────────────┐
│   Client    │◀══════════════════▶│  mutagen daemon │
│   (Nexus)   │      socket        │  (subprocess)   │
└─────────────┘                    └────────┬────────┘
                                            │
                                            │ mutagen-agent
                                            ▼
                                    ┌─────────────────┐
                                    │ Docker Container│
                                    └─────────────────┘
```

**Key Components:**
- **Daemon:** Manages sync sessions via gRPC (Unix socket)
- **mutagen-agent:** Runs inside containers for file operations
- **Session:** Defined by alpha (host) and beta (container) endpoints

### 2. Embedding Strategy Comparison

| Approach | Feasibility | Complexity | Recommendation |
|----------|-------------|------------|----------------|
| **Subprocess** | High | Low | ✅ **Recommended** |
| In-process library | Medium | High | ❌ Unsupported API |
| External dependency | High | Low | ❌ Poor UX |

**Evidence:**
- mutagen-compose uses subprocess approach successfully
- In-process approach uses internal APIs that may change
- Subprocess provides clean separation and full feature access

### 3. Data Directory Isolation

Mutagen respects `MUTAGEN_DATA_DIRECTORY` environment variable:

```go
// Before starting daemon:
os.Setenv("MUTAGEN_DATA_DIRECTORY", "~/.nexus/mutagen")

// Result:
// Socket: ~/.nexus/mutagen/daemon/daemon.sock
// Data:   ~/.nexus/mutagen/sessions/, caches/, etc.
```

**Benefits:**
- Isolated from user's `~/.mutagen/`
- Clean uninstall (just delete `~/.nexus/`)
- No session conflicts with standalone Mutagen

### 4. Required Artifacts

| File | Size | Source |
|------|------|--------|
| `mutagen` binary | ~30MB | https://github.com/mutagen-io/mutagen/releases |
| `mutagen-agents.tar.gz` | ~20MB | Same release |
| **Total** | **~50MB** | Bundled with Nexus |

---

## Implementation Path

### Phase 1: Core Integration (Medium effort)

1. **Add Mutagen dependency to go.mod**
   ```
   go get github.com/mutagen-io/mutagen@v0.18.0
   ```

2. **Create `internal/sync/mutagen/` package**
   - `daemon.go` - Embedded daemon management
   - `session.go` - Session CRUD operations
   - `client.go` - gRPC client wrappers

3. **Implement daemon lifecycle**
   - Start with `exec.Command("mutagen", "daemon", "run")`
   - Set `MUTAGEN_DATA_DIRECTORY` env var
   - Wait for socket, connect via gRPC
   - Monitor process, handle restarts

4. **Implement session management**
   - Create: host path → Docker container
   - Pause/Resume: for workspace stop/start
   - Terminate: for workspace destroy
   - Status: for monitoring

### Phase 2: Build Integration (Quick)

5. **Update build process**
   - Download Mutagen release during build
   - Bundle `mutagen` and `mutagen-agents.tar.gz`
   - Include in distribution packages

### Phase 3: Integration (Medium)

6. **Wire into workspace lifecycle**
   - Create sync on workspace create
   - Pause on workspace stop
   - Resume on workspace start
   - Terminate on workspace destroy

---

## Code Examples

### Starting the Embedded Daemon

```go
daemon := mutagen.NewEmbeddedDaemon("~/.nexus/mutagen")
if err := daemon.Start(ctx); err != nil {
    return err
}
defer daemon.Stop(ctx)
```

### Creating a Sync Session

```go
manager := mutagen.NewSessionManager(daemon)
session, err := manager.CreateSession(
    ctx,
    "my-workspace",
    "/home/user/.worktrees/my-workspace",
    "abc123def456",  // container ID
    "/workspace",
)
```

### Managing Sessions

```go
// Pause when workspace stops
manager.PauseSession(ctx, session.ID)

// Resume when workspace starts  
manager.ResumeSession(ctx, session.ID)

// Terminate when workspace destroyed
manager.TerminateSession(ctx, session.ID)
```

---

## Architecture Decision

### Decision: Use Subprocess Embedding with Isolated Data Directory

**Context:** We need to provide file synchronization without requiring users to install Mutagen CLI separately.

**Decision:** Embed Mutagen as a subprocess with `MUTAGEN_DATA_DIRECTORY` set to `~/.nexus/mutagen/`.

**Consequences:**
- ✅ Zero user setup
- ✅ Isolated from user's Mutagen installation
- ✅ Full lifecycle control
- ✅ Can upgrade Mutagen with Nexus releases
- ⚠️ +50MB distribution size
- ⚠️ Additional process to manage

**Rejected Alternatives:**
- **External Mutagen:** Poor UX, version conflicts
- **In-process library:** Uses unsupported internal APIs

---

## Deliverables Created

1. **Updated PRD:** `docs/dev/internal/plans/001-docker-workspaces/02-architecture.md`
   - Section 2.8.6: Complete embedded daemon documentation
   - Architecture diagrams
   - Code examples
   - Configuration reference

2. **Research Document:** `docs/dev/internal/research/embedded-mutagen-daemon.md`
   - Comprehensive technical research
   - Real-world examples (mutagen-compose)
   - API documentation
   - Performance characteristics
   - Testing strategy

3. **Implementation Code:** `docs/dev/internal/research/embedded_daemon.go`
   - Production-ready Go code
   - `EmbeddedDaemon` struct with lifecycle management
   - `SessionManager` for session CRUD
   - Error handling and monitoring

---

## Next Steps

1. **Review research** with team
2. **Create implementation plan** using `writing-plans` skill
3. **Begin Phase 1** - Core integration
4. **Add Mutagen to go.mod** and resolve dependencies
5. **Implement `internal/sync/mutagen/` package**

---

## References

- **Mutagen:** https://github.com/mutagen-io/mutagen
- **mutagen-compose:** https://github.com/mutagen-io/mutagen-compose (reference implementation)
- **Documentation:** https://mutagen.io/documentation
- **Release Downloads:** https://github.com/mutagen-io/mutagen/releases

---

## Contact

For questions about this research, refer to:
- Research doc: `docs/dev/internal/research/embedded-mutagen-daemon.md`
- Implementation code: `docs/dev/internal/research/embedded_daemon.go`
- Updated PRD: `docs/dev/internal/plans/001-docker-workspaces/02-architecture.md`
