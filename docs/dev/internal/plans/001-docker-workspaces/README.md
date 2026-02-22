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
| [05-operations.md](./05-operations.md) | Deployment, monitoring, troubleshooting |
| [06-testing.md](./06-testing.md) | Testing strategy and benchmarks |
| [07-roadmap.md](./07-roadmap.md) | Implementation phases and migration |

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

# List all
boulder workspace list
```

### Success Criteria

- Workspace switch: <2 seconds
- Cold start: <30 seconds  
- Availability: 99.9%
- Zero data loss

---

**Migration Note:** This replaces the monolithic `001-docker-workspaces-prd.md` with a modular structure for easier maintenance.
