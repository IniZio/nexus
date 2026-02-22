# 5. Operations

## 5.1 On-Call Procedures

### Alert: Workspace Start Failure Rate > 5%

```
Severity: P2
Runbook:

1. Check system resources
   $ boulder admin stats
   
2. Check Docker daemon status
   $ docker system info
   
3. Check recent errors
   $ boulder admin logs --errors --last=1h
   
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
   $ boulder admin top --sort=memory
   
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
boulder admin workspace inspect <name>
# Shows: state, resources, ports, recent events

# View system logs
boulder admin workspace logs <name> --system
# Shows: daemon logs, not just app logs

# Execute with debug logging
boulder admin workspace exec <name> --debug

# Check workspace health
boulder admin workspace health <name>
```

### System Debugging

```bash
# System stats
boulder admin stats
# CPU, memory, disk, network usage

# Port allocation
boulder admin ports
# List all allocated ports

# Networks
boulder admin networks
# List Docker networks

# Full request trace
boulder admin trace <request-id>

# Diagnostic bundle
boulder admin support-bundle
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

---

## 5.4 Backup & Recovery

### Backup Procedure

```bash
# Create backup
boulder admin backup create
# Creates:
# - State store dump
# - Workspace metadata
# - User configurations

# List backups
boulder admin backup list

# Restore from backup
boulder admin backup restore <backup-id>
# Restores state, recreates workspaces
```

### Disaster Recovery

```bash
# 1. Restore state from backup
boulder admin backup restore latest

# 2. Verify worktrees exist
boulder admin worktree verify --repair

# 3. Recreate missing containers
boulder admin workspace repair --all

# 4. Validate
boulder admin health-check
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
| `WORKSPACE_NOT_FOUND` | "Workspace doesn't exist" | No retry | Suggest: `boulder workspace list` |
| `WORKSPACE_ALREADY_EXISTS` | "Workspace already exists" | No retry | Suggest: `boulder workspace switch` |
| `WORKSPACE_START_FAILED` | "Failed to start" | 3 retries, exponential backoff | Auto-retry or manual repair |
| `PORT_CONFLICT` | "Port X already in use" | 1 retry with new port | Auto-retry with different port |
| `RESOURCE_EXHAUSTED` | "Not enough resources" | Retry in 30s | Suggest: destroy unused workspaces |
| `BACKEND_UNAVAILABLE` | "Docker daemon not responding" | Retry in 5s | Auto-retry, escalate if persists |
| `CONTAINER_ERROR` | "Container crashed" | No retry | Suggest: `boulder workspace repair` |
| `PERMISSION_DENIED` | "You don't have permission" | No retry | Suggest: contact admin |
| `AUTHENTICATION_FAILED` | "Session expired" | No retry | Prompt: re-authenticate |
| `TIMEOUT` | "Operation timed out" | 1 retry | Auto-retry with increased timeout |
| `NETWORK_ERROR` | "Network connection failed" | 5 retries, exponential backoff | Auto-retry |
| `DISK_FULL` | "Not enough disk space" | No retry | Suggest: `boulder workspace cleanup` |

---

## 5.8 CLI Commands Reference

### Admin Commands

```bash
# System status
boulder admin status
boulder admin stats
boulder admin health-check

# Workspace management
boulder admin workspace list
boulder admin workspace inspect <name>
boulder admin workspace logs <name>
boulder admin workspace repair <name>
boulder admin workspace cleanup

# Network and ports
boulder admin ports
boulder admin networks

# Debugging
boulder admin trace <request-id>
boulder admin support-bundle
```

### Configuration Management

```bash
# View current config
boulder config show

# Edit config
boulder config edit

# Validate config
boulder config validate

# Reload config
boulder config reload
```
