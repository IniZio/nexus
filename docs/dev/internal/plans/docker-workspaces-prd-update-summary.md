# Docker Workspaces PRD Update Summary

**Date:** 2026-02-22  
**Version:** 2.0.0  
**Status:** Complete

## Changes Made

### 1. Unified CLI (Boulder → Nexus)

**All PRD files updated to use unified `nexus` CLI:**

| File | Changes |
|------|---------|
| README.md | Updated CLI commands, version 2.0.0, added lifecycle/forwarding sections |
| 01-overview.md | Added hanlun-lms reference implementation section |
| 02-architecture.md | Replaced boulder with nexus in diagrams and examples |
| 03-api.md | Replaced all boulder commands, added lifecycle RPCs and REST endpoints |
| 04-security.md | No boulder references (already clean) |
| 05-ssh-workspaces.md | Replaced boulder with nexus workspace ssh |
| 06-migration.md | Replaced boulder with nexus throughout |
| 05-operations.md | Replaced all boulder commands |
| 06-testing.md | Replaced all boulder commands |
| 07-roadmap.md | Replaced all boulder commands |

**Key CLI Changes:**
- `boulder workspace create` → `nexus workspace create`
- `boulder ssh <name>` → `nexus workspace ssh <name>`
- `boulder workspace exec` → `nexus workspace ssh <name> --`
- `boulder admin` → `nexus admin`

**Removed:**
- No `nexus boulder status|pause|resume` subcommand
- Boulder is now strictly an internal enforcement engine

### 2. Port Forwarding Design Added

**New section in 02-architecture.md:**

#### 2.9 Service Port Forwarding Architecture

- **2.9.1 Overview**: Automatic port detection and user-friendly URLs
- **2.9.2 Port Detection Strategy**: Parse docker-compose.yml with priority hierarchy
- **2.9.3 Port Allocation Strategy**: Service port ranges (32900-34999)
- **2.9.4 URL Generation**: localhost and custom domain support
- **2.9.5 Port Configuration Schema**: Full YAML configuration
- **2.9.6 Health Check Integration**: Wait for services before marking ready
- **2.9.7 CLI Integration**: `nexus workspace port list|add|open`

**Key Features:**
- Auto-detect services from docker-compose.yml
- User-friendly URLs: `http://localhost:32901`
- Health check integration
- Fixed host ports for databases (15432 for postgres, etc.)
- Service dependency management

### 3. Workspace Lifecycle Management Added

**New section in 02-architecture.md:**

#### 2.10 Workspace Lifecycle Management

- **2.10.1 State Machine**: PENDING → STOPPED → RUNNING ↔ PAUSED
- **2.10.2 Lifecycle Hooks**: pre-start, post-start, pre-stop, post-stop, health-check
- **2.10.3 Service Lifecycle**: Dependency-based startup/shutdown
- **2.10.4 Resource Cleanup**: Automatic garbage collection
- **2.10.5 Lifecycle API**: gRPC and REST endpoints
- **2.10.6 hanlun-lms Example**: Complete workflow example

**New CLI Commands:**
```bash
nexus workspace pause <name>
nexus workspace resume <name>
nexus workspace restart <name>
nexus workspace wait <name>
nexus workspace status <name>
nexus workspace hook run <name> <hook>
nexus workspace service list <name>
nexus workspace service logs <name> <service>
nexus workspace service restart <name> <service>
```

### 4. hanlun-lms Requirements Documented

**New section in 01-overview.md:**

#### 1.6 Reference Implementation: hanlun-lms

Complete documentation of the hanlun-lms project as a reference implementation:

**Project Profile:**
- Repository: git@github.com:oursky/hanlun-lms.git
- Stack: Next.js 14, Node.js, Express, tRPC, PostgreSQL 15, Redis
- Services: 6 (web, api, db, redis, worker, nginx)

**Port Requirements:**
| Service | Port | Purpose |
|---------|------|---------|
| Web (Next.js) | 3000 | Frontend development |
| API (Node.js) | 3001 | Backend API |
| PostgreSQL | 5432 | Database |
| Redis | 6379 | Cache |
| Nginx | 8080 | Reverse proxy |

**Configuration Example:**
Full `.nexus/config.yaml` example with services, hooks, and health checks.

**CLI Usage Example:**
Complete workflow from creation to pause/resume to service management.

### 5. API Updates

**03-api.md enhancements:**

#### REST API Additions

**Lifecycle Management:**
```http
POST /api/v1/workspaces/{id}/pause
POST /api/v1/workspaces/{id}/resume
GET /api/v1/workspaces/{id}/status
POST /api/v1/workspaces/{id}/hooks/{hook}
```

**Services:**
```http
GET /api/v1/workspaces/{id}/services
GET /api/v1/workspaces/{id}/services/{service}/logs
POST /api/v1/workspaces/{id}/services/{service}/restart
```

#### gRPC Additions

**New RPCs:**
- `PauseWorkspace`
- `ResumeWorkspace`
- `RestartWorkspace`
- `GetWorkspaceStatus`
- `WaitForWorkspace`
- `ListServices`
- `GetServiceLogs`
- `RestartService`

**New Messages:**
- `StartWorkspaceResponse`
- `ServiceHealth`
- `PauseWorkspaceRequest`
- `ResumeWorkspaceRequest`
- `ResumeWorkspaceResponse`
- `Service`
- `LogEntry`
- And more...

#### CLI Additions

```bash
# Lifecycle Management
nexus workspace pause <name>
nexus workspace resume <name>
nexus workspace restart <name>
nexus workspace wait <name>
nexus workspace status <name>
nexus workspace hook run <name> <hook>

# Service Management
nexus workspace service list <name>
nexus workspace service logs <name> <service>
nexus workspace service restart <name> <service>
nexus workspace service health <name> [service]

# Port Forwarding
nexus workspace port add <name> <container-port> --url-format=...
nexus workspace port list <name> --format=table|json
nexus workspace port remove <name> <port-id>
nexus workspace open <name> [service]
```

## Files Modified

```
docs/dev/internal/plans/001-docker-workspaces/
├── README.md (updated version, CLI commands)
├── 01-overview.md (+ hanlun-lms section)
├── 02-architecture.md (+ port forwarding + lifecycle sections)
├── 03-api.md (+ lifecycle REST/gRPC + updated CLI)
├── 05-ssh-workspaces.md (boulder → nexus)
├── 06-migration.md (boulder → nexus)
├── 05-operations.md (boulder → nexus)
├── 06-testing.md (boulder → nexus)
└── 07-roadmap.md (boulder → nexus)

Additional file created:
docs/dev/internal/plans/docker-workspaces-prd-update-plan.md
```

## Verification

✅ All `boulder` references replaced with `nexus`  
✅ No `nexus boulder` subcommand references remain  
✅ Port forwarding design documented  
✅ Lifecycle management documented  
✅ hanlun-lms requirements documented  
✅ All CLI examples updated  
✅ Architecture diagrams updated  
✅ API specifications updated  
✅ Version bumped to 2.0.0  

## Next Steps

1. Review updated PRD for accuracy
2. Sync implementation with updated design
3. Update AGENTS.md to reference nexus CLI (not boulder)
4. Update any other docs referencing boulder CLI
