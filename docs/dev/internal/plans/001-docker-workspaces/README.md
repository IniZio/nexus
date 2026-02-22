# Docker Workspace Management PRD

**Version:** 1.2.0  
**Status:** Simplified for Implementation  
**Last Updated:** 2026-02-22  

## Overview

This PRD describes the Docker Workspace Management system for Nexus - a solution for frictionless parallel development environments combining git worktree isolation with containerized compute.

## Document Structure

| Document | Content |
|----------|---------|
| [01-overview.md](./01-overview.md) | Problem statement, goals, non-goals |
| [02-architecture.md](./02-architecture.md) | System design, components, data flow |
| [03-api.md](./03-api.md) | REST API, gRPC, WebSocket, CLI specifications |
| [04-security.md](./04-security.md) | SSH handling, secrets, threat model |
| [05-ssh-workspaces.md](./05-ssh-workspaces.md) | **SSH-based workspace access** (primary method) |
| [06-migration.md](./06-migration.md) | Migration guide: docker exec → SSH |
| [07-operations.md](./07-operations.md) | Deployment, monitoring, troubleshooting |
| [08-testing.md](./08-testing.md) | Testing strategy and benchmarks |
| [09-roadmap.md](./09-roadmap.md) | Implementation phases |

### Architecture Change Notice

**⚠️ Major Change:** This PRD has been updated to use **SSH-based workspace access** instead of `docker exec`. See:
- [05-ssh-workspaces.md](./05-ssh-workspaces.md) - Full SSH architecture
- [06-migration.md](./06-migration.md) - Migration guide

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
    path: /Users/newman/code/hanlun
    backend: docker
    ports: [3000, 5173]
    
  docs-site:
    path: /Users/newman/code/docs
    backend: docker
    image: node:18-alpine
```

### Key Commands

```bash
# Create workspace
boulder workspace create <name>

# Start/stop
boulder workspace up <name>
boulder workspace down <name>

# Switch (<2s)
boulder workspace switch <name>

# SSH into workspace (primary access method)
boulder ssh <name>
boulder ssh <name> -- npm test  # Execute command
boulder ssh <name> -L 3000:localhost:3000  # Port forwarding

# List all
boulder workspace list

# File sync status
boulder workspace sync-status <name>
```

### Success Criteria

- Workspace switch: <2 seconds
- Cold start: <30 seconds  
- Availability: 99.9%
- Zero data loss

---

**Migration Note:** This replaces the monolithic `001-docker-workspaces-prd.md` with a modular structure for easier maintenance.
