# 5. Operations

## 5.1 On-Call Procedures

### Alert: Workspace Start Failure Rate > 5%

```
Severity: P2
Runbook:

1. Check system resources
   $ nexus admin stats
   
2. Check Docker daemon status
   $ docker system info
   
3. Check recent errors
   $ nexus admin logs --errors --last=1h
   
4. Common causes:
   a. Disk full → Cleanup old workspaces
   b. Image pull failures → Check registry auth
   c. Port exhaustion → Check for leaked ports
   
5. Escalate if:
   - Error rate > 20%
   - Affects > 10 users
   - Persists > 30 min
```

### Alert: High Memory Usage

```
Severity: P3
Runbook:

1. Identify top memory consumers
   $ nexus admin top --sort=memory
   
2. Options:
   a. Contact users to stop unused workspaces
   b. Force-stop idle workspaces (>24h)
   c. Add more memory to host
   
3. Prevention:
   - Lower default resource class
   - Enable auto-shutdown
```

---

## 5.2 Debugging Commands

### Workspace Debugging

```bash
# Inspect workspace details
nexus admin workspace inspect <name>
# Shows: state, resources, ports, recent events

# View system logs
nexus admin workspace logs <name> --system
# Shows: daemon logs, not just app logs

# Execute with debug logging
nexus admin workspace exec <name> --debug

# Check workspace health
nexus admin workspace health <name>

# Check file sync status
nexus admin workspace sync-status <name>
# Shows: sync provider, status, last sync, conflicts

# List sync conflicts
nexus admin workspace sync-conflicts <name>

# Force sync flush
nexus admin workspace sync-flush <name>
```

### System Debugging

```bash
# System stats
nexus admin stats
# CPU, memory, disk, network usage

# Port allocation
nexus admin ports
# List all allocated ports

# Networks
nexus admin networks
# List Docker networks

# Full request trace
nexus admin trace <request-id>

# Diagnostic bundle
nexus admin support-bundle
# Collects logs, config, stats for support
```

---

## 5.3 Common Issues & Resolution

| Issue | Symptoms | Diagnosis | Resolution |
|-------|----------|-----------|------------|
| **Workspace stuck in PENDING** | Create hangs | Check Docker logs | Restart Docker daemon |
| **Port already in use** | Start fails | `lsof -i :PORT` | Kill process or reassign port |
| **Container exits immediately** | Start then stop | Check container logs | Fix app crash or config |
| **Slow file operations** | High latency | Check disk I/O | Add SSD, reduce workspace count |
| **Git auth failures** | Clone fails | Check credentials | Refresh token, check SSH keys |
| **Out of disk** | Operations fail | `df -h` | Cleanup, increase disk |
| **Network timeouts** | External requests fail | Check proxy, DNS | Verify network config |
| **High CPU** | System slow | `top` / `htop` | Identify and throttle workspace |
| **Sync not working** | File changes not propagating | Check `sync-status` | Pause/resume sync, restart daemon |
| **Sync conflicts** | Conflicting file versions | Check `sync-conflicts` | Resolve with `sync-resolve` |
| **High sync latency** | Slow file propagation | Check network, file count | Exclude large directories, use polling |

---

## 5.4 Backup & Recovery

### Backup Procedure

```bash
# Create backup
nexus admin backup create
# Creates:
# - State store dump
# - Workspace metadata
# - User configurations

# List backups
nexus admin backup list

# Restore from backup
nexus admin backup restore <backup-id>
# Restores state, recreates workspaces
```

### Disaster Recovery

```bash
# 1. Restore state from backup
nexus admin backup restore latest

# 2. Verify worktrees exist
nexus admin worktree verify --repair

# 3. Recreate missing containers
nexus admin workspace repair --all

# 4. Validate
nexus admin health-check
```

---

## 5.5 Maintenance Windows

### Scheduled Maintenance

```
1. Weekly (Sundays 2am)
   - Cleanup dangling images
   - Prune unused volumes
   - Compact state database

2. Monthly (First Sunday)
   - Update base images
   - Security patches
   - Major version upgrades

3. Ad-hoc
   - Emergency security updates
   - Critical bug fixes

Communication:
- 7 days notice for scheduled
- 24 hours notice for security
- In-app notifications
```

---

## 5.6 Performance Monitoring

### Key Metrics

```typescript
// Real-time performance dashboard
const PERFORMANCE_SLIs = {
  coldStart: {
    p50: '< 15s',
    p95: '< 30s',
    p99: '< 60s',
  },
  warmStart: {
    p50: '< 1s',
    p95: '< 2s',
    p99: '< 5s',
  },
  contextSwitch: {
    p50: '< 1s',
    p95: '< 2s',
    p99: '< 5s',
  },
  syncLatency: {
    p50: '< 200ms',
    p95: '< 500ms',
    p99: '< 2s',
  },
  initialSync: {
    p50: '< 5s',
    p95: '< 10s',
    p99: '< 30s',
  },
};
```

### Resource Limits

| Limit | Value | Notes |
|-------|-------|-------|
| Max workspaces per host | 50 | Based on 16GB RAM |
| Max ports per workspace | 10 | Configurable |
| Max concurrent operations | 20 | Prevent resource exhaustion |
| Max snapshot size | 100GB | Per-workspace |
| Max workspace lifetime | 30 days | Auto-cleanup |
| Max inactive time | 7 days | Before auto-stop |

---

## 5.7 Error Handling Matrix

| Error Code | User Message | Retry Strategy | Recovery Action |
|------------|--------------|----------------|-----------------|
| `WORKSPACE_NOT_FOUND` | "Workspace doesn't exist" | No retry | Suggest: `nexus workspace list` |
| `WORKSPACE_ALREADY_EXISTS` | "Workspace already exists" | No retry | Suggest: `nexus workspace switch` |
| `WORKSPACE_START_FAILED` | "Failed to start" | 3 retries, exponential backoff | Auto-retry or manual repair |
| `PORT_CONFLICT` | "Port X already in use" | 1 retry with new port | Auto-retry with different port |
| `RESOURCE_EXHAUSTED` | "Not enough resources" | Retry in 30s | Suggest: destroy unused workspaces |
| `BACKEND_UNAVAILABLE` | "Docker daemon not responding" | Retry in 5s | Auto-retry, escalate if persists |
| `CONTAINER_ERROR` | "Container crashed" | No retry | Suggest: `nexus workspace repair` |
| `PERMISSION_DENIED` | "You don't have permission" | No retry | Suggest: contact admin |
| `AUTHENTICATION_FAILED` | "Session expired" | No retry | Prompt: re-authenticate |
| `TIMEOUT` | "Operation timed out" | 1 retry | Auto-retry with increased timeout |
| `NETWORK_ERROR` | "Network connection failed" | 5 retries, exponential backoff | Auto-retry |
| `DISK_FULL` | "Not enough disk space" | No retry | Suggest: `nexus workspace cleanup` |
| `SYNC_SESSION_FAILED` | "File sync failed to start" | 3 retries | Check Mutagen installation, retry |
| `SYNC_CONFLICT` | "File sync conflict detected" | No retry | Run `sync-conflicts` to view and resolve |
| `SYNC_PAUSED` | "File sync is paused" | Auto-resume on workspace start | Resume sync with `sync-resume` |
| `SYNC_PROVIDER_NOT_FOUND` | "Mutagen not installed" | No retry | Install Mutagen or use embedded mode |

---

## 5.8 CLI Commands Reference

### Admin Commands

```bash
# System status
nexus admin status
nexus admin stats
nexus admin health-check

# Workspace management
nexus admin workspace list
nexus admin workspace inspect <name>
nexus admin workspace logs <name>
nexus admin workspace repair <name>
nexus admin workspace cleanup

# Network and ports
nexus admin ports
nexus admin networks

# Debugging
nexus admin trace <request-id>
nexus admin support-bundle
```

### Configuration Management

```bash
# View current config
nexus config show

# Edit config
nexus config edit

# Validate config
nexus config validate

# Reload config
nexus config reload
```
