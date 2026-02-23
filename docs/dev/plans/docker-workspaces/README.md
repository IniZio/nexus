# Docker Workspace Management PRD

**Version:** 2.0.0  
**Status:** Updated with Unified CLI, Port Forwarding & Lifecycle Management  
**Last Updated:** 2026-02-22  

## Overview

This PRD describes the Docker Workspace Management system for Nexus - a solution for frictionless parallel development environments combining git worktree isolation with containerized compute.

## Document Structure

| Document | Content |
|----------|---------|
| [01-overview.md](./01-overview.md) | Problem statement, goals, hanlun-lms requirements |
| [02-architecture.md](./02-architecture.md) | System design, port forwarding, lifecycle management |
| [03-api.md](./03-api.md) | REST API, gRPC, WebSocket, CLI specifications |
| [04-security.md](./04-security.md) | SSH handling, secrets, threat model |
| [05-ssh-workspaces.md](./05-ssh-workspaces.md) | SSH-based workspace access (primary method) |
| [06-migration.md](./06-migration.md) | Migration guide: docker exec → SSH |
| [05-operations.md](./05-operations.md) | Deployment, monitoring, troubleshooting |
| [06-testing.md](./06-testing.md) | Testing strategy and benchmarks |
| [07-roadmap.md](./07-roadmap.md) | Implementation phases |

### Architecture Change Notice

**⚠️ Major Change:** This PRD has been updated to use **SSH-based workspace access** instead of `docker exec`. See:
- [05-ssh-workspaces.md](./05-ssh-workspaces.md) - Full SSH architecture
- [06-migration.md](./06-migration.md) - Migration guide

**✅ Unified CLI:** All workspace operations use the `nexus` CLI directly. Boulder is an internal enforcement system, not a user-facing CLI command.

## Quick Reference

### Configuration

**Single config file:** `~/.nexus/config.yaml`

```yaml
# ~/.nexus/config.yaml
daemon:
  port: 8080

defaults:
  backend: docker
  idle_timeout: 30m
  resources: medium

workspaces:
  hanlun:
    path: /Users/dev/code/hanlun
    backend: docker
    # Port forwarding automatically detected from docker-compose.yml
    services:
      web:
        port: 3000
        auto_forward: true
        url_format: "{name}.localhost"
      api:
        port: 3001
        auto_forward: true
      postgres:
        port: 5432
        host_port: 15432  # Fixed port for local DB tools
```

### Key Commands

```bash
# Create workspace
nexus workspace create <name>

# Start/stop (with lifecycle hooks and service auto-start)
nexus workspace up <name>
nexus workspace down <name>

# Pause/resume (checkpoint state)
nexus workspace pause <name>
nexus workspace resume <name>

# Switch (<2s)
nexus workspace switch <name>

# SSH into workspace (primary access method)
nexus workspace ssh <name>
nexus workspace ssh <name> -- npm test  # Execute command

# Port forwarding (automatic from docker-compose or manual)
nexus workspace port list <name>
nexus workspace port add <name> <container-port>

# List all
nexus workspace list

# File sync status
nexus workspace sync-status <name>

# Lifecycle hooks
nexus workspace logs <name> --service=web
```

### Workspace Lifecycle

```
PENDING → STOPPED → RUNNING
            ↑        ↓
            └──── PAUSED
```

**Automatic on `nexus workspace up`:**
1. Start container
2. Run pre-start hooks
3. Start docker-compose services
4. Run health checks
5. Start file sync
6. Run post-start hooks

**Graceful shutdown on `nexus workspace down`:**
1. Run pre-stop hooks
2. Stop services in reverse dependency order
3. Run post-stop hooks
4. Stop container
5. Pause file sync

### Success Criteria

- Workspace switch: <2 seconds
- Cold start: <30 seconds  
- Service health check: <5 seconds
- Graceful shutdown: <10 seconds
- Availability: 99.9%
- Zero data loss

### hanlun-lms Reference Implementation

Typical web application with multiple services:
- **Frontend:** Next.js (port 3000)
- **Backend:** Node.js API (port 3001)
- **Database:** PostgreSQL (port 5432)
- **Cache:** Redis (port 6379)
- **Proxy:** Nginx (port 8080)

All ports auto-detected from `docker-compose.yml` and forwarded with user-friendly URLs.

---

**Migration Note:** This replaces the monolithic `001-docker-workspaces-prd.md` with a modular structure for easier maintenance.

**CLI Note:** Boulder is the internal enforcement engine. Users interact with `nexus` CLI only.
