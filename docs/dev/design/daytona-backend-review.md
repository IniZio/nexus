# Design Review: Daytona Backend Integration

**Date:** 2026-02-24  
**Status:** Approved with Modifications  
**Scope:** MVP Implementation Only

---

## Executive Summary

The proposed design is **architecturally sound** for an MVP. The SSH-only abstraction correctly leverages Nexus's existing infrastructure (Mutagen, worktrees, SSH execution) without requiring new code paths. This is the right call for a first release.

**Recommendation:** Proceed with the simplified SSH-only design, with the modifications noted below.

---

## Architecture Review

### Core Principle: SSH-Only Abstraction

**Verdict: APPROVED** ✅

SSH as the universal interface is the correct choice for MVP. Nexus already has:
- `ExecViaSSH()` implementation in Docker backend
- `Shell()` using SSH for interactive sessions
- Mutagen configured for SSH-based sync
- Port allocation/mapping logic

**Why this works:**
- Daytona's SSH gateway becomes a drop-in replacement for Docker's port-mapped SSH
- Zero changes needed to Mutagen, exec, or shell functionality
- Same user experience regardless of backend

**Trade-off accepted:** File API performance benefits are deferred. SSH/SCP throughput is sufficient for most development workflows (confirmed by Docker backend usage patterns).

---

## Answers to Design Questions

### 1. Is SSH-only the right abstraction?

**Answer: Yes, for MVP.**

**Pros validated:**
- Simple, proven with Docker backend
- Works with existing Mutagen sync (tested)
- No new dependencies or protocols

**Cons accepted:**
- Missing File API performance (2-5x slower for bulk operations)
- No native directory listings

**Escalation trigger:** If user feedback shows sync performance issues with large repositories (>10k files), revisit File API integration in v2.

---

### 2. API key from .env - correct approach?

**Answer: Partially correct, needs modification.**

**Current proposal:**
```yaml
backends:
  daytona:
    enabled: true
    api_url: "https://app.daytona.io/api"
```

```bash
# .env
DAYTONA_API_KEY=sk_day_xxx
```

**Issues identified:**

1. **Security:** `.env` files are often committed accidentally. Use standard credential storage instead.
2. **No per-project config:** Some teams may need different Daytona accounts per project.
3. **Missing validation:** No clear error handling path.

**Recommended approach:**

```yaml
# ~/.nexus/config.yaml
backends:
  daytona:
    enabled: true
    api_url: "https://app.daytona.io/api"
    # API key loaded from environment or keychain
    # Priority: 1) env var 2) system keychain 3) error
```

**Resolution order:**
1. `DAYTONA_API_KEY` environment variable (CI/automation)
2. System keychain/credential store (desktop users)
3. Clear error: "Daytona API key not found. Set DAYTONA_API_KEY or run 'nexus config set-credentials daytona'"

**Error handling:**
```go
// On startup, verify credentials exist
if err := backend.ValidateCredentials(); err != nil {
    return fmt.Errorf("daytona backend: %w. Set DAYTONA_API_KEY or run 'nexus login daytona'", err)
}
```

---

### 3. Mutagen over SSH for Daytona

**Answer: Works, but test these scenarios.**

**Known working (Docker backend):**
- Local ↔ Container sync over SSH port mapping
- Two-way safe mode
- Conflict resolution

**Daytona-specific concerns:**

| Concern | Risk Level | Mitigation |
|---------|-----------|------------|
| SSH gateway latency | Low | Test with `ssh -v` to measure handshake time |
| Connection limits | Medium | Daytona may limit concurrent SSH connections |
| Gateway timeouts | Medium | May need keepalive config in `~/.ssh/config` |
| Sandbox restart = new host key | High | Must configure `StrictHostKeyChecking=no` |

**Required testing:**
```bash
# Test SSH connectivity
nexus workspace create test --backend=daytona
time ssh daytona-gateway...  # Check latency

# Test Mutagen sync with large repo
nexus sync start test
# Monitor for errors, conflicts, or timeouts
```

**Configuration needed:**
Add to Daytona backend initialization:
```go
sshConfig := &SSHConfig{
    StrictHostKeyChecking: false,  // Sandbox ephemeral, keys rotate
    ServerAliveInterval:   30,     // Prevent gateway timeouts
    ConnectTimeout:        10,
}
```

---

### 4. Workspace lifecycle - auto-stop handling

**Answer: Expose to user, don't hide it.**

**Daytona behavior:**
- Sandboxes auto-stop after 15 min idle (configurable)
- Restart is fast but not instant (~5-10 seconds)

**Design decision:**

Don't try to hide auto-stop. Instead:

1. **Detect stopped state** in `GetStatus()` 
2. **Auto-restart on next command** with user notification
3. **Show TTL warning** when workspace is active

**Implementation:**

```go
func (b *DaytonaBackend) ExecViaSSH(ctx context.Context, id string, cmd []string) (string, error) {
    // Check if stopped
    status, _ := b.GetStatus(ctx, id)
    if status == types.StatusStopped {
        fmt.Fprintf(os.Stderr, "Workspace %s is sleeping. Starting...\n", id)
        if err := b.StartWorkspace(ctx, id); err != nil {
            return "", fmt.Errorf("auto-start failed: %w", err)
        }
        fmt.Fprintf(os.Stderr, "Workspace started. Executing command...\n")
    }
    // ... proceed with SSH exec
}
```

**CLI display:**
```bash
$ nexus workspace list
NAME        STATUS    BACKEND    TTL
my-feature  running   daytona    12m remaining
other-ws    sleeping  daytona    -
```

**Why not manage TTL directly:**
- Adds complexity (need background keepalive)
- Conflicts with Daytona's cost optimization
- Users can configure Daytona-side TTL if needed

---

### 5. Git workflow - conflicts or issues?

**Answer: One major issue to address.**

**Proposed flow:**
```
Local Worktree ←[Mutagen]→ Daytona Sandbox
                     ↕
              [Daytona clones repo]
```

**The problem:** Race condition on initial sync.

Daytona clones the repo into `/workspace` during sandbox creation. Mutagen then tries to sync local worktree → sandbox. This creates conflicts:
- Daytona has `.git/` from its clone
- Local worktree has `.git/` pointing elsewhere
- Both have different file states

**Solutions considered:**

| Approach | Complexity | Risk |
|----------|-----------|------|
| A. Exclude `.git/` from sync | Low | Git operations fail in sandbox |
| B. Skip Daytona clone, use empty sandbox + manual setup | Medium | More setup steps |
| C. Sync only working tree, not `.git/` | Medium | Requires git remote config |
| **D. Daytona with `initScript` to skip clone** | Low | **Recommended** |

**Recommended solution (D):**

Configure Daytona sandbox without auto-clone:

```go
req := &daytona.CreateSandboxRequest{
    Repository: nil,  // Don't auto-clone
    InitScript: `#!/bin/sh
        mkdir -p /workspace
        # Don't clone - Mutagen will populate
    `,
}
```

**Mutagen sync configuration:**
```yaml
# Exclude .git to avoid conflicts
exclude:
  - ".git/"
  - "node_modules/"
```

**Git operations in sandbox:**
- User can run `git init` or manually clone if needed
- Or use host-side git for commits/pushes
- Document that Daytona sandbox is for execution, not source control

---

### 6. What's missing for MVP?

**Essential (must have):**

1. ✅ Backend interface implementation
2. ✅ SSH connection management
3. ✅ Configuration loading
4. ⚠️ Error handling for missing credentials
5. ⚠️ Auto-restart on exec
6. ⚠️ Status polling with TTL display

**Defer to later:**

1. **File API integration** - SSH/SCP is sufficient
2. **Port forwarding** - Can use Daytona's built-in preview URLs
3. **Custom images** - Use Daytona defaults for MVP
4. **Multi-region** - Single region is fine
5. **Prebuilds** - Standard sandbox startup is fast enough
6. **Volume persistence** - Accept ephemeral for MVP

**Red flag items:**

- ❌ Checkpoint/restore - Daytona may not support container commit
- ❌ Resource limits - Daytona manages these, may conflict with Nexus config
- ❌ Custom DNS - May need investigation

---

## Critical Issues to Address

### Issue 1: Backend Type Enumeration

**Current:** `types.BackendType` has Docker, Sprite, Kubernetes. Daytona needs to be added.

```go
// internal/types/types.go
const (
    BackendDocker BackendType = iota
    BackendSprite
    BackendKubernetes
    BackendDaytona  // Add this
)
```

### Issue 2: Workspace Storage

**Problem:** Daytona workspace IDs are different from Nexus workspace IDs.

**Solution:** Map Nexus ID → Daytona ID internally.

```go
type DaytonaBackend struct {
    apiClient *daytona.Client
    // Map Nexus workspace ID to Daytona sandbox ID
    idMapping map[string]string  // Persist to disk
}
```

### Issue 3: Port Allocation

**Problem:** Docker backend allocates local ports (32800-34999). Daytona doesn't need this.

**Solution:** Daytona's SSH gateway uses standard port 22. Port allocation returns 0 (no-op).

```go
func (b *DaytonaBackend) AllocatePort() (int32, error) {
    return 0, nil  // Daytona uses gateway port 22
}
```

### Issue 4: Sync Initialization Race

**Problem:** Mutagen needs SSH host:port, but Daytona gateway info comes from API.

**Solution:** 
1. Create sandbox
2. Poll for "running" status  
3. Get SSH connection info from API
4. Start Mutagen sync
5. Return workspace to user

---

## Suggested Implementation Order

**Phase 1: Foundation (2-3 days)**
1. Add `BackendDaytona` to types
2. Create `internal/daytona/` package structure
3. Implement Daytona API client wrapper
4. Add configuration loading

**Phase 2: Core Backend (3-4 days)**
5. Implement `CreateWorkspace()` - sandbox creation
6. Implement `GetStatus()` with polling
7. Implement `GetSSHConnection()` - gateway info
8. Implement `StartWorkspace()` / `StopWorkspace()`

**Phase 3: SSH Integration (2-3 days)**
9. Implement `ExecViaSSH()` with auto-restart
10. Implement `Shell()` via SSH gateway
11. Test Mutagen sync over SSH
12. Handle `.git/` sync exclusion

**Phase 4: Polish (2 days)**
13. Error messages for missing credentials
14. TTL display in workspace list
15. Documentation
16. Integration tests

**Total effort:** ~9-12 days (Medium)

---

## Red Flags and Blockers

### 🚩 Blocker 1: Daytona API Surface

**Risk:** If Daytona's API doesn't expose SSH gateway host/port programmatically, the design fails.

**Mitigation:** Verify API capability before implementation:
```bash
# Test with curl
curl -H "Authorization: Bearer $DAYTONA_API_KEY" \
  https://app.daytona.io/api/sandboxes/xxx \
  | jq '.sshConnection'
```

**Fallback:** Use Daytona CLI if API is insufficient (adds dependency).

### 🚩 Blocker 2: Mutagen with Gateway SSH

**Risk:** Mutagen requires stable SSH connection. Gateway timeouts may break sync.

**Mitigation:** Test early with real Daytona sandbox:
```bash
# Manual test
mutagen sync create \
  --name=test \
  ./local-folder \
  user@daytona-gateway.example.com:/workspace
```

**Fallback:** Use Daytona File API for sync if SSH is unreliable (requires new code path).

### 🚩 Blocker 3: Sandbox Initialization Time

**Risk:** If Daytona sandbox creation takes >30 seconds, UX suffers.

**Mitigation:** Implement async creation with progress updates.

**Acceptance criteria:**
- Cold start: < 60 seconds
- Warm start (restart): < 15 seconds

---

## Recommended Design Modifications

### 1. Credential Handling

Change from:
```bash
# .env file (risky)
DAYTONA_API_KEY=xxx
```

To:
```bash
# Environment or keychain
export DAYTONA_API_KEY=xxx
# OR
nexus login daytona  # Interactive prompt, stores in keychain
```

### 2. Backend Selection CLI

Current proposal:
```bash
nexus workspace create my-feature --backend=daytona
```

Add default backend config:
```yaml
# ~/.nexus/config.yaml
workspace:
  default_backend: "daytona"  # or "docker"
```

### 3. Sync Exclusions

Add to Daytona backend:
```go
var defaultExclusions = []string{
    ".git/",
    ".nexus/",
    "node_modules/",
    "*.log",
}
```

### 4. Status Display

Add TTL to workspace list:
```bash
$ nexus workspace list
NAME        STATUS    BACKEND    TTL
my-feature  running   daytona    8m remaining
docker-ws   running   docker     -
```

---

## Architecture Comparison: Docker vs Daytona

| Feature | Docker Backend | Daytona Backend | Notes |
|---------|---------------|-----------------|-------|
| **Creation** | Local container | API call to Daytona | Daytona needs network |
| **SSH Access** | localhost:PORT | gateway.daytona.io:22 | Different connection string |
| **Lifecycle** | Full control | Limited (auto-stop) | Accept for MVP |
| **Sync** | Mutagen over SSH | Mutagen over SSH | Same code path |
| **Exec** | SSH to localhost | SSH to gateway | Same code path |
| **Ports** | Allocated locally | Daytona preview URLs | Different approach |
| **Storage** | Persistent volumes | Ephemeral | Accept for MVP |
| **Cost** | Local resources | Cloud billing | User concern |

---

## Conclusion

The simplified SSH-only design is **approved for MVP** with the modifications outlined above. The architecture correctly leverages existing Nexus infrastructure and minimizes new code paths.

**Next steps:**
1. Verify Daytona API capabilities (blocker check)
2. Update `types.BackendType` with Daytona
3. Create `internal/daytona/` package
4. Implement Phase 1 (Foundation)

**Success criteria:**
- User can create Daytona workspace with same UX as Docker
- Mutagen sync works over SSH gateway
- Auto-restart on command execution
- Clear error messages for credential issues

---

## Appendix: Daytona API Research Needed

Before implementation, verify:

1. **Authentication:** API key format, header name
2. **Sandbox creation:** Required fields, repository handling
3. **SSH info:** How to get host/port/credentials for a sandbox
4. **Lifecycle:** Start/stop/delete endpoints
5. **Rate limits:** Requests per minute/hour
6. **Pricing:** Cost per sandbox, data transfer

Document findings in `docs/dev/research/daytona-api.md`.
