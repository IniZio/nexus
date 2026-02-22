# MOVED

This document has been reorganized into smaller, focused documents:

**New Location:** `docs/dev/internal/plans/001-docker-workspaces/`

| Old Section | New Document |
|-------------|--------------|
| Overview, Goals | [01-overview.md](./001-docker-workspaces/01-overview.md) |
| Architecture, Data Models | [02-architecture.md](./001-docker-workspaces/02-architecture.md) |
| API Specs | [03-api.md](./001-docker-workspaces/03-api.md) |
| Security, SSH | [04-security.md](./001-docker-workspaces/04-security.md) |
| Operations | [05-operations.md](./001-docker-workspaces/05-operations.md) |
| Testing | [06-testing.md](./001-docker-workspaces/06-testing.md) |
| Roadmap | [07-roadmap.md](./001-docker-workspaces/07-roadmap.md) |

## Key Changes

1. **Single Config File:** Configuration now uses `~/.nexus/config.yaml` only
   - No more separate `workspace.yaml` per workspace
   - Workspaces defined under `workspaces:` key in main config

2. **Simpler Configuration:**
   ```yaml
   # ~/.nexus/config.yaml
   daemon:
     port: 8080
     
   defaults:
     backend: docker
     idle_timeout: 30m
     
   workspaces:
     hanlun:
       path: /Users/newman/code/hanlun
       ports: [3000, 5173]
   ```

3. **Better Organization:** Each document focuses on one topic

---

*This file is kept for redirect purposes. Please refer to the new location for current documentation.*
