# Bidirectional File Sync Research

## Context
- Local host: git worktree at `.nexus/worktrees/<name>/`
- Remote daemon runs workspace containers
- Need bidirectional sync (host ↔ container)
- Must handle: code edits, git operations, build artifacts, node_modules

---

## Comparison Table

| Aspect | Mutagen | Unison | rsync + lsyncd | SSHFS/FUSE | WebSocket Sync |
|--------|---------|--------|----------------|------------|---------------|
| **Bidirectional** | ✅ Native | ✅ Native | ❌ One-way only | ✅ Native | ✅ Custom |
| **Real-time** | ✅ Watch-based | ✅ With watch | ✅ With lsyncd | ✅ Always | ✅ Custom |
| **Conflict Resolution** | ✅ 4 modes | ✅ Interactive | ❌ None | N/A | ❌ Manual |
| **Performance** | Excellent (rsync algo) | Good | Excellent | Poor | Good |
| **Setup Complexity** | Low | Medium | Medium | Low | High |
| **No Remote Install** | ✅ Agent injection | ❌ Both sides | ✅ SSH | ❌ Both sides | ✅ Agent |
| **Cross-platform** | ✅ | ✅ | ✅ | ⚠️ Limited | ✅ |
| **Active Development** | ✅ (Docker-backed) | ⚠️ Minimal | ✅ | ✅ | ❌ |

---

## Detailed Analysis

### 1. Mutagen

**How it works:**
- Uses rsync algorithm for differential transfers
- Filesystem watching (inotify/fsevents) for change detection
- Three-way merge algorithm with stored "last synchronized" state
- Agent injection: copies small binary to remote via scp/docker cp

**Performance:**
- rsync-like performance (excellent)
- Low-latency sync cycles (sub-second for small changes)
- Differential transfers for large files

**Conflict Resolution (4 modes):**
- `two-way-safe`: Auto-resolve if no data loss, else pause for user
- `two-way-resolved`: Alpha always wins
- `one-way-safe`: Changes only alpha→beta, protect beta edits
- `one-way-replica`: Beta = exact replica of alpha

**Setup:**
- Single binary, no remote installation needed
- Configuration via YAML or CLI flags
- Docker Compose integration available

**Docker Desktop:**
- Acquired by Docker in June 2023
- Now integrated into Docker Desktop as file sync solution
- Used for "Filesystem syncing" feature

---

### 2. Unison

**How it works:**
- Two-way file synchronizer
- Uses rsync-like algorithm for network efficiency
- Requires installation on BOTH endpoints
- Can run in "repeat" mode with filesystem monitor

**Performance:**
- Good for medium repos
- Can be slow with very large codebases
- Compression for network efficiency

**Conflict Resolution:**
- Interactive conflict resolution UI
- Shows diff and lets user choose
- Can set preferences for file types

**Setup:**
- Must be installed on both local and remote
- OCaml-based, requires compilation or binary
- Less active maintenance (2.5 maintainers)

---

### 3. rsync + SSH (with lsyncd)

**How it works:**
- rsync: one-way sync using rsync algorithm
- lsyncd: watches local filesystem, triggers rsync
- For bidirectional: need two lsyncd instances (complex)

**Real-time options:**
- lsyncd: batches changes every few seconds
- inotify/fsevents for immediate triggers

**Limitations:**
- Not native bidirectional
- Conflict resolution: none (last write wins)
- Complex for true two-way

---

### 4. SSHFS/FUSE

**How it works:**
- Mounts remote filesystem locally via SSH/SFTP
- Or reverse: mount local into container via sshfs daemon

**Performance:**
- Significant overhead for every file operation
- Not suitable for development (IDE lag, git slowness)
- Best for occasional file access, not development

**VS Code Remote:**
- VS Code recommends SSHFS for "single file edits"
- Explicitly warns: "performance will be significantly slower"
- Recommends rsync for "bulk file operations"

---

### 5. WebSocket File Sync (Current Workspace-SDK)

**Current approach (if implemented):**
- Custom file transfer over WebSocket
- Change detection on both sides
- Manual conflict resolution needed

**Challenges:**
- Must implement own change detection
- No standard conflict resolution
- Performance with binary files depends on implementation
- Must handle reconnection, partial transfers

---

## Industry Approaches

### Docker Desktop
- Uses Mutagen (acquired 2023)
- Also uses VirtioFS, gRPC-FUSE, osxfs for bind mounts
- Mutagen used when "synchronization" is preferred over "bind mounts"

### VS Code Remote (SSH)
- **No built-in sync** - runs entirely on remote
- Recommends SSHFS for convenience
- Recommends rsync for performance
- Code stays on remote, local is "view" only

### GitHub Codespaces
- Code runs entirely in cloud container
- No local file sync needed
- Browser or VS Code connects to remote

### Gitpod
- Also cloud-based development
- Uses cloud workspaces, not local sync

---

## Recommendations

### For Nexus Workspace: **Mutagen** is the clear winner

**Justification:**
1. **Battle-tested**: Used by Docker Desktop, production-ready
2. **Bidirectional**: Native support, handles the core requirement
3. **Conflict modes**: Multiple options for different use cases
4. **Performance**: rsync algorithm is industry standard for efficiency
5. **No remote install**: Agent injection solves deployment
6. **Active development**: Docker backing ensures longevity
7. **Handles all cases**: Code, git, build artifacts, node_modules

### Recommended Configuration for Nexus:

```
syncMode: two-way-safe  # Protect against data loss
ignores:
  - node_modules/
  - .git/
  - dist/
  - build/
  - "*.log"
```

**Special handling for node_modules:**
- Option 1: Exclude entirely, run `npm install` in container
- Option 2: One-way from container to host only (for caching)
- Option 3: Use Mutagen's `one-way-safe` for node_modules only

### Implementation Path:

1. Embed Mutagen binary in workspace-daemon
2. On workspace start: create sync session
3. Use Docker transport for container endpoints
4. Handle conflicts via Mutagen's built-in mechanisms
5. Expose conflict status to workspace-sdk

---

## Benchmark Reference

Based on Mutagen's own testing and Docker benchmarks:

| Operation | Mutagen | rsync (manual) | SSHFS |
|-----------|---------|----------------|-------|
| Initial sync (1GB) | ~30s | ~30s | N/A |
| Small file change | <100ms | N/A | ~500ms |
| Large file diff | ~5s | ~5s | N/A |
| Directory listing | Local | Local | ~200ms |

---

## Risks & Considerations

1. **node_modules**: Large, frequently changed - consider excluding
2. **Git operations**: Conflicts can occur if editing in both places
3. **Binary files**: Handled well by rsync algorithm
4. **Network latency**: Mutagen tolerates high-latency connections
5. **Container restarts**: Sessions persist, reconnection is automatic
